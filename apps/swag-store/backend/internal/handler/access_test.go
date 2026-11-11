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
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/reqctx"
)

func testAccess() AccessConfig {
	return AccessConfig{
		CatalogManagerGroups: []string{"swag-catalog"},
		FulfilmentOpsGroups:  []string{"swag-ops"},
		AllowedEmailDomains:  []string{"wso2.com"},
	}
}

func guardedRouter(g *AccessGuard, perm Permission, user *reqctx.User) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if user != nil {
			c.Request = c.Request.WithContext(reqctx.WithUser(c.Request.Context(), user))
		}
	})
	r.GET("/x", g.Require(perm), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	return r
}

func call(r *gin.Engine) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	return rec
}

func TestAccessGuard(t *testing.T) {
	g := NewAccessGuard(testAccess())
	shopper := &reqctx.User{Email: "you@wso2.com", UserID: "u-1"}
	catalog := &reqctx.User{Email: "cm@wso2.com", UserID: "u-2", Groups: []string{"swag-catalog"}}
	ops := &reqctx.User{Email: "ops@wso2.com", UserID: "u-3", Groups: []string{"swag-ops"}}
	both := &reqctx.User{Email: "both@wso2.com", UserID: "u-4", Groups: []string{"swag-ops", "swag-catalog"}}
	outsider := &reqctx.User{Email: "x@partner.com", UserID: "u-5", Groups: []string{"swag-catalog"}}

	cases := []struct {
		name string
		perm Permission
		user *reqctx.User
		want int
	}{
		{"no user", PermAuthenticated, nil, 401},
		{"shopper", PermAuthenticated, shopper, 204},
		{"shopper on admin view", PermViewStoreAdmin, shopper, 403},
		{"shopper on catalog", PermManageCatalog, shopper, 403},
		{"catalog on catalog", PermManageCatalog, catalog, 204},
		{"catalog on view", PermViewStoreAdmin, catalog, 204},
		{"catalog on fulfil", PermFulfilOrders, catalog, 403},
		{"ops on fulfil", PermFulfilOrders, ops, 204},
		{"ops on view", PermViewStoreAdmin, ops, 204},
		{"ops on catalog", PermManageCatalog, ops, 403},
		{"both on catalog", PermManageCatalog, both, 204},
		{"both on fulfil", PermFulfilOrders, both, 204},
		{"outside domain", PermAuthenticated, outsider, 403},
	}
	for _, tc := range cases {
		rec := call(guardedRouter(g, tc.perm, tc.user))
		if rec.Code != tc.want {
			t.Errorf("%s: status = %d, want %d", tc.name, rec.Code, tc.want)
		}
	}

	rec := call(guardedRouter(g, PermAuthenticated, outsider))
	if !strings.Contains(rec.Body.String(), `"errorCode":"not_wso2"`) {
		t.Fatalf("outside-domain body = %s", rec.Body)
	}
}

func TestUnsetRoleEnvMeansNobody(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	g := NewAccessGuard(AccessConfig{AllowedEmailDomains: []string{"wso2.com"}})
	if !strings.Contains(buf.String(), "AUTH_CATALOG_MANAGER_GROUPS") || !strings.Contains(buf.String(), "AUTH_FULFILMENT_OPS_GROUPS") {
		t.Fatalf("no warning naming the unset vars: %s", buf.String())
	}
	// A user with no groups, and one with an empty group, hold nothing.
	for _, groups := range [][]string{nil, {""}} {
		u := &reqctx.User{Email: "a@wso2.com", UserID: "u", Groups: groups}
		for _, p := range []Permission{PermViewStoreAdmin, PermManageCatalog, PermFulfilOrders} {
			if g.Has(u, p) {
				t.Fatalf("unset role env granted %v to groups %v", p, groups)
			}
		}
		if roles := g.Roles(u); len(roles) != 0 {
			t.Fatalf("roles = %v", roles)
		}
	}
}

func TestGroupMatchIsExact(t *testing.T) {
	g := NewAccessGuard(testAccess())
	for _, grp := range []string{"SWAG-CATALOG", "swag-catalog ", "swag"} {
		if g.Has(&reqctx.User{Email: "a@wso2.com", Groups: []string{grp}}, PermManageCatalog) {
			t.Errorf("group %q granted catalog manager", grp)
		}
	}
	roles := g.Roles(&reqctx.User{Email: "a@wso2.com", Groups: []string{"swag-ops", "swag-catalog"}})
	if strings.Join(roles, ",") != "catalog_manager,fulfilment_ops" {
		t.Fatalf("roles = %v", roles)
	}
}
