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
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"gopkg.in/yaml.v3"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/handler"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/middleware"
)

func init() { gin.SetMode(gin.TestMode) }

const (
	authn   = handler.PermAuthenticated
	view    = handler.PermViewStoreAdmin
	catalog = handler.PermManageCatalog
	fulfil  = handler.PermFulfilOrders
)

// expectedPerms is backend-spec §6, written out by hand. A route added to the
// table without a row here, or with another permission, fails the test.
var expectedPerms = map[string]handler.Permission{
	"GET /users/me":                   authn,
	"GET /wallets":                    authn,
	"GET /categories":                 authn,
	"GET /products":                   authn,
	"GET /products/:slug":             authn,
	"GET /cart":                       authn,
	"GET /cart/summary":               authn,
	"POST /cart/lines":                authn,
	"PATCH /cart/lines/:id":           authn,
	"DELETE /cart/lines/:id":          authn,
	"GET /pickup-offices":             authn,
	"POST /quotes":                    authn,
	"POST /orders":                    authn,
	"GET /orders":                     authn,
	"GET /orders/:orderNo":            authn,
	"POST /orders/:orderNo/payments":  authn,
	"GET /stock-alerts":               authn,
	"POST /stock-alerts":              authn,
	"DELETE /stock-alerts/:id":        authn,
	"GET /admin/pickup-offices":       fulfil,
	"POST /admin/pickup-offices":      fulfil,
	"PATCH /admin/pickup-offices/:id": fulfil,
}

func gin2openapi(path string) string {
	return regexp.MustCompile(`:([A-Za-z]+)`).ReplaceAllString(path, `{$1}`)
}

func TestRoutePermissionsMatchSpec(t *testing.T) {
	got := map[string]handler.Permission{}
	for _, r := range routeTable(&app{}) {
		key := r.Method + " " + r.Path
		if _, dup := got[key]; dup {
			t.Errorf("route %s registered twice", key)
		}
		got[key] = r.Perm
		if want, ok := expectedPerms[key]; !ok {
			t.Errorf("route %s has no row in expectedPerms (backend-spec §6)", key)
		} else if want != r.Perm {
			t.Errorf("route %s requires %v, spec says %v", key, r.Perm, want)
		}
	}
	for key := range expectedPerms {
		if _, ok := got[key]; !ok {
			t.Errorf("spec route %s is not registered", key)
		}
	}
}

func TestRoutesBuild(t *testing.T) {
	defer func() {
		if v := recover(); v != nil {
			t.Fatalf("route table panics in Gin: %v", v)
		}
	}()
	newRouter(routerDeps{auth: func(c *gin.Context) {}, guard: handler.NewAccessGuard(handler.AccessConfig{}),
		limiter: middleware.NewLimiter(), app: &app{health: handler.NewHealthHandler(nil)}})
}

type openapiDoc struct {
	Paths map[string]map[string]struct {
		Responses map[string]any `yaml:"responses"`
	} `yaml:"paths"`
}

func loadOpenAPI(t *testing.T) openapiDoc {
	t.Helper()
	raw, err := os.ReadFile("../../openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc openapiDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestEveryRegisteredRouteIsInOpenAPI(t *testing.T) {
	doc := loadOpenAPI(t)
	registered := map[string]bool{"GET /health": true, "GET /health/dependencies": true}
	for _, r := range routeTable(&app{}) {
		registered[r.Method+" "+gin2openapi(r.Path)] = true
	}
	for key := range registered {
		method, path, _ := strings.Cut(key, " ")
		op, ok := doc.Paths[path][strings.ToLower(method)]
		if !ok {
			t.Errorf("%s has no openapi.yaml operation", key)
			continue
		}
		if !strings.HasPrefix(path, "/health") {
			if _, ok := op.Responses["403"]; !ok {
				t.Errorf("%s does not declare 403", key)
			}
		}
	}
	for path, ops := range doc.Paths {
		for method := range ops {
			if method == "parameters" {
				continue
			}
			if key := strings.ToUpper(method) + " " + path; !registered[key] {
				t.Errorf("openapi.yaml documents %s, which is not registered", key)
			}
		}
	}
}

// --- permission matrix through the real auth middleware and guard ---

var matrixKey, _ = rsa.GenerateKey(rand.Reader, 2048)

func matrixToken(t *testing.T, email string, groups ...string) string {
	t.Helper()
	s, err := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": "https://issuer.example", "aud": "swag-store", "exp": time.Now().Add(time.Hour).Unix(),
		"email": email, "userid": "u-" + email, "groups": groups,
	}).SignedString(matrixKey)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRoutePermissionMatrix(t *testing.T) {
	auth := middleware.Auth(middleware.AuthConfig{Issuer: "https://issuer.example", Audiences: []string{"swag-store"},
		ClockSkew: 30 * time.Second, ValidatorEnabled: true},
		func(*jwt.Token) (any, error) { return &matrixKey.PublicKey, nil })
	guard := handler.NewAccessGuard(handler.AccessConfig{CatalogManagerGroups: []string{"g-catalog"},
		FulfilmentOpsGroups: []string{"g-ops"}, AllowedEmailDomains: []string{"wso2.com"}})

	// Every handler is replaced with a stub: this test is about the guard.
	table := routeTable(&app{})
	for i := range table {
		table[i].Handler = func(c *gin.Context) { c.Status(http.StatusTeapot) }
	}
	r := gin.New()
	r.Use(auth)
	mount(r, guard, middleware.NewLimiter(), table)

	tokens := map[string]string{
		"none":     "",
		"outsider": matrixToken(t, "x@partner.com", "g-catalog", "g-ops"),
		"shopper":  matrixToken(t, "you@wso2.com"),
		"catalog":  matrixToken(t, "cm@wso2.com", "g-catalog"),
		"ops":      matrixToken(t, "ops@wso2.com", "g-ops"),
	}
	allowed := func(perm handler.Permission, who string) bool {
		switch who {
		case "shopper":
			return perm == authn
		case "catalog":
			return perm == authn || perm == view || perm == catalog
		case "ops":
			return perm == authn || perm == view || perm == fulfil
		}
		return false
	}
	for _, rt := range table {
		path := regexp.MustCompile(`:[A-Za-z]+`).ReplaceAllString(rt.Path, "0192f3a4-5b6c-7d8e-9f01-23456789abcd")
		for who, tok := range tokens {
			req := httptest.NewRequest(rt.Method, path, strings.NewReader("{}"))
			if tok != "" {
				req.Header.Set("x-jwt-assertion", tok)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			var want int
			switch {
			case who == "none":
				want = http.StatusUnauthorized
			case who == "outsider":
				want = http.StatusForbidden
			case allowed(rt.Perm, who):
				want = http.StatusTeapot
			default:
				want = http.StatusForbidden
			}
			if rec.Code != want {
				t.Errorf("%s %s as %s: %d, want %d", rt.Method, rt.Path, who, rec.Code, want)
			}
		}
	}
}
