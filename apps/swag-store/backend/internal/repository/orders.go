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
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/notifications"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/orders"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/store"
)

const (
	reasonReserve = store.SwagInventoryMovementsReasonRESERVE
	reasonRelease = store.SwagInventoryMovementsReasonRELEASE
	reasonCommit  = store.SwagInventoryMovementsReasonCOMMIT
)

func orderFrom(o store.Order) Order {
	return Order{
		ID: o.ID, OrderNo: o.OrderNo, UserEmail: o.UserEmail, UserID: o.UserID, UserName: o.UserName,
		Status: orders.Status(o.Status), OfficeID: o.PickupOfficeID, OfficeName: o.OfficeName,
		WalletAddress: o.WalletAddress, Total: o.TotalCoins, PaymentReference: o.PaymentReference,
		IdempotencyKey: o.IdempotencyKey, FailureCode: o.FailureCode.String, AttentionReason: o.AttentionReason.String,
		PaymentChecks: int(o.PaymentChecks), PlacedAt: o.PlacedAt, ConfirmedAt: timePtr(o.ConfirmedAt),
		FailedAt: timePtr(o.FailedAt), ReadyAt: timePtr(o.ReadyAt), CollectedAt: timePtr(o.CollectedAt),
		CollectedBy: o.CollectedByEmail.String,
	}
}

func orPtr(o store.Order, err error) (*Order, error) {
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	v := orderFrom(o)
	return &v, nil
}

// OrderByKey returns the caller's order for an Idempotency-Key, or nil.
func (s *Store) OrderByKey(ctx context.Context, email, key string) (*Order, error) {
	return orPtr(s.q.GetOrderByKey(ctx, store.GetOrderByKeyParams{UserEmail: email, IdempotencyKey: key}))
}

// OrderByNoForUser returns one of the caller's orders, or nil.
func (s *Store) OrderByNoForUser(ctx context.Context, orderNo, email string) (*Order, error) {
	return orPtr(s.q.GetOrderByNoForUser(ctx, store.GetOrderByNoForUserParams{OrderNo: orderNo, UserEmail: email}))
}

// OrderByNo returns any order (admin), or nil.
func (s *Store) OrderByNo(ctx context.Context, orderNo string) (*Order, error) {
	return orPtr(s.q.GetOrderByNo(ctx, orderNo))
}

// OrderByID returns any order, or nil.
func (s *Store) OrderByID(ctx context.Context, id string) (*Order, error) {
	return orPtr(s.q.GetOrder(ctx, id))
}

// CreatePendingOrder is Tx1 of backend-spec §8.3: lock the caller's cart, re-run
// the quote rules (n.Check), take an order number, insert the order, its line
// snapshots and first event, and reserve the stock of every line.
func (s *Store) CreatePendingOrder(ctx context.Context, n NewOrder) (*Order, error) {
	var out *Order
	err := s.inTx(ctx, func(q *store.Queries) error {
		if _, err := q.LockCartLines(ctx, n.UserEmail); err != nil {
			return err
		}
		items, err := s.cartItems(ctx, q, n.UserEmail)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			return ErrCartEmpty
		}
		total, err := n.Check(items)
		if err != nil {
			return err
		}
		num, err := q.NextOrderNumber(ctx)
		if err != nil {
			return err
		}
		orderNo := orders.Number(num)
		// Lock order: products (shared), then variants (exclusive), each in id
		// order, before any row that has a foreign key to them is inserted.
		// Product removal takes the product first too, and a foreign-key check
		// taken before the reserve would upgrade a shared variant lock and
		// deadlock concurrent buyers of one variant.
		byVariant := append([]CartItem(nil), items...)
		sort.Slice(byVariant, func(i, j int) bool { return byVariant[i].VariantID < byVariant[j].VariantID })
		productIDs := make([]string, 0, len(items))
		for _, it := range byVariant {
			productIDs = append(productIDs, it.ProductID)
		}
		if _, err := q.LockProductsShared(ctx, productIDs); err != nil {
			return err
		}
		for _, it := range byVariant {
			rows, err := q.ReserveStock(ctx, store.ReserveStockParams{Qty: int32(it.Quantity), VariantID: it.VariantID}) // #nosec G115
			if err != nil {
				return err
			}
			if rows == 0 {
				// The single conditional UPDATE also settles a race with product removal.
				v, err := q.GetVariant(ctx, it.VariantID)
				if err == nil && v.Active {
					if p, err := q.GetProduct(ctx, v.ProductID); err == nil && string(p.Status) == "ACTIVE" {
						return fmt.Errorf("%w: %s", ErrOutOfStock, it.SKU)
					}
				}
				return fmt.Errorf("%w: %s", ErrNotAvailable, it.SKU)
			}
			if err := movement(ctx, q, it.VariantID, it.Quantity, reasonReserve, orderNo, ""); err != nil {
				return err
			}
		}
		err = q.InsertOrder(ctx, store.InsertOrderParams{
			ID: n.ID, OrderNo: orderNo, UserEmail: n.UserEmail, UserID: n.UserID, UserName: n.UserName,
			PickupOfficeID: n.OfficeID, OfficeName: n.OfficeName, WalletAddress: n.WalletAddress,
			TotalCoins: total, PaymentReference: orders.PaymentReference(n.ID), IdempotencyKey: n.IdempotencyKey,
		})
		if isDuplicate(err) {
			return ErrDuplicateKey
		}
		if err != nil {
			return err
		}
		for _, it := range items {
			if err := q.InsertOrderLine(ctx, store.InsertOrderLineParams{
				ID: newID(), OrderID: n.ID, ProductID: it.ProductID, VariantID: it.VariantID, Sku: it.SKU,
				Name: it.Name, OptionsLabel: it.Options, UnitPriceCoins: it.UnitPrice, Quantity: int32(it.Quantity), // #nosec G115
			}); err != nil {
				return err
			}
		}
		if err := q.InsertOrderEvent(ctx, store.InsertOrderEventParams{OrderID: n.ID, ToStatus: string(orders.PendingPayment)}); err != nil {
			return err
		}
		out = &Order{ID: n.ID, OrderNo: orderNo, UserEmail: n.UserEmail, UserID: n.UserID, UserName: n.UserName,
			Status: orders.PendingPayment, OfficeID: n.OfficeID, OfficeName: n.OfficeName, WalletAddress: n.WalletAddress,
			Total: total, PaymentReference: orders.PaymentReference(n.ID), IdempotencyKey: n.IdempotencyKey, PlacedAt: s.now()}
		return nil
	})
	return out, err
}

// ConfirmOrder moves PENDING_PAYMENT → CONFIRMED: commit the stock, delete the
// ordered cart lines and queue the confirmation. It returns false when the
// order was no longer pending (another path settled it).
func (s *Store) ConfirmOrder(ctx context.Context, orderID string) (bool, error) {
	done := false
	err := s.inTx(ctx, func(q *store.Queries) error {
		o, err := q.LockOrder(ctx, orderID)
		if err != nil {
			return notFound(err)
		}
		n, err := q.ConfirmFromPending(ctx, orderID)
		if err != nil || n == 0 {
			return err
		}
		lines, err := q.ListOrderLines(ctx, orderID)
		if err != nil {
			return err
		}
		if err := commitLines(ctx, q, o.OrderNo, lines); err != nil {
			return err
		}
		ids := make([]string, 0, len(lines))
		for _, l := range lines {
			ids = append(ids, l.VariantID)
		}
		if err := q.DeleteCartLinesForVariants(ctx, store.DeleteCartLinesForVariantsParams{UserEmail: o.UserEmail, VariantIds: ids}); err != nil {
			return err
		}
		if err := event(ctx, q, orderID, orders.PendingPayment, orders.Confirmed, "", ""); err != nil {
			return err
		}
		if err := enqueueOrderEmail(ctx, q, notifications.TemplateOrderConfirmed, o, lines, "confirmed:"+orderID); err != nil {
			return err
		}
		done = true
		return nil
	})
	return done, err
}

func commitLines(ctx context.Context, q *store.Queries, ref string, lines []store.OrderLine) error {
	for _, l := range lines {
		n, err := q.CommitStock(ctx, store.CommitStockParams{Qty: l.Quantity, VariantID: l.VariantID})
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("commit stock for %s: reservation missing", l.Sku)
		}
		if err := movement(ctx, q, l.VariantID, -int(l.Quantity), reasonCommit, ref, ""); err != nil {
			return err
		}
	}
	return nil
}

// FailOrder moves PENDING_PAYMENT → FAILED and releases the reservation. A
// plain refusal queues the "payment failed" email; an attention reason (such
// as payment_mismatch) writes an admin action row for manual handling.
func (s *Store) FailOrder(ctx context.Context, orderID string, f Failure) (bool, error) {
	done := false
	err := s.inTx(ctx, func(q *store.Queries) error {
		o, err := q.LockOrder(ctx, orderID)
		if err != nil {
			return notFound(err)
		}
		n, err := q.FailFromPending(ctx, store.FailFromPendingParams{FailureCode: nullStr(f.Code), AttentionReason: nullStr(f.Attention), ID: orderID})
		if err != nil || n == 0 {
			return err
		}
		lines, err := q.ListOrderLines(ctx, orderID)
		if err != nil {
			return err
		}
		for _, l := range lines {
			n, err := q.ReleaseStock(ctx, store.ReleaseStockParams{Qty: l.Quantity, VariantID: l.VariantID})
			if err != nil {
				return err
			}
			if n == 0 {
				return fmt.Errorf("release stock for %s: reservation missing", l.Sku)
			}
			if err := movement(ctx, q, l.VariantID, -int(l.Quantity), reasonRelease, o.OrderNo, ""); err != nil {
				return err
			}
		}
		if err := event(ctx, q, orderID, orders.PendingPayment, orders.Failed, f.Code, ""); err != nil {
			return err
		}
		if f.Notify {
			if err := enqueueOrderEmail(ctx, q, notifications.TemplatePaymentFailed, o, lines, "failed:"+orderID); err != nil {
				return err
			}
		}
		if f.Attention != "" {
			if err := audit(ctx, q, SystemActor, "order_flagged", "order", o.OrderNo, map[string]any{"reason": f.Attention}); err != nil {
				return err
			}
		}
		done = true
		return nil
	})
	return done, err
}

// ErrNoStockForLatePayment means a late payment arrived but the stock is gone.
var ErrNoStockForLatePayment = errors.New("late payment found but the stock is gone")

// ConfirmLatePayment is FAILED → CONFIRMED for a payment the treasury
// reconciler found late: reserve again, then confirm. When the stock is gone
// the order stays FAILED with attention late_payment_no_stock.
func (s *Store) ConfirmLatePayment(ctx context.Context, orderID string) error {
	err := s.inTx(ctx, func(q *store.Queries) error {
		o, err := q.LockOrder(ctx, orderID)
		if err != nil {
			return notFound(err)
		}
		if orders.Status(o.Status) != orders.Failed {
			return ErrStateConflict
		}
		lines, err := q.ListOrderLines(ctx, orderID)
		if err != nil {
			return err
		}
		for _, l := range lines {
			n, err := q.ReserveStockLate(ctx, store.ReserveStockLateParams{Qty: l.Quantity, VariantID: l.VariantID})
			if err != nil {
				return err
			}
			if n == 0 {
				return ErrNoStockForLatePayment
			}
			if err := movement(ctx, q, l.VariantID, int(l.Quantity), reasonReserve, o.OrderNo, ""); err != nil {
				return err
			}
		}
		if n, err := q.ConfirmFromFailed(ctx, orderID); err != nil || n == 0 {
			if err == nil {
				err = ErrStateConflict
			}
			return err
		}
		if err := commitLines(ctx, q, o.OrderNo, lines); err != nil {
			return err
		}
		if err := event(ctx, q, orderID, orders.Failed, orders.Confirmed, "late payment found", ""); err != nil {
			return err
		}
		if err := audit(ctx, q, SystemActor, "late_payment_confirmed", "order", o.OrderNo, nil); err != nil {
			return err
		}
		return enqueueOrderEmail(ctx, q, notifications.TemplateOrderConfirmed, o, lines, "confirmed:"+orderID)
	})
	if errors.Is(err, ErrNoStockForLatePayment) {
		if ferr := s.Flag(ctx, orderID, "late_payment_no_stock"); ferr != nil {
			return ferr
		}
	}
	return err
}

// Flag sets an attention reason with an admin action row.
func (s *Store) Flag(ctx context.Context, orderID, reason string) error {
	return s.inTx(ctx, func(q *store.Queries) error {
		o, err := q.GetOrder(ctx, orderID)
		if err != nil {
			return notFound(err)
		}
		if err := q.SetAttention(ctx, store.SetAttentionParams{AttentionReason: nullStr(reason), ID: orderID}); err != nil {
			return err
		}
		return audit(ctx, q, SystemActor, "order_flagged", "order", o.OrderNo, map[string]any{"reason": reason})
	})
}

// BumpPaymentChecks counts one reconciler look at a pending order.
func (s *Store) BumpPaymentChecks(ctx context.Context, orderID string) error {
	return s.q.BumpPaymentChecks(ctx, orderID)
}

// PendingOrdersOlderThan lists PENDING_PAYMENT orders placed before cutoff.
func (s *Store) PendingOrdersOlderThan(ctx context.Context, cutoff time.Time) ([]Order, error) {
	rows, err := s.q.PendingPaymentOrders(ctx, cutoff)
	if err != nil {
		return nil, err
	}
	out := make([]Order, 0, len(rows))
	for _, r := range rows {
		out = append(out, orderFrom(r))
	}
	return out, nil
}

// TransitionOrder is a Fulfilment Ops status change (backend-spec §8.4): CAS on
// status, an order event, an admin action and the matching email, in one
// transaction. A stale or disallowed change is ErrStateConflict.
func (s *Store) TransitionOrder(ctx context.Context, orderNo string, to orders.Status, a Actor, note string) (*Order, error) {
	var out *Order
	err := s.inTx(ctx, func(q *store.Queries) error {
		o, err := q.GetOrderByNo(ctx, orderNo)
		if err != nil {
			return notFound(err)
		}
		from := orders.Status(o.Status)
		if err := orders.Transition(from, to, orders.ActorFulfilment); err != nil {
			return ErrStateConflict
		}
		var n int64
		var template, dedupe string
		switch to {
		case orders.ReadyForPickup:
			n, err = q.MarkReady(ctx, o.ID)
			template, dedupe = notifications.TemplateReadyForPickup, "ready:"+o.ID
		case orders.Collected:
			n, err = q.MarkCollected(ctx, store.MarkCollectedParams{CollectedByEmail: nullStr(a.Email), ID: o.ID})
			template, dedupe = notifications.TemplateCollected, "collected:"+o.ID
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrStateConflict
		}
		if err := event(ctx, q, o.ID, from, to, note, a.Email); err != nil {
			return err
		}
		if err := audit(ctx, q, a, "order_"+strings.ToLower(string(to)), "order", o.OrderNo, map[string]any{"from": from}); err != nil {
			return err
		}
		lines, err := q.ListOrderLines(ctx, o.ID)
		if err != nil {
			return err
		}
		if err := enqueueOrderEmail(ctx, q, template, o, lines, dedupe); err != nil {
			return err
		}
		fresh, err := q.GetOrder(ctx, o.ID)
		if err != nil {
			return err
		}
		v := orderFrom(fresh)
		out = &v
		return nil
	})
	return out, err
}

// AddOrderNote adds a fulfilment note with an admin action row.
func (s *Store) AddOrderNote(ctx context.Context, orderNo string, a Actor, note string) error {
	return s.inTx(ctx, func(q *store.Queries) error {
		o, err := q.GetOrderByNo(ctx, orderNo)
		if err != nil {
			return notFound(err)
		}
		if err := q.InsertOrderNote(ctx, store.InsertOrderNoteParams{OrderID: o.ID, Note: note, ActorEmail: a.Email}); err != nil {
			return err
		}
		return audit(ctx, q, a, "order_note_added", "order", o.OrderNo, nil)
	})
}

func event(ctx context.Context, q *store.Queries, orderID string, from, to orders.Status, note, actor string) error {
	return q.InsertOrderEvent(ctx, store.InsertOrderEventParams{
		OrderID: orderID, FromStatus: nullStr(string(from)), ToStatus: string(to), Note: nullStr(note), ActorEmail: nullStr(actor),
	})
}

// enqueueOrderEmail queues one order email, built from the order, its lines and office.
func enqueueOrderEmail(ctx context.Context, q *store.Queries, template string, o store.Order, lines []store.OrderLine, dedupe string) error {
	payload := notifications.OrderEmail{OrderNo: o.OrderNo, FirstName: firstName(o.UserName), OfficeName: o.OfficeName,
		Total: o.TotalCoins, PlacedAt: o.PlacedAt}
	if off, err := q.GetOffice(ctx, o.PickupOfficeID); err == nil {
		payload.CollectionPoint, payload.Hours, payload.LeadTimeDays = off.CollectionPoint, off.Hours, int(off.LeadTimeDays)
	}
	for _, l := range lines {
		payload.Lines = append(payload.Lines, notifications.EmailLine{Name: l.Name, Options: l.OptionsLabel,
			Quantity: int(l.Quantity), UnitPrice: l.UnitPriceCoins})
	}
	return enqueue(ctx, q, template, o.UserEmail, payload, dedupe)
}

func firstName(full string) string {
	name, _, _ := strings.Cut(strings.TrimSpace(full), " ")
	return name
}

// OrderDetail returns an order with its lines, timeline, notes and office.
// When email is set the order must belong to that user (else ErrNotFound).
func (s *Store) OrderDetail(ctx context.Context, orderNo, email string) (*OrderDetail, error) {
	var o store.Order
	var err error
	if email != "" {
		o, err = s.q.GetOrderByNoForUser(ctx, store.GetOrderByNoForUserParams{OrderNo: orderNo, UserEmail: email})
	} else {
		o, err = s.q.GetOrderByNo(ctx, orderNo)
	}
	if err != nil {
		return nil, notFound(err)
	}
	d := &OrderDetail{Order: orderFrom(o)}
	lines, err := s.q.ListOrderLines(ctx, o.ID)
	if err != nil {
		return nil, err
	}
	for _, l := range lines {
		d.Lines = append(d.Lines, OrderLine{ID: l.ID, ProductID: l.ProductID, VariantID: l.VariantID, SKU: l.Sku,
			Name: l.Name, Options: l.OptionsLabel, UnitPrice: l.UnitPriceCoins, Quantity: int(l.Quantity)})
	}
	events, err := s.q.ListOrderEvents(ctx, o.ID)
	if err != nil {
		return nil, err
	}
	for _, e := range events {
		d.Events = append(d.Events, OrderEvent{From: e.FromStatus.String, To: e.ToStatus, Note: e.Note.String,
			Actor: e.ActorEmail.String, CreatedAt: e.CreatedAt})
	}
	if email == "" {
		notes, err := s.q.ListOrderNotes(ctx, o.ID)
		if err != nil {
			return nil, err
		}
		for _, n := range notes {
			d.Notes = append(d.Notes, OrderNote{Note: n.Note, Actor: n.ActorEmail, CreatedAt: n.CreatedAt})
		}
	}
	if d.Office, err = s.Office(ctx, o.PickupOfficeID); err != nil {
		return nil, err
	}
	return d, nil
}

// OrdersForUser lists the caller's orders, newest first.
func (s *Store) OrdersForUser(ctx context.Context, email string, limit, offset int) ([]Order, int, error) {
	rows, err := s.q.ListOrdersForUser(ctx, store.ListOrdersForUserParams{UserEmail: email, Limit: int32(limit), Offset: int32(offset)}) // #nosec G115
	if err != nil {
		return nil, 0, err
	}
	total, err := s.q.CountOrdersForUser(ctx, email)
	if err != nil {
		return nil, 0, err
	}
	out := make([]Order, 0, len(rows))
	for _, r := range rows {
		out = append(out, orderFrom(r))
	}
	return out, int(total), nil
}
