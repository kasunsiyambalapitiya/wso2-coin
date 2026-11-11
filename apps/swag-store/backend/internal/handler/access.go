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

// Package handler holds the HTTP handlers, one file per resource, plus the
// access guard and the shared request and response helpers. Handlers depend on
// small local interfaces, never on concrete clients.
package handler

import (
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/reqctx"
)

// Permission is what a route requires. Every route names one; there is no default.
type Permission int

const (
	// PermAuthenticated is any valid token from an allowed email domain.
	PermAuthenticated Permission = iota + 1
	// PermViewStoreAdmin is Catalog Manager or Fulfilment Ops.
	PermViewStoreAdmin
	// PermManageCatalog is Catalog Manager.
	PermManageCatalog
	// PermFulfilOrders is Fulfilment Ops.
	PermFulfilOrders
)

func (p Permission) String() string {
	switch p {
	case PermAuthenticated:
		return "authenticated"
	case PermViewStoreAdmin:
		return "view_store_admin"
	case PermManageCatalog:
		return "manage_catalog"
	case PermFulfilOrders:
		return "fulfil_orders"
	}
	return "unknown"
}

// Portal role names, as GET /users/me reports them.
const (
	RoleCatalogManager = "catalog_manager"
	RoleFulfilmentOps  = "fulfilment_ops"
)

// AccessConfig maps token groups to store roles. An empty group list means
// nobody holds that role.
type AccessConfig struct {
	CatalogManagerGroups []string // AUTH_CATALOG_MANAGER_GROUPS
	FulfilmentOpsGroups  []string // AUTH_FULFILMENT_OPS_GROUPS
	AllowedEmailDomains  []string // AUTH_ALLOWED_EMAIL_DOMAINS
}

// AccessGuard decides what a user may do. It is the single source for both
// route checks and the roles GET /users/me reports.
type AccessGuard struct {
	catalogGroups []string
	opsGroups     []string
	domains       []string
}

// NewAccessGuard builds the guard and warns about each unset role variable.
func NewAccessGuard(cfg AccessConfig) *AccessGuard {
	var unset []string
	if len(cfg.CatalogManagerGroups) == 0 {
		unset = append(unset, "AUTH_CATALOG_MANAGER_GROUPS")
	}
	if len(cfg.FulfilmentOpsGroups) == 0 {
		unset = append(unset, "AUTH_FULFILMENT_OPS_GROUPS")
	}
	if len(unset) > 0 {
		slog.Warn("access: role group variables unset; nobody holds these roles", "vars", strings.Join(unset, ","))
	}
	domains := make([]string, 0, len(cfg.AllowedEmailDomains))
	for _, d := range cfg.AllowedEmailDomains {
		domains = append(domains, strings.ToLower(d))
	}
	return &AccessGuard{
		catalogGroups: nonEmpty(cfg.CatalogManagerGroups),
		opsGroups:     nonEmpty(cfg.FulfilmentOpsGroups),
		domains:       domains,
	}
}

func nonEmpty(in []string) []string {
	var out []string
	for _, s := range in {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func hasGroup(user, granted []string) bool {
	for _, g := range user {
		if g != "" && slices.Contains(granted, g) {
			return true
		}
	}
	return false
}

// Roles returns the user's portal roles in a fixed order.
func (g *AccessGuard) Roles(u *reqctx.User) []string {
	roles := []string{}
	if u == nil {
		return roles
	}
	if hasGroup(u.Groups, g.catalogGroups) {
		roles = append(roles, RoleCatalogManager)
	}
	if hasGroup(u.Groups, g.opsGroups) {
		roles = append(roles, RoleFulfilmentOps)
	}
	return roles
}

// Has reports whether u holds perm. It does not check the email domain.
func (g *AccessGuard) Has(u *reqctx.User, perm Permission) bool {
	if u == nil {
		return false
	}
	catalog := hasGroup(u.Groups, g.catalogGroups)
	ops := hasGroup(u.Groups, g.opsGroups)
	switch perm {
	case PermAuthenticated:
		return true
	case PermViewStoreAdmin:
		return catalog || ops
	case PermManageCatalog:
		return catalog
	case PermFulfilOrders:
		return ops
	}
	return false
}

func (g *AccessGuard) allowedDomain(email string) bool {
	at := strings.LastIndexByte(email, '@')
	if at < 0 {
		return false
	}
	return slices.Contains(g.domains, strings.ToLower(email[at+1:]))
}

// Require returns the route's guard: 401 with no user, 403 not_wso2 outside the
// allowed domains, 403 when no group grants perm.
func (g *AccessGuard) Require(perm Permission) gin.HandlerFunc {
	return func(c *gin.Context) {
		u := reqctx.UserFrom(c.Request.Context())
		if u == nil {
			abortError(c, http.StatusUnauthorized, ErrMsgUnauthorized, "")
			return
		}
		if !g.allowedDomain(u.Email) {
			slog.WarnContext(c.Request.Context(), "access denied: email domain not allowed",
				"method", c.Request.Method, "path", c.Request.URL.Path)
			abortError(c, http.StatusForbidden, ErrMsgNotWSO2, CodeNotWSO2)
			return
		}
		if !g.Has(u, perm) {
			slog.WarnContext(c.Request.Context(), "access denied: token carries no role granting this permission",
				"permission", perm.String(), "method", c.Request.Method, "path", c.Request.URL.Path)
			abortError(c, http.StatusForbidden, ErrMsgForbidden, "")
			return
		}
		c.Next()
	}
}
