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

// Command fakes serves local fakes of every upstream the store calls
// (transaction-service, HR Entity GraphQL, email-service and an OAuth2 token
// endpoint) on one loopback port, so the store runs without Choreo:
//
//	go run ./cmd/fakes
//
// Point TRANSACTION_SERVICE_BASE_URL and EMAIL_BASE_URL at http://127.0.0.1:8081,
// HR_ENTITY_BASE_URL at http://127.0.0.1:8081/graphql and OAUTH2_TOKEN_URL at
// http://127.0.0.1:8081/token. FAKES_ADDR changes the address; it must be a
// loopback one. FAKE_TREASURY_ADDRESS changes the treasury wallet. All state
// is in memory and lost on exit. Local use only: nothing here is verified.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const (
	defaultAddr     = "127.0.0.1:8081"
	defaultTreasury = "0xfa4e00000000000000000000000000000000beef"
)

func main() {
	addr, err := listenAddr(os.Getenv)
	if err != nil {
		slog.Error("fakes: refusing to start", "error", err)
		os.Exit(1)
	}
	treasury := treasuryAddress(os.Getenv)
	srv := &http.Server{Addr: addr, Handler: newHandler(treasury), ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	slog.Info("fakes listening", "addr", addr, "treasury", treasury)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("fakes: server stopped", "error", err)
		os.Exit(1)
	}
}

// listenAddr returns FAKES_ADDR, or the default. Anything but a loopback
// address is refused: the fakes accept any token and must never be reachable
// from another machine.
func listenAddr(getenv func(string) string) (string, error) {
	addr := getenv("FAKES_ADDR")
	if addr == "" {
		addr = defaultAddr
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("FAKES_ADDR %q: %w", addr, err)
	}
	if host == "localhost" {
		return addr, nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return addr, nil
	}
	return "", fmt.Errorf("FAKES_ADDR %q is not a loopback address", addr)
}

// treasuryAddress returns FAKE_TREASURY_ADDRESS, or the default.
func treasuryAddress(getenv func(string) string) string {
	if a := getenv("FAKE_TREASURY_ADDRESS"); a != "" {
		return a
	}
	return defaultTreasury
}

// newHandler serves every fake on one mux.
func newHandler(treasury string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", handleToken)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	newLedger(treasury).register(mux)
	mux.HandleFunc("POST /graphql", handleGraphQL)
	newMailbox().register(mux)
	return mux
}

// handleToken is an OAuth2 client-credentials token endpoint that accepts
// any client.
func handleToken(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"access_token": "fake-token", "token_type": "Bearer", "expires_in": 3600})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeMessage(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"message": msg})
}

// readJSON decodes a request body of at most limit bytes and refuses unknown
// fields, so a client that drifts from the contract fails locally. An empty
// body leaves v as it is.
func readJSON(r *http.Request, limit int64, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, limit))
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}
