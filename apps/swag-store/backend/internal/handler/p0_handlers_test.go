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

package handler

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/coins"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/hr"
)

func TestHealthLive(t *testing.T) {
	h := NewHealthHandler(nil)
	rec := do(testRouter(nil, http.MethodGet, "/health", h.Live), http.MethodGet, "/health", "")
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("live = %d %q", rec.Code, rec.Body.String())
	}
}

func TestHealthDependenciesCachedFor10s(t *testing.T) {
	var calls atomic.Int32
	h := NewHealthHandler([]Probe{
		{Name: "database", Check: func(context.Context) string { calls.Add(1); return StatusOK }},
		{Name: "email", Check: func(context.Context) string { return StatusNotConfigured }},
	})
	now := time.Unix(0, 0)
	h.now = func() time.Time { return now }
	r := testRouter(nil, http.MethodGet, "/health/dependencies", h.Dependencies)

	rec := do(r, http.MethodGet, "/health/dependencies", "")
	body := decode[map[string]string](t, rec)
	if rec.Code != 200 || body["database"] != "ok" || body["email"] != "not_configured" {
		t.Fatalf("deps = %d %v", rec.Code, body)
	}
	do(r, http.MethodGet, "/health/dependencies", "")
	if calls.Load() != 1 {
		t.Fatalf("not cached: %d probe calls", calls.Load())
	}
	now = now.Add(11 * time.Second)
	do(r, http.MethodGet, "/health/dependencies", "")
	if calls.Load() != 2 {
		t.Fatalf("cache never expires: %d", calls.Load())
	}
}

func TestHealthDependenciesFailureIs503AndProbesTimeOut(t *testing.T) {
	old := probeTimeout
	probeTimeout = 50 * time.Millisecond
	defer func() { probeTimeout = old }()
	h := NewHealthHandler([]Probe{
		{Name: "treasury", Check: func(context.Context) string { return "mismatch" }},
		{Name: "hrEntity", Check: func(ctx context.Context) string { <-ctx.Done(); return StatusError }},
	})
	start := time.Now()
	rec := do(testRouter(nil, http.MethodGet, "/x", h.Dependencies), http.MethodGet, "/x", "")
	if rec.Code != http.StatusServiceUnavailable || time.Since(start) > time.Second {
		t.Fatalf("deps = %d after %v", rec.Code, time.Since(start))
	}
	body := decode[map[string]string](t, rec)
	if body["treasury"] != "mismatch" || body["hrEntity"] != "error" {
		t.Fatalf("body = %v", body)
	}
}

func TestUsersMe(t *testing.T) {
	g := NewAccessGuard(testAccess())
	emp := &fakeEmployees{emp: &hr.Employee{EmployeeStatus: "Active", FirstName: "HR", LastName: "Person"}}
	u := *shopper
	u.Groups = []string{"swag-catalog"}
	h := NewUsersHandler(g, emp)
	rec := do(testRouter(&u, http.MethodGet, "/users/me", h.Me), http.MethodGet, "/users/me", "")
	body := decode[map[string]any](t, rec)
	if rec.Code != 200 || body["email"] != "you@wso2.com" || body["firstName"] != "Your" ||
		body["employeeStatus"] != "Active" || body["roles"].([]any)[0] != "catalog_manager" {
		t.Fatalf("me = %d %v", rec.Code, body)
	}
}

func TestUsersMeWhenHRDown(t *testing.T) {
	g := NewAccessGuard(testAccess())
	h := NewUsersHandler(g, &fakeEmployees{err: errDown})
	rec := do(testRouter(shopper, http.MethodGet, "/users/me", h.Me), http.MethodGet, "/users/me", "")
	body := decode[map[string]any](t, rec)
	if rec.Code != 200 || body["employeeStatus"] != nil || len(body["roles"].([]any)) != 0 {
		t.Fatalf("me = %d %v", rec.Code, body)
	}
}

func TestWalletsList(t *testing.T) {
	h := NewWalletsHandler(&fakeWallets{wallets: []coins.Wallet{
		{Address: "0xa", Balance: rat("202.5"), BalanceText: "202.50", Default: true},
		{Address: "0xb", Balance: rat("1"), BalanceText: "1", Default: false},
	}})
	rec := do(testRouter(shopper, http.MethodGet, "/wallets", h.List), http.MethodGet, "/wallets", "")
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("wallets = %d %v", rec.Code, rec.Header())
	}
	body := decode[[]map[string]any](t, rec)
	if len(body) != 2 || body[0]["walletAddress"] != "0xa" || body[0]["balance"] != "202.50" || body[0]["defaultWallet"] != true {
		t.Fatalf("body = %v", body)
	}
}

func TestWalletsDown(t *testing.T) {
	h := NewWalletsHandler(&fakeWallets{err: errDown})
	rec := do(testRouter(shopper, http.MethodGet, "/wallets", h.List), http.MethodGet, "/wallets", "")
	if rec.Code != http.StatusServiceUnavailable || decode[errorBody](t, rec).ErrorCode != CodeCoinsUnavailable {
		t.Fatalf("down = %d %s", rec.Code, rec.Body)
	}
}
