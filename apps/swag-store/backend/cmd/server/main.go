// Copyright (c) 2026 WSO2 LLC. (https://www.wso2.com).
//
// WSO2 LLC. licenses this file to you under the Apache License,
// Version 2.0 (the "License"); you may not use this file except
// in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

// Command server runs the Swag Store API. main.go is the composition root:
// config, database, upstream clients, handlers, route table, middleware chain,
// jobs and graceful shutdown.
package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // STORE_TIMEZONE must load even on an image without zoneinfo

	"github.com/gin-gonic/gin"
	_ "github.com/go-sql-driver/mysql"
	"github.com/golang-jwt/jwt/v5"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/checkout"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/coins"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/handler"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/hr"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/middleware"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/migrate"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/notifications"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/repository"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/migrations"
)

func main() {
	middleware.ConfigureLogger()
	if err := run(); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	if err := loadDotEnv(".env"); err != nil {
		return err
	}
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		return err
	}
	if !cfg.Auth.ValidatorEnabled {
		slog.Warn("AUTH_TOKEN_VALIDATOR_ENABLED=false: tokens are decoded without a signature check (local only)")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := openDB(ctx, cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = migrate.CheckCurrent(checkCtx, db, migrations.FS)
	cancel()
	if err != nil {
		return err
	}

	coinsClient := coins.NewClient(coins.Config{
		BaseURL: cfg.TransactionService.BaseURL, TokenURL: cfg.TransactionService.TokenURL,
		ClientID: cfg.TransactionService.ClientID, ClientSecret: cfg.TransactionService.ClientSecret,
		Scopes: cfg.TransactionService.Scopes, Timeout: cfg.UpstreamTimeout,
	})
	hrClient := hr.NewClient(hr.Config{
		BaseURL: cfg.HR.BaseURL, TokenURL: cfg.HR.TokenURL, ClientID: cfg.HR.ClientID,
		ClientSecret: cfg.HR.ClientSecret, Scopes: cfg.HR.Scopes, Timeout: cfg.UpstreamTimeout,
	})
	employees := hr.NewCache(hrClient, cfg.HRCacheTTL)
	treasury := checkout.NewTreasuryGuard(cfg.TreasuryWallet, coinsClient)

	var keyFunc jwt.Keyfunc
	if cfg.Auth.ValidatorEnabled {
		keyFunc, err = middleware.NewJWKSKeyfunc(ctx, cfg.JWKSEndpoint)
		if err != nil {
			return err
		}
	}

	repo := repository.New(db)
	svc := checkout.NewService(repo, coinsClient, employees, treasury)

	// Email is optional: without EMAIL_BASE_URL the outbox rows wait.
	emailProbe := handler.Probe{Name: "email", Check: func(context.Context) string { return handler.StatusNotConfigured }}
	if cfg.Email.BaseURL != "" {
		emailClient := notifications.NewEmailClient(notifications.EmailConfig{
			BaseURL: cfg.Email.BaseURL, TokenURL: cfg.Email.TokenURL, ClientID: cfg.Email.ClientID,
			ClientSecret: cfg.Email.ClientSecret, Scopes: cfg.Email.Scopes, FromAddress: cfg.EmailFromAddress,
			Timeout: cfg.UpstreamTimeout,
		})
		emailProbe = handler.ErrProbe("email", emailClient.Health)
	}

	guard := handler.NewAccessGuard(cfg.Access)
	listCache := handler.NewListCache()
	a := &app{
		health: handler.NewHealthHandler([]handler.Probe{
			handler.ErrProbe("database", db.PingContext),
			handler.ErrProbe("transactionService", coinsClient.Health),
			{Name: "treasury", Check: func(ctx context.Context) string {
				treasury.Allow(ctx)
				return string(treasury.State())
			}},
			handler.ErrProbe("hrEntity", hrClient.Health),
			emailProbe,
		}),
		users:   handler.NewUsersHandler(guard, employees),
		wallets: handler.NewWalletsHandler(coinsClient),
		catalog: handler.NewCatalogHandler(repo, listCache, cfg.LowStockThreshold),
		cart:    handler.NewCartHandler(repo, cfg.LowStockThreshold),
		offices: handler.NewOfficesHandler(repo, employees),
		orders:  handler.NewOrdersHandler(svc, repo),
		alerts:  handler.NewAlertsHandler(repo),
	}

	gin.SetMode(gin.ReleaseMode)
	router := newRouter(routerDeps{
		auth:        middleware.Auth(cfg.Auth, keyFunc),
		guard:       guard,
		limiter:     middleware.NewLimiter(),
		corsOrigins: cfg.CORSAllowedOrigins,
		app:         a,
	})

	srv := &http.Server{
		Addr:              net.JoinHostPort("", cfg.Port),
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       60 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return context.WithoutCancel(ctx) },
	}
	errCh := make(chan error, 1)
	go func() {
		slog.Info("swag store listening", "port", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

func openDB(ctx context.Context, cfg *Config) (*sql.DB, error) {
	db, err := sql.Open("mysql", cfg.DatabaseDSN)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(cfg.DBMaxOpenConns)
	db.SetMaxIdleConns(max(1, cfg.DBMaxOpenConns/2))
	db.SetConnMaxLifetime(5 * time.Minute)
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, errors.New("database unreachable: " + err.Error())
	}
	return db, nil
}
