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

package middleware

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/reqctx"
)

var (
	testKey, _  = rsa.GenerateKey(rand.Reader, 2048)
	otherKey, _ = rsa.GenerateKey(rand.Reader, 2048)
)

func testAuthConfig() AuthConfig {
	return AuthConfig{
		Issuer:           "https://issuer.example",
		Audiences:        []string{"swag-store"},
		ClockSkew:        30 * time.Second,
		ValidatorEnabled: true,
	}
}

func staticKeyfunc(*jwt.Token) (any, error) { return &testKey.PublicKey, nil }

func sign(t *testing.T, key *rsa.PrivateKey, claims jwt.MapClaims) string {
	t.Helper()
	s, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func goodClaims() jwt.MapClaims {
	return jwt.MapClaims{
		"iss":         "https://issuer.example",
		"aud":         []string{"swag-store"},
		"exp":         time.Now().Add(time.Hour).Unix(),
		"email":       "You@WSO2.com",
		"userid":      "u-1",
		"given_name":  "Your",
		"family_name": "Name",
		"groups":      []string{"swag-catalog"},
	}
}

func authRouter(cfg AuthConfig) (*gin.Engine, *reqctx.User, *string) {
	r := gin.New()
	r.Use(Auth(cfg, staticKeyfunc))
	var user reqctx.User
	var assertion string
	h := func(c *gin.Context) {
		if u := reqctx.UserFrom(c.Request.Context()); u != nil {
			user = *u
		}
		assertion = reqctx.UserAssertion(c.Request.Context())
		c.Status(http.StatusOK)
	}
	r.GET("/users/me", h)
	r.GET("/health", h)
	r.GET("/health/dependencies", h)
	return r, &user, &assertion
}

func TestAuthAcceptsValidToken(t *testing.T) {
	r, user, assertion := authRouter(testAuthConfig())
	token := sign(t, testKey, goodClaims())
	req := httptest.NewRequest(http.MethodGet, "/users/me", nil)
	req.Header.Set("x-jwt-assertion", token)
	rec := serve(r, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body)
	}
	if user.Email != "you@wso2.com" || user.UserID != "u-1" || user.FirstName != "Your" || user.LastName != "Name" {
		t.Fatalf("user = %+v", *user)
	}
	if len(user.Groups) != 1 || user.Groups[0] != "swag-catalog" {
		t.Fatalf("groups = %v", user.Groups)
	}
	if *assertion != token {
		t.Fatalf("user assertion not stored for the coins client")
	}
}

func TestAuthGroupsClaimAsString(t *testing.T) {
	r, user, _ := authRouter(testAuthConfig())
	c := goodClaims()
	c["groups"] = "swag-ops"
	req := httptest.NewRequest(http.MethodGet, "/users/me", nil)
	req.Header.Set("x-jwt-assertion", sign(t, testKey, c))
	if rec := serve(r, req); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if len(user.Groups) != 1 || user.Groups[0] != "swag-ops" {
		t.Fatalf("groups = %v", user.Groups)
	}
}

func TestAuthRejects(t *testing.T) {
	without := func(key string) jwt.MapClaims { c := goodClaims(); delete(c, key); return c }
	with := func(key string, v any) jwt.MapClaims { c := goodClaims(); c[key] = v; return c }

	cases := map[string]string{
		"missing token":   "",
		"bad signature":   sign(t, otherKey, goodClaims()),
		"wrong issuer":    sign(t, testKey, with("iss", "https://other.example")),
		"wrong audience":  sign(t, testKey, with("aud", []string{"other"})),
		"expired":         sign(t, testKey, with("exp", time.Now().Add(-time.Hour).Unix())),
		"no expiry":       sign(t, testKey, without("exp")),
		"missing email":   sign(t, testKey, without("email")),
		"missing userid":  sign(t, testKey, without("userid")),
		"bad groups type": sign(t, testKey, with("groups", 7)),
		"garbage":         "not.a.token",
		"alg none":        noneToken(t),
	}
	r, _, _ := authRouter(testAuthConfig())
	var bodies []string
	for name, token := range cases {
		req := httptest.NewRequest(http.MethodGet, "/users/me", nil)
		if token != "" {
			req.Header.Set("x-jwt-assertion", token)
		}
		rec := serve(r, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", name, rec.Code)
		}
		bodies = append(bodies, rec.Body.String())
	}
	for _, b := range bodies[1:] {
		if b != bodies[0] {
			t.Fatalf("401 bodies differ, the reason leaks: %q vs %q", b, bodies[0])
		}
	}
}

func noneToken(t *testing.T) string {
	t.Helper()
	s, err := jwt.NewWithClaims(jwt.SigningMethodNone, goodClaims()).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAuthSkipsHealth(t *testing.T) {
	r, _, _ := authRouter(testAuthConfig())
	for _, p := range []string{"/health", "/health/dependencies"} {
		if rec := serve(r, httptest.NewRequest(http.MethodGet, p, nil)); rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200 without a token", p, rec.Code)
		}
	}
}

func TestAuthDecodeOnlyWhenValidatorOff(t *testing.T) {
	cfg := testAuthConfig()
	cfg.ValidatorEnabled = false
	r, user, _ := authRouter(cfg)
	req := httptest.NewRequest(http.MethodGet, "/users/me", nil)
	req.Header.Set("x-jwt-assertion", sign(t, otherKey, goodClaims()))
	if rec := serve(r, req); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if user.UserID != "u-1" {
		t.Fatalf("user = %+v", *user)
	}
}

func TestX5CStrippingTransport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"keys":[{"kty":"RSA","kid":"k1","n":"AQAB","e":"AQAB","x5c":["MIIBad"]}]}`)
	}))
	defer srv.Close()

	client := &http.Client{Transport: &x5cStrippingTransport{base: http.DefaultTransport}}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "x5c") {
		t.Fatalf("x5c not stripped: %s", body)
	}
	var doc struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal(body, &doc); err != nil || len(doc.Keys) != 1 || doc.Keys[0]["kid"] != "k1" {
		t.Fatalf("JWKS damaged: %s (%v)", body, err)
	}
}
