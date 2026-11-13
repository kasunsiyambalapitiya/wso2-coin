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

package main

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/assets"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/handler"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/httpsec"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/middleware"
)

// upstreamConfig is one upstream service: its base URL and the OAuth2
// client-credentials app it authenticates with.
type upstreamConfig struct {
	BaseURL      string
	TokenURL     string
	ClientID     string
	ClientSecret string
	Scopes       []string
}

// Config is every setting the service reads. All of it comes from env vars.
type Config struct {
	Port               string
	ReadTimeout        time.Duration
	WriteTimeout       time.Duration
	UpstreamTimeout    time.Duration
	CORSAllowedOrigins []string
	JobsEnabled        bool

	JWKSEndpoint string
	Auth         middleware.AuthConfig
	Access       handler.AccessConfig

	DatabaseDSN    string
	DBMaxOpenConns int

	OAuth2TokenURL     string
	TransactionService upstreamConfig
	HR                 upstreamConfig
	Email              upstreamConfig // BaseURL empty = email not configured
	EmailFromAddress   string
	EmailDebug         []string

	TreasuryWallet      string
	ContactEmail        string
	WebURL              string
	HRCacheTTL          time.Duration
	LowStockThreshold   int
	PickupReminderDays  int
	UncollectedFlagDays int
	Timezone            *time.Location

	// Product photos. Assets.AccountURL empty means photos are off.
	Assets         assets.Config
	MaxUploadBytes int64
}

// containerNameRe is Azure's container naming rule, without the length.
var containerNameRe = regexp.MustCompile(`^[a-z0-9](?:-?[a-z0-9])*$`)

// photoStorage checks the photo settings. Nothing here calls Azure: a Blob
// outage or a missing role must never stop the store from starting.
func (e *envReader) photoStorage(a *assets.Config) {
	if n := len(a.Container); n < 3 || n > 63 || !containerNameRe.MatchString(a.Container) {
		e.errs = append(e.errs, fmt.Errorf("ASSETS_CONTAINER must be 3 to 63 lower-case letters, digits and single hyphens, got %q", a.Container))
	}
	if a.AccountURL == "" {
		return
	}
	a.AccountURL = strings.TrimRight(a.AccountURL, "/")
	e.secureURL("ASSETS_ACCOUNT_URL", a.AccountURL)
	u, err := url.Parse(a.AccountURL)
	if err != nil {
		return // secureURL has reported it
	}
	loopback := httpsec.IsLoopbackURL(a.AccountURL)
	switch {
	case u.RawQuery != "" || u.Fragment != "":
		e.errs = append(e.errs, errors.New("ASSETS_ACCOUNT_URL must not carry a query"))
	case u.Path != "" && !loopback:
		e.errs = append(e.errs, errors.New("ASSETS_ACCOUNT_URL takes no path; a path is only for the local emulator"))
	}
	if a.AccountKey != "" && !loopback {
		e.errs = append(e.errs, errors.New("ASSETS_ACCOUNT_KEY is only for the local emulator on a loopback ASSETS_ACCOUNT_URL"))
	}
	if u.Scheme == "http" && a.AccountKey == "" {
		e.errs = append(e.errs, errors.New("ASSETS_ACCOUNT_KEY is required with an http ASSETS_ACCOUNT_URL: Entra sign-in needs https"))
	}
	if a.ClientSecret != "" && (a.TenantID == "" || a.ClientID == "") {
		e.errs = append(e.errs, errors.New("AZURE_CLIENT_SECRET needs AZURE_TENANT_ID and AZURE_CLIENT_ID"))
	}
}

// sqlMode is the strict mode every connection runs with.
const sqlMode = "'STRICT_ALL_TABLES,NO_ZERO_IN_DATE,NO_ZERO_DATE,ERROR_FOR_DIVISION_BY_ZERO,NO_ENGINE_SUBSTITUTION,ONLY_FULL_GROUP_BY'"

// envReader collects every problem, so one failed start names all of them.
type envReader struct {
	get  func(string) string
	errs []error
}

func (e *envReader) str(name string) string { return strings.TrimSpace(e.get(name)) }

func (e *envReader) or(name, def string) string {
	if v := e.str(name); v != "" {
		return v
	}
	return def
}

func (e *envReader) must(name string) string {
	v := e.str(name)
	if v == "" {
		e.errs = append(e.errs, fmt.Errorf("%s is required", name))
	}
	return v
}

func (e *envReader) duration(name string, def time.Duration) time.Duration {
	v := e.str(name)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		e.errs = append(e.errs, fmt.Errorf("%s must be a positive Go duration such as 45s, got %q", name, v))
		return def
	}
	return d
}

func (e *envReader) count(name string, def, min int) int {
	v := e.str(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < min {
		e.errs = append(e.errs, fmt.Errorf("%s must be a whole number of at least %d, got %q", name, min, v))
		return def
	}
	return n
}

func (e *envReader) boolOn(name string) bool {
	switch strings.ToLower(e.str(name)) {
	case "", "true", "1", "yes":
		return true
	case "false", "0", "no":
		return false
	default:
		e.errs = append(e.errs, fmt.Errorf("%s must be true or false, got %q", name, e.str(name)))
		return true
	}
}

func (e *envReader) port(name, def string) string {
	v := e.or(name, def)
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > 65535 {
		e.errs = append(e.errs, fmt.Errorf("%s must be a port number, got %q", name, v))
	}
	return v
}

func (e *envReader) secureURL(name, value string) {
	if value == "" {
		return
	}
	if err := httpsec.RequireSecureURL(value); err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s: %w", name, err))
	}
}

// splitComma splits on commas, trims, and drops empty items.
func splitComma(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// loadConfig reads and checks every setting. The error names each variable
// that is missing or malformed.
func loadConfig(get func(string) string) (*Config, error) {
	e := &envReader{get: get}
	c := &Config{
		Port:               e.port("PORT", "8080"),
		ReadTimeout:        e.duration("REST_READ_TIMEOUT", 60*time.Second),
		WriteTimeout:       e.duration("REST_WRITE_TIMEOUT", 60*time.Second),
		UpstreamTimeout:    e.duration("UPSTREAM_TIMEOUT", 25*time.Second),
		CORSAllowedOrigins: splitComma(e.str("CORS_ALLOWED_ORIGINS")),
		JobsEnabled:        !strings.EqualFold(e.str("JOBS_ENABLED"), "false"),
		JWKSEndpoint:       e.must("AUTH_JWKS_ENDPOINT"),
	}
	c.Auth = middleware.AuthConfig{
		Issuer:           e.must("AUTH_ISSUER"),
		Audiences:        splitComma(e.must("AUTH_AUDIENCE")),
		ClockSkew:        30 * time.Second,
		ValidatorEnabled: e.boolOn("AUTH_TOKEN_VALIDATOR_ENABLED"),
	}
	c.Access = handler.AccessConfig{
		CatalogManagerGroups: splitComma(e.str("AUTH_CATALOG_MANAGER_GROUPS")),
		FulfilmentOpsGroups:  splitComma(e.str("AUTH_FULFILMENT_OPS_GROUPS")),
		AllowedEmailDomains:  splitComma(e.or("AUTH_ALLOWED_EMAIL_DOMAINS", "wso2.com")),
	}

	c.DatabaseDSN = databaseDSN(e)
	c.DBMaxOpenConns = e.count("DB_MAX_OPEN_CONNS", 10, 1)

	clientID := e.must("OAUTH2_CLIENT_ID")
	secret := e.must("OAUTH2_CLIENT_SECRET")
	c.OAuth2TokenURL = e.must("OAUTH2_TOKEN_URL")
	e.secureURL("OAUTH2_TOKEN_URL", c.OAuth2TokenURL)
	shared := func(base, scopes string) upstreamConfig {
		return upstreamConfig{BaseURL: base, TokenURL: c.OAuth2TokenURL, ClientID: clientID, ClientSecret: secret, Scopes: splitComma(scopes)}
	}
	c.TransactionService = shared(e.must("TRANSACTION_SERVICE_BASE_URL"), e.str("TRANSACTION_SERVICE_SCOPES"))
	e.secureURL("TRANSACTION_SERVICE_BASE_URL", c.TransactionService.BaseURL)
	c.HR = shared(e.must("HR_ENTITY_BASE_URL"), e.str("HR_ENTITY_SCOPES"))
	e.secureURL("HR_ENTITY_BASE_URL", c.HR.BaseURL)
	if v := e.str("HR_ENTITY_TOKEN_URL"); v != "" {
		c.HR.TokenURL = v
		e.secureURL("HR_ENTITY_TOKEN_URL", v)
	}
	if v := e.str("HR_ENTITY_CLIENT_ID"); v != "" {
		c.HR.ClientID = v
		c.HR.ClientSecret = e.must("HR_ENTITY_CLIENT_SECRET")
	}
	c.Email = shared(e.str("EMAIL_BASE_URL"), e.str("EMAIL_SCOPES"))
	e.secureURL("EMAIL_BASE_URL", c.Email.BaseURL)
	if c.Email.BaseURL != "" {
		c.EmailFromAddress = e.must("EMAIL_FROM_ADDRESS")
	}
	c.EmailDebug = splitComma(e.str("EMAIL_DEBUG_RECIPIENTS"))

	c.TreasuryWallet = e.must("STORE_TREASURY_WALLET_ADDRESS")
	c.ContactEmail = e.must("STORE_CONTACT_EMAIL")
	c.WebURL = e.must("STORE_WEB_URL")
	e.secureURL("STORE_WEB_URL", c.WebURL)
	c.HRCacheTTL = e.duration("HR_CACHE_TTL", time.Hour)
	c.LowStockThreshold = e.count("LOW_STOCK_THRESHOLD", 5, 0)
	c.PickupReminderDays = e.count("PICKUP_REMINDER_DAYS", 7, 1)
	c.UncollectedFlagDays = e.count("UNCOLLECTED_FLAG_DAYS", 30, 1)
	tz, err := time.LoadLocation(e.or("STORE_TIMEZONE", "Asia/Colombo"))
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("STORE_TIMEZONE: %w", err))
		tz = time.UTC
	}
	c.Timezone = tz

	c.MaxUploadBytes = int64(e.count("ASSETS_MAX_UPLOAD_BYTES", 5<<20, 1))
	c.Assets = assets.Config{
		AccountURL:   e.str("ASSETS_ACCOUNT_URL"),
		Container:    e.or("ASSETS_CONTAINER", "swag-store-assets"),
		AccountKey:   e.str("ASSETS_ACCOUNT_KEY"),
		TenantID:     e.str("AZURE_TENANT_ID"),
		ClientID:     e.str("AZURE_CLIENT_ID"),
		ClientSecret: e.str("AZURE_CLIENT_SECRET"),
		Timeout:      c.UpstreamTimeout,
	}
	e.photoStorage(&c.Assets)

	// Decode-only tokens are for local runs: every upstream must be loopback.
	if !c.Auth.ValidatorEnabled {
		for name, u := range map[string]string{
			"TRANSACTION_SERVICE_BASE_URL": c.TransactionService.BaseURL,
			"HR_ENTITY_BASE_URL":           c.HR.BaseURL,
			"EMAIL_BASE_URL":               c.Email.BaseURL,
			"OAUTH2_TOKEN_URL":             c.OAuth2TokenURL,
		} {
			if u != "" && !httpsec.IsLoopbackURL(u) {
				e.errs = append(e.errs, fmt.Errorf("AUTH_TOKEN_VALIDATOR_ENABLED=false is allowed only when every upstream is loopback; %s is not", name))
			}
		}
	}
	if len(e.errs) > 0 {
		return nil, errors.Join(e.errs...)
	}
	return c, nil
}

// databaseDSN builds the go-sql-driver DSN from DATABASE_URL, or from the
// DB_* variables, and forces the session settings the store relies on.
func databaseDSN(e *envReader) string {
	var mc *mysql.Config
	if raw := e.str("DATABASE_URL"); raw != "" {
		parsed, err := mysql.ParseDSN(raw)
		if err != nil {
			e.errs = append(e.errs, fmt.Errorf("DATABASE_URL is not a valid go-sql-driver DSN: %w", err))
			return ""
		}
		mc = parsed
	} else {
		mc = mysql.NewConfig()
		mc.Net = "tcp"
		mc.Addr = e.must("DB_HOST") + ":" + e.port("DB_PORT", "3306")
		mc.User = e.must("DB_USER")
		mc.Passwd = e.must("DB_PASSWORD")
		mc.DBName = e.must("DB_NAME")
	}
	mc.ParseTime = true
	mc.Loc = time.UTC
	if mc.Params == nil {
		mc.Params = map[string]string{}
	}
	mc.Params["time_zone"] = "'+00:00'"
	mc.Params["transaction_isolation"] = "'READ-COMMITTED'"
	mc.Params["sql_mode"] = sqlMode
	if e.boolOn("DB_TLS") {
		if pem := e.str("DB_CA_CERT"); pem != "" {
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM([]byte(strings.ReplaceAll(pem, `\n`, "\n"))) {
				e.errs = append(e.errs, errors.New("DB_CA_CERT holds no PEM certificate"))
			} else if err := mysql.RegisterTLSConfig("swag", &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}); err != nil {
				e.errs = append(e.errs, fmt.Errorf("DB_CA_CERT: %w", err))
			}
			mc.TLSConfig = "swag"
		} else {
			mc.TLSConfig = "true"
		}
	} else {
		mc.TLSConfig = ""
	}
	return mc.FormatDSN()
}

// loadDotEnv reads KEY=VALUE lines from path for local runs. A missing file
// is fine, and a variable already set in the environment always wins.
func loadDotEnv(path string) error {
	f, err := os.Open(path) // #nosec G304 -- fixed local file name chosen by main
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if _, set := os.LookupEnv(key); set {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return sc.Err()
}
