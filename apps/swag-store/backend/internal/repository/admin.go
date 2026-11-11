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

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/catalog"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/store"
)

// OrderFilter is the body of POST /admin/orders/search.
type OrderFilter struct {
	Status      string
	OfficeID    string
	SearchQuery string // order number or email
	From        *time.Time
	To          *time.Time
	Limit       int
	Offset      int
}

// SearchOrders lists orders for Fulfilment Ops, newest first, and the total.
func (s *Store) SearchOrders(ctx context.Context, f OrderFilter) ([]Order, int, error) {
	where := []string{"1 = 1"}
	var args []any
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	if f.OfficeID != "" {
		where = append(where, "pickup_office_id = ?")
		args = append(args, f.OfficeID)
	}
	if q := strings.TrimSpace(f.SearchQuery); q != "" {
		where = append(where, "(order_no = ? OR user_email LIKE ?)")
		args = append(args, strings.ToUpper(q), likePattern(strings.ToLower(q)))
	}
	if f.From != nil {
		where = append(where, "placed_at >= ?")
		args = append(args, *f.From)
	}
	if f.To != nil {
		where = append(where, "placed_at < ?")
		args = append(args, *f.To)
	}
	cond := strings.Join(where, " AND ")
	var total int
	// #nosec G202 -- fixed fragments; every value is a parameter.
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM swag_orders WHERE "+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	// #nosec G202 -- fixed fragments; every value is a parameter.
	rows, err := s.db.QueryContext(ctx, `SELECT id, order_no, user_email, user_id, user_name, status, pickup_office_id,
    office_name, wallet_address, total_coins, payment_reference, idempotency_key, failure_code, attention_reason,
    payment_checks, placed_at, confirmed_at, failed_at, ready_at, collected_at, collected_by_email, reminder_sent_at,
    updated_at FROM swag_orders WHERE `+cond+` ORDER BY placed_at DESC, id DESC LIMIT ? OFFSET ?`,
		append(args, f.Limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Order
	for rows.Next() {
		var o store.Order
		if err := rows.Scan(&o.ID, &o.OrderNo, &o.UserEmail, &o.UserID, &o.UserName, &o.Status, &o.PickupOfficeID,
			&o.OfficeName, &o.WalletAddress, &o.TotalCoins, &o.PaymentReference, &o.IdempotencyKey, &o.FailureCode,
			&o.AttentionReason, &o.PaymentChecks, &o.PlacedAt, &o.ConfirmedAt, &o.FailedAt, &o.ReadyAt, &o.CollectedAt,
			&o.CollectedByEmail, &o.ReminderSentAt, &o.UpdatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, orderFrom(o))
	}
	return out, total, rows.Err()
}

// Dashboard holds the admin counts. Catalog counts are filled only for a
// Catalog Manager and order counts only for Fulfilment Ops.
type Dashboard struct {
	Catalog *CatalogCounts `json:"catalog,omitempty"`
	Orders  *OrderCounts   `json:"orders,omitempty"`
}

// CatalogCounts are the Catalog Manager's numbers.
type CatalogCounts struct {
	Active        int `json:"active"`
	Draft         int `json:"draft"`
	Removed       int `json:"removed"`
	LowStock      int `json:"lowStockVariants"`
	OutOfStock    int `json:"outOfStockVariants"`
	PendingAlerts int `json:"pendingAlerts"`
}

// OrderCounts are Fulfilment Ops' numbers and the orders that need a person.
type OrderCounts struct {
	PendingPayment int     `json:"pendingPayment"`
	ToPrepare      int     `json:"toPrepare"`
	ReadyForPickup int     `json:"readyForPickup"`
	CollectedToday int     `json:"collectedToday"`
	NeedsAttention []Order `json:"-"`
}

// Dashboard reads the counts the caller's roles may see.
func (s *Store) Dashboard(ctx context.Context, catalogRole, opsRole bool, lowStock int, dayStart time.Time) (*Dashboard, error) {
	d := &Dashboard{}
	if catalogRole {
		c := &CatalogCounts{}
		err := s.db.QueryRowContext(ctx, `SELECT
    CAST(COALESCE(SUM(status = 'ACTIVE'), 0) AS SIGNED), CAST(COALESCE(SUM(status = 'DRAFT'), 0) AS SIGNED),
    CAST(COALESCE(SUM(status = 'REMOVED'), 0) AS SIGNED) FROM swag_products`).Scan(&c.Active, &c.Draft, &c.Removed)
		if err != nil {
			return nil, err
		}
		err = s.db.QueryRowContext(ctx, `SELECT
    CAST(COALESCE(SUM(v.stock_on_hand - v.stock_reserved BETWEEN 1 AND ?), 0) AS SIGNED),
    CAST(COALESCE(SUM(v.stock_on_hand - v.stock_reserved <= 0), 0) AS SIGNED)
FROM swag_variants v JOIN swag_products p ON p.id = v.product_id WHERE p.status = 'ACTIVE' AND v.active = TRUE`,
			lowStock).Scan(&c.LowStock, &c.OutOfStock)
		if err != nil {
			return nil, err
		}
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM swag_stock_alerts WHERE status = 'PENDING'`).Scan(&c.PendingAlerts); err != nil {
			return nil, err
		}
		d.Catalog = c
	}
	if opsRole {
		o := &OrderCounts{}
		err := s.db.QueryRowContext(ctx, `SELECT
    CAST(COALESCE(SUM(status = 'PENDING_PAYMENT'), 0) AS SIGNED), CAST(COALESCE(SUM(status = 'CONFIRMED'), 0) AS SIGNED),
    CAST(COALESCE(SUM(status = 'READY_FOR_PICKUP'), 0) AS SIGNED),
    CAST(COALESCE(SUM(status = 'COLLECTED' AND collected_at >= ?), 0) AS SIGNED)
FROM swag_orders`, dayStart).Scan(&o.PendingPayment, &o.ToPrepare, &o.ReadyForPickup, &o.CollectedToday)
		if err != nil {
			return nil, err
		}
		attention, _, err := s.searchAttention(ctx)
		if err != nil {
			return nil, err
		}
		o.NeedsAttention = attention
		d.Orders = o
	}
	return d, nil
}

func (s *Store) searchAttention(ctx context.Context) ([]Order, int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT order_no FROM swag_orders WHERE attention_reason IS NOT NULL
ORDER BY placed_at DESC LIMIT 50`)
	if err != nil {
		return nil, 0, err
	}
	var nos []string
	for rows.Next() {
		var no string
		if err := rows.Scan(&no); err != nil {
			_ = rows.Close()
			return nil, 0, err
		}
		nos = append(nos, no)
	}
	_ = rows.Close()
	out := make([]Order, 0, len(nos))
	for _, no := range nos {
		o, err := s.OrderByNo(ctx, no)
		if err != nil {
			return nil, 0, err
		}
		if o != nil {
			out = append(out, *o)
		}
	}
	return out, len(out), nil
}

// AuditEvent is one admin action row.
type AuditEvent struct {
	ID         int64           `json:"id"`
	ActorEmail string          `json:"actorEmail"`
	Action     string          `json:"action"`
	EntityType string          `json:"entityType"`
	EntityID   string          `json:"entityId"`
	Detail     json.RawMessage `json:"detail"`
	CreatedAt  time.Time       `json:"createdAt"`
}

// AuditEvents lists admin actions, newest first.
func (s *Store) AuditEvents(ctx context.Context, limit, offset int) ([]AuditEvent, int, error) {
	rows, err := s.q.ListAdminActions(ctx, store.ListAdminActionsParams{Limit: int32(limit), Offset: int32(offset)}) // #nosec G115
	if err != nil {
		return nil, 0, err
	}
	total, err := s.q.CountAdminActions(ctx)
	if err != nil {
		return nil, 0, err
	}
	out := make([]AuditEvent, 0, len(rows))
	for _, r := range rows {
		detail := r.DetailJson
		if len(detail) == 0 {
			detail = json.RawMessage("null")
		}
		out = append(out, AuditEvent{ID: r.ID, ActorEmail: r.ActorEmail, Action: r.Action, EntityType: r.EntityType,
			EntityID: r.EntityID, Detail: detail, CreatedAt: r.CreatedAt})
	}
	return out, int(total), nil
}

// DemandRow is the demand board: pending alerts per variant.
type DemandRow struct {
	ProductID     string     `json:"productId"`
	ProductName   string     `json:"productName"`
	VariantID     string     `json:"variantId"`
	SKU           string     `json:"sku"`
	Options       string     `json:"options"`
	PendingAlerts int        `json:"pendingAlerts"`
	FirstAlertAt  time.Time  `json:"firstAlertAt"`
	RestockETA    *time.Time `json:"restockEta"`
	Free          int        `json:"free"`
}

// Demand lists variants with pending alerts, most wanted first.
func (s *Store) Demand(ctx context.Context) ([]DemandRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT p.id, p.name, v.id, v.sku, COUNT(*) AS n, MIN(a.created_at), v.restock_eta,
    v.stock_on_hand - v.stock_reserved
FROM swag_stock_alerts a JOIN swag_variants v ON v.id = a.variant_id JOIN swag_products p ON p.id = v.product_id
WHERE a.status = 'PENDING'
GROUP BY p.id, p.name, v.id, v.sku, v.restock_eta, v.stock_on_hand, v.stock_reserved
ORDER BY n DESC, MIN(a.created_at) LIMIT 500`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DemandRow{}
	var ids []string
	for rows.Next() {
		var d DemandRow
		var eta sql.NullTime
		if err := rows.Scan(&d.ProductID, &d.ProductName, &d.VariantID, &d.SKU, &d.PendingAlerts, &d.FirstAlertAt, &eta, &d.Free); err != nil {
			return nil, err
		}
		d.RestockETA = timePtr(eta)
		out = append(out, d)
		ids = append(ids, d.VariantID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	labels, err := s.optionLabels(ctx, s.q, ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Options = labels[out[i].VariantID]
	}
	return out, nil
}

// Alert is one of the caller's stock alerts.
type Alert struct {
	ID          string     `json:"id"`
	VariantID   string     `json:"variantId"`
	SKU         string     `json:"sku"`
	ProductSlug string     `json:"productSlug"`
	ProductName string     `json:"productName"`
	Options     string     `json:"options"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"createdAt"`
	NotifiedAt  *time.Time `json:"notifiedAt"`
	ClosedAt    *time.Time `json:"closedAt"`
}

// Alerts lists the caller's alerts, newest first.
func (s *Store) Alerts(ctx context.Context, email string) ([]Alert, error) {
	rows, err := s.q.ListAlertsForUser(ctx, email)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.VariantID)
	}
	labels, err := s.optionLabels(ctx, s.q, ids)
	if err != nil {
		return nil, err
	}
	out := make([]Alert, 0, len(rows))
	for _, r := range rows {
		out = append(out, Alert{ID: r.ID, VariantID: r.VariantID, SKU: r.Sku, ProductSlug: r.Slug, ProductName: r.ProductName,
			Options: labels[r.VariantID], Status: string(r.Status), CreatedAt: r.CreatedAt,
			NotifiedAt: timePtr(r.NotifiedAt), ClosedAt: timePtr(r.ClosedAt)})
	}
	return out, nil
}

// CreateAlert adds a pending alert. It is allowed only on an out-of-stock
// variant of an ACTIVE, RESTOCKABLE product (backend-spec §8.6); anything else
// is ErrNotAvailable. An existing pending alert is returned as it is.
func (s *Store) CreateAlert(ctx context.Context, email, variantID string) (string, error) {
	var id string
	err := s.inTx(ctx, func(q *store.Queries) error {
		v, err := q.GetVariant(ctx, variantID)
		if err != nil {
			return notFound(err)
		}
		p, err := q.GetProduct(ctx, v.ProductID)
		if err != nil {
			return err
		}
		if string(p.Status) != catalog.StatusActive || !v.Active || string(p.RestockPolicy) != string(catalog.Restockable) ||
			v.StockOnHand-v.StockReserved > 0 {
			return ErrNotAvailable
		}
		if a, err := q.GetPendingAlert(ctx, store.GetPendingAlertParams{UserEmail: email, VariantID: variantID}); err == nil {
			id = a.ID
			return nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		id = newID()
		err = q.InsertStockAlert(ctx, store.InsertStockAlertParams{ID: id, UserEmail: email, VariantID: variantID})
		if isDuplicate(err) {
			a, err := q.GetPendingAlert(ctx, store.GetPendingAlertParams{UserEmail: email, VariantID: variantID})
			id = a.ID
			return err
		}
		return err
	})
	return id, err
}

// CancelAlert closes one of the caller's pending alerts.
func (s *Store) CancelAlert(ctx context.Context, email, id string) error {
	n, err := s.q.CloseAlertForUser(ctx, store.CloseAlertForUserParams{ID: id, UserEmail: email})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
