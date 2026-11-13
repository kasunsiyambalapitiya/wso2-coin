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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

func baseEnv() map[string]string {
	return map[string]string{
		"AUTH_JWKS_ENDPOINT":            "https://idp.example/jwks",
		"AUTH_ISSUER":                   "https://idp.example",
		"AUTH_AUDIENCE":                 "swag-store, other",
		"DB_HOST":                       "db.example",
		"DB_USER":                       "swag",
		"DB_PASSWORD":                   "pw",
		"DB_NAME":                       "hris_swag_store",
		"OAUTH2_CLIENT_ID":              "cid",
		"OAUTH2_CLIENT_SECRET":          "secret",
		"OAUTH2_TOKEN_URL":              "https://idp.example/token",
		"TRANSACTION_SERVICE_BASE_URL":  "https://ts.example",
		"HR_ENTITY_BASE_URL":            "https://hr.example/graphql",
		"STORE_TREASURY_WALLET_ADDRESS": "0xtreasury",
		"STORE_CONTACT_EMAIL":           "swag@example.com",
		"STORE_WEB_URL":                 "https://one.example",
	}
}

func envFunc(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := loadConfig(envFunc(baseEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != "8080" || cfg.ReadTimeout != 60*time.Second || cfg.UpstreamTimeout != 25*time.Second {
		t.Fatalf("server defaults: %+v", cfg)
	}
	if !cfg.Auth.ValidatorEnabled || cfg.Auth.ClockSkew != 30*time.Second {
		t.Fatalf("auth defaults: %+v", cfg.Auth)
	}
	if strings.Join(cfg.Auth.Audiences, "|") != "swag-store|other" {
		t.Fatalf("audiences = %v", cfg.Auth.Audiences)
	}
	if strings.Join(cfg.Access.AllowedEmailDomains, ",") != "wso2.com" {
		t.Fatalf("domains = %v", cfg.Access.AllowedEmailDomains)
	}
	if len(cfg.Access.CatalogManagerGroups) != 0 || len(cfg.Access.FulfilmentOpsGroups) != 0 {
		t.Fatalf("role groups must default to none: %+v", cfg.Access)
	}
	if !cfg.JobsEnabled || cfg.HRCacheTTL != time.Hour || cfg.LowStockThreshold != 5 ||
		cfg.PickupReminderDays != 7 || cfg.UncollectedFlagDays != 30 || cfg.Timezone.String() != "Asia/Colombo" {
		t.Fatalf("store defaults: %+v", cfg)
	}
	if cfg.HR.TokenURL != cfg.OAuth2TokenURL || cfg.HR.ClientID != "cid" {
		t.Fatalf("HR must default to the shared app: %+v", cfg.HR)
	}
	if mc, err := mysql.ParseDSN(cfg.DatabaseDSN); err != nil || mc.Loc != time.UTC {
		t.Fatalf("DSN location: %v %v", err, mc)
	}
	for _, want := range []string{"parseTime=true", "time_zone", "transaction_isolation", "sql_mode", "tls=true", "db.example:3306", "hris_swag_store"} {
		if !strings.Contains(cfg.DatabaseDSN, want) {
			t.Errorf("DSN missing %q: %s", want, cfg.DatabaseDSN)
		}
	}
}

func TestLoadConfigNamesEveryMissingVar(t *testing.T) {
	_, err := loadConfig(envFunc(map[string]string{}))
	if err == nil {
		t.Fatal("empty env loaded")
	}
	for _, v := range []string{"AUTH_JWKS_ENDPOINT", "AUTH_ISSUER", "AUTH_AUDIENCE", "OAUTH2_CLIENT_ID",
		"OAUTH2_CLIENT_SECRET", "OAUTH2_TOKEN_URL", "TRANSACTION_SERVICE_BASE_URL", "HR_ENTITY_BASE_URL",
		"STORE_TREASURY_WALLET_ADDRESS", "STORE_CONTACT_EMAIL", "STORE_WEB_URL", "DB_HOST"} {
		if !strings.Contains(err.Error(), v) {
			t.Errorf("error does not name %s: %v", v, err)
		}
	}
}

func TestLoadConfigRejectsMalformed(t *testing.T) {
	cases := map[string]map[string]string{
		"bad port":         {"PORT": "80a"},
		"bad duration":     {"REST_READ_TIMEOUT": "fast"},
		"bad threshold":    {"LOW_STOCK_THRESHOLD": "-1"},
		"bad tz":           {"STORE_TIMEZONE": "Mars/Base"},
		"http upstream":    {"TRANSACTION_SERVICE_BASE_URL": "http://ts.example"},
		"http token url":   {"OAUTH2_TOKEN_URL": "http://idp.example/token"},
		"email no from":    {"EMAIL_BASE_URL": "https://mail.example"},
		"validator off":    {"AUTH_TOKEN_VALIDATOR_ENABLED": "false"},
		"bad bool":         {"AUTH_TOKEN_VALIDATOR_ENABLED": "nope"},
		"bad database url": {"DATABASE_URL": "::::"},
	}
	for name, override := range cases {
		env := baseEnv()
		for k, v := range override {
			env[k] = v
		}
		if _, err := loadConfig(envFunc(env)); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
}

func TestValidatorOffAllowedOnlyWithLoopbackUpstreams(t *testing.T) {
	env := baseEnv()
	env["AUTH_TOKEN_VALIDATOR_ENABLED"] = "false"
	env["TRANSACTION_SERVICE_BASE_URL"] = "http://localhost:8081"
	env["HR_ENTITY_BASE_URL"] = "http://127.0.0.1:8081/graphql"
	env["EMAIL_BASE_URL"] = "http://localhost:8081"
	env["EMAIL_FROM_ADDRESS"] = "noreply@example.com"
	env["OAUTH2_TOKEN_URL"] = "http://localhost:8081/token"
	cfg, err := loadConfig(envFunc(env))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Auth.ValidatorEnabled {
		t.Fatal("validator still on")
	}
}

func TestDatabaseURLWins(t *testing.T) {
	env := baseEnv()
	delete(env, "DB_HOST")
	env["DATABASE_URL"] = "app:pw@tcp(127.0.0.1:3306)/swag_dev"
	env["DB_TLS"] = "false"
	cfg, err := loadConfig(envFunc(env))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cfg.DatabaseDSN, "127.0.0.1:3306") || !strings.Contains(cfg.DatabaseDSN, "parseTime=true") ||
		strings.Contains(cfg.DatabaseDSN, "tls=") {
		t.Fatalf("DSN = %s", cfg.DatabaseDSN)
	}
}

func TestSplitComma(t *testing.T) {
	if got := strings.Join(splitComma(" a, ,b ,, c"), "|"); got != "a|b|c" {
		t.Fatalf("splitComma = %q", got)
	}
	if splitComma("") != nil {
		t.Fatal("empty must be nil")
	}
}

func TestLoadDotEnvNeverOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := "# comment\nSWAG_TEST_A=from-file\nSWAG_TEST_B = \"quoted\"\nexport SWAG_TEST_C=c\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWAG_TEST_A", "from-env")
	os.Unsetenv("SWAG_TEST_B")
	os.Unsetenv("SWAG_TEST_C")
	defer os.Unsetenv("SWAG_TEST_B")
	defer os.Unsetenv("SWAG_TEST_C")

	if err := loadDotEnv(path); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("SWAG_TEST_A") != "from-env" || os.Getenv("SWAG_TEST_B") != "quoted" || os.Getenv("SWAG_TEST_C") != "c" {
		t.Fatalf("A=%q B=%q C=%q", os.Getenv("SWAG_TEST_A"), os.Getenv("SWAG_TEST_B"), os.Getenv("SWAG_TEST_C"))
	}
	if err := loadDotEnv(filepath.Join(dir, "missing.env")); err != nil {
		t.Fatalf("missing file must be fine: %v", err)
	}
}
