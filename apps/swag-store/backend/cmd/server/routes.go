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
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/handler"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/middleware"
)

// rateLimit is a per-user token bucket shared by every route that names it.
type rateLimit struct {
	name      string
	perMinute int
}

var (
	limitOrders = &rateLimit{"orders", 5}
	limitCart   = &rateLimit{"cart", 30}
	limitAlerts = &rateLimit{"alerts", 30}
	limitAdmin  = &rateLimit{"admin", 60} // every admin write, both roles
)

// routeDef is one row of the route table. Perm is required; there is no default.
type routeDef struct {
	Method  string
	Path    string
	Perm    handler.Permission
	Limit   *rateLimit
	Handler gin.HandlerFunc
}

// app holds every handler. routeTable only takes method values, so a zero
// app is enough for the route tests.
type app struct {
	health       *handler.HealthHandler
	users        *handler.UsersHandler
	wallets      *handler.WalletsHandler
	catalog      *handler.CatalogHandler
	cart         *handler.CartHandler
	offices      *handler.OfficesHandler
	orders       *handler.OrdersHandler
	alerts       *handler.AlertsHandler
	admin        *handler.AdminHandler
	adminCatalog *handler.AdminCatalogHandler
	adminOrders  *handler.AdminOrdersHandler
}

const (
	permAuthn   = handler.PermAuthenticated
	permView    = handler.PermViewStoreAdmin
	permCatalog = handler.PermManageCatalog
	permFulfil  = handler.PermFulfilOrders
)

// routeTable is the whole API except /health and /health/dependencies, which
// skip auth. Gin params use the same name as their neighbours in a segment.
func routeTable(a *app) []routeDef {
	const (
		get, post, put, patch, del = http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete
	)
	return []routeDef{
		// Identity and wallets.
		{get, "/users/me", permAuthn, nil, a.users.Me},
		{get, "/wallets", permAuthn, nil, a.wallets.List},

		// Shop.
		{get, "/categories", permAuthn, nil, a.catalog.Categories},
		{get, "/products", permAuthn, nil, a.catalog.Products},
		{get, "/products/:slug", permAuthn, nil, a.catalog.Product},
		{get, "/cart", permAuthn, nil, a.cart.Get},
		{get, "/cart/summary", permAuthn, nil, a.cart.Summary},
		{post, "/cart/lines", permAuthn, limitCart, a.cart.AddLine},
		{patch, "/cart/lines/:id", permAuthn, limitCart, a.cart.UpdateLine},
		{del, "/cart/lines/:id", permAuthn, limitCart, a.cart.DeleteLine},
		{get, "/pickup-offices", permAuthn, nil, a.offices.List},
		{post, "/quotes", permAuthn, nil, a.orders.Quote},
		{post, "/orders", permAuthn, limitOrders, a.orders.Place},
		{get, "/orders", permAuthn, nil, a.orders.List},
		{get, "/orders/:orderNo", permAuthn, nil, a.orders.Get},
		{post, "/orders/:orderNo/payments", permAuthn, limitOrders, a.orders.RetryPayment},
		{get, "/stock-alerts", permAuthn, nil, a.alerts.List},
		{post, "/stock-alerts", permAuthn, limitAlerts, a.alerts.Create},
		{del, "/stock-alerts/:id", permAuthn, limitAlerts, a.alerts.Delete},

		// Admin: either role.
		{get, "/admin/dashboard", permView, nil, a.admin.Dashboard},
		{get, "/admin/audit-events", permView, nil, a.admin.AuditEvents},

		// Admin: Catalog Manager.
		{get, "/admin/products", permCatalog, nil, a.adminCatalog.List},
		{post, "/admin/products", permCatalog, limitAdmin, a.adminCatalog.Create},
		{get, "/admin/products/:id", permCatalog, nil, a.adminCatalog.Get},
		{patch, "/admin/products/:id", permCatalog, limitAdmin, a.adminCatalog.Patch},
		{del, "/admin/products/:id", permCatalog, limitAdmin, a.adminCatalog.Delete},
		{get, "/admin/products/:id/removal-impact", permCatalog, nil, a.adminCatalog.RemovalImpact},
		{put, "/admin/products/:id/options", permCatalog, limitAdmin, a.adminCatalog.SetOptions},
		{patch, "/admin/products/:id/variants/:variantId", permCatalog, limitAdmin, a.adminCatalog.PatchVariant},
		{post, "/admin/variants/:id/stock-movements", permCatalog, limitAdmin, a.adminCatalog.StockMovement},
		{get, "/admin/categories", permCatalog, nil, a.adminCatalog.Categories},
		{post, "/admin/categories", permCatalog, limitAdmin, a.adminCatalog.CreateCategory},
		{get, "/admin/categories/:id", permCatalog, nil, a.adminCatalog.GetCategory},
		{patch, "/admin/categories/:id", permCatalog, limitAdmin, a.adminCatalog.PatchCategory},
		{del, "/admin/categories/:id", permCatalog, limitAdmin, a.adminCatalog.DeleteCategory},
		{get, "/admin/demand", permCatalog, nil, a.adminCatalog.Demand},

		// Admin: Fulfilment Ops.
		{post, "/admin/orders/search", permFulfil, nil, a.adminOrders.Search},
		{get, "/admin/orders/:orderNo", permFulfil, nil, a.adminOrders.Get},
		{patch, "/admin/orders/:orderNo", permFulfil, limitAdmin, a.adminOrders.Patch},
		{post, "/admin/orders/:orderNo/notes", permFulfil, limitAdmin, a.adminOrders.AddNote},
		{get, "/admin/pickup-offices", permFulfil, nil, a.offices.AdminList},
		{post, "/admin/pickup-offices", permFulfil, limitAdmin, a.offices.Create},
		{patch, "/admin/pickup-offices/:id", permFulfil, limitAdmin, a.offices.Update},
	}
}

type routerDeps struct {
	auth        gin.HandlerFunc
	guard       *handler.AccessGuard
	limiter     *middleware.Limiter
	corsOrigins []string
	app         *app
}

// newRouter builds the Gin engine with the middleware chain, outermost first.
func newRouter(d routerDeps) *gin.Engine {
	r := gin.New()
	r.HandleMethodNotAllowed = true
	r.ContextWithFallback = true
	_ = r.SetTrustedProxies(nil)
	r.Use(
		middleware.SecurityHeaders(),
		middleware.CORS(d.corsOrigins),
		middleware.CorrelationID(),
		d.auth,
		middleware.Logger(),
		middleware.Recovery(),
	)
	r.NoRoute(handler.NoRoute)
	r.NoMethod(handler.NoMethod)
	r.GET("/health", d.app.health.Live)
	r.GET("/health/dependencies", d.app.health.Dependencies)
	mount(r, d.guard, d.limiter, routeTable(d.app))
	return r
}

// mount registers each route behind its permission guard and rate limit.
func mount(r gin.IRoutes, guard *handler.AccessGuard, limiter *middleware.Limiter, table []routeDef) {
	for _, rt := range table {
		chain := []gin.HandlerFunc{guard.Require(rt.Perm)}
		if rt.Limit != nil {
			chain = append(chain, limiter.Limit(rt.Limit.name, rt.Limit.perMinute))
		}
		chain = append(chain, rt.Handler)
		r.Handle(rt.Method, rt.Path, chain...)
	}
}
