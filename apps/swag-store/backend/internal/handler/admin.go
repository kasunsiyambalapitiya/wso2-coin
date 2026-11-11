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
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/repository"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/reqctx"
)

// AdminStore reads the dashboard and the audit log.
type AdminStore interface {
	Dashboard(ctx context.Context, catalogRole, opsRole bool, lowStock int, dayStart time.Time) (*repository.Dashboard, error)
	AuditEvents(ctx context.Context, limit, offset int) ([]repository.AuditEvent, int, error)
}

// AdminHandler serves the dashboard and audit events (either admin role).
type AdminHandler struct {
	guard    *AccessGuard
	store    AdminStore
	lowStock int
	tz       *time.Location
	now      func() time.Time
}

// NewAdminHandler builds the handler.
func NewAdminHandler(g *AccessGuard, s AdminStore, lowStock int, tz *time.Location) *AdminHandler {
	return &AdminHandler{guard: g, store: s, lowStock: lowStock, tz: tz, now: time.Now}
}

// Dashboard returns only the counts the caller's roles may see.
func (h *AdminHandler) Dashboard(c *gin.Context) {
	u := reqctx.UserFrom(c.Request.Context())
	now := h.now().In(h.tz)
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, h.tz)
	d, err := h.store.Dashboard(c.Request.Context(), h.guard.Has(u, PermManageCatalog), h.guard.Has(u, PermFulfilOrders), h.lowStock, dayStart)
	if err != nil {
		writeInternal(c, "admin dashboard: read failed", err)
		return
	}
	out := gin.H{}
	if d.Catalog != nil {
		out["catalog"] = d.Catalog
	}
	if d.Orders != nil {
		attention := make([]adminOrderJSON, 0, len(d.Orders.NeedsAttention))
		for _, o := range d.Orders.NeedsAttention {
			attention = append(attention, adminOrder(o))
		}
		out["orders"] = gin.H{"pendingPayment": d.Orders.PendingPayment, "toPrepare": d.Orders.ToPrepare,
			"readyForPickup": d.Orders.ReadyForPickup, "collectedToday": d.Orders.CollectedToday, "needsAttention": attention}
	}
	writeJSON(c, http.StatusOK, out)
}

// AuditEvents lists admin actions, newest first.
func (h *AdminHandler) AuditEvents(c *gin.Context) {
	limit, offset, ok := paging(c)
	if !ok {
		return
	}
	events, total, err := h.store.AuditEvents(c.Request.Context(), limit, offset)
	if err != nil {
		writeInternal(c, "admin audit: read failed", err)
		return
	}
	if events == nil {
		events = []repository.AuditEvent{}
	}
	writeJSON(c, http.StatusOK, gin.H{"items": events, "total": total})
}
