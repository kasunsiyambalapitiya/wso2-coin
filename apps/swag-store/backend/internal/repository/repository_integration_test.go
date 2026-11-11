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

package repository_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/orders"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/repository"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/testdb"
)

var admin = repository.Actor{Email: "admin@wso2.com", UserID: "u-admin"}

type fixture struct {
	s         *repository.Store
	productID string
	variantID string
	officeID  string
}

// newFixture makes an ACTIVE product with one variant holding stock units, and one office.
func newFixture(t *testing.T, stock int) *fixture {
	t.Helper()
	ctx := context.Background()
	s := repository.New(testdb.Open(t))
	cat, err := s.SaveCategory(ctx, admin, repository.Category{Slug: "apparel", Name: "Apparel"})
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.CreateProduct(ctx, admin, repository.ProductInput{Name: "WSO2 Hoodie", Slug: "wso2-hoodie",
		CategoryID: cat.ID, Price: 90, RestockPolicy: "RESTOCKABLE"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetProductStatus(ctx, admin, d.Product.ID, "ACTIVE", ""); err != nil {
		t.Fatal(err)
	}
	v := d.Variants[0].ID
	if stock > 0 {
		if _, err := s.RecordStockMovement(ctx, admin, v, repository.MoveReceive, stock, "first delivery"); err != nil {
			t.Fatal(err)
		}
	}
	o, err := s.SaveOffice(ctx, admin, repository.Office{Name: "Colombo office", LeadTimeDays: 2, HRLocations: []string{"Colombo"}, Active: true})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{s: s, productID: d.Product.ID, variantID: v, officeID: o.ID}
}

func (f *fixture) pendingOrder(t *testing.T, email string, qty int) (*repository.Order, error) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.s.AddToCart(ctx, email, f.variantID, qty); err != nil {
		t.Fatal(err)
	}
	return f.s.CreatePendingOrder(ctx, repository.NewOrder{
		ID: uuid.Must(uuid.NewV7()).String(), UserEmail: email, UserID: "u-" + email, UserName: "Test User",
		OfficeID: f.officeID, OfficeName: "Colombo office", WalletAddress: "0xuser", IdempotencyKey: uuid.NewString(),
		Check: func(items []repository.CartItem) (int64, error) {
			var total int64
			for _, it := range items {
				total += it.UnitPrice * int64(it.Quantity)
			}
			return total, nil
		},
	})
}

func (f *fixture) stock(t *testing.T) (onHand, reserved int) {
	t.Helper()
	d, err := f.s.ProductByID(context.Background(), f.productID)
	if err != nil {
		t.Fatal(err)
	}
	return d.Variants[0].OnHand, d.Variants[0].Reserved
}

func TestRemovedProductOrdersStillFulfil(t *testing.T) {
	f := newFixture(t, 3)
	ctx := context.Background()
	o, err := f.pendingOrder(t, "you@wso2.com", 1)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := f.s.ConfirmOrder(ctx, o.ID); !ok || err != nil {
		t.Fatalf("confirm = %v %v", ok, err)
	}
	if err := f.s.SetProductStatus(ctx, admin, f.productID, "REMOVED", "supplier stopped"); err != nil {
		t.Fatal(err)
	}
	for _, to := range []orders.Status{orders.ReadyForPickup, orders.Collected} {
		if _, err := f.s.TransitionOrder(ctx, o.OrderNo, to, admin, ""); err != nil {
			t.Fatalf("%s after removal: %v", to, err)
		}
	}
	if onHand, reserved := f.stock(t); onHand != 2 || reserved != 0 {
		t.Fatalf("stock = %d/%d", onHand, reserved)
	}
}

func TestTransitionCASConflict(t *testing.T) {
	f := newFixture(t, 3)
	ctx := context.Background()
	o, _ := f.pendingOrder(t, "you@wso2.com", 1)
	_, _ = f.s.ConfirmOrder(ctx, o.ID)
	if _, err := f.s.TransitionOrder(ctx, o.OrderNo, orders.ReadyForPickup, admin, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.TransitionOrder(ctx, o.OrderNo, orders.Collected, admin, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.TransitionOrder(ctx, o.OrderNo, orders.Collected, admin, ""); !errors.Is(err, repository.ErrStateConflict) {
		t.Fatalf("second collect = %v", err)
	}
	d, err := f.s.OrderDetail(ctx, o.OrderNo, "")
	if err != nil {
		t.Fatal(err)
	}
	collected := 0
	for _, e := range d.Events {
		if e.To == string(orders.Collected) {
			collected++
		}
	}
	if collected != 1 {
		t.Fatalf("collected events = %d", collected)
	}
}

func TestFailReleasesStock(t *testing.T) {
	f := newFixture(t, 2)
	ctx := context.Background()
	o, _ := f.pendingOrder(t, "you@wso2.com", 2)
	if _, reserved := f.stock(t); reserved != 2 {
		t.Fatalf("reserved = %d", reserved)
	}
	if ok, err := f.s.FailOrder(ctx, o.ID, repository.Failure{Code: "insufficient_funds", Notify: true}); !ok || err != nil {
		t.Fatalf("fail = %v %v", ok, err)
	}
	if onHand, reserved := f.stock(t); onHand != 2 || reserved != 0 {
		t.Fatalf("stock = %d/%d", onHand, reserved)
	}
	if ok, _ := f.s.ConfirmOrder(ctx, o.ID); ok {
		t.Fatal("a failed order confirmed through the pending path")
	}
}

func TestReconcilerConfirmsLatePayment(t *testing.T) {
	f := newFixture(t, 2)
	ctx := context.Background()
	o, _ := f.pendingOrder(t, "you@wso2.com", 1)
	_, _ = f.s.FailOrder(ctx, o.ID, repository.Failure{Code: "payment_not_found"})
	if err := f.s.ConfirmLatePayment(ctx, o.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := f.s.OrderByID(ctx, o.ID)
	if got.Status != orders.Confirmed {
		t.Fatalf("status = %s", got.Status)
	}
	if onHand, reserved := f.stock(t); onHand != 1 || reserved != 0 {
		t.Fatalf("stock = %d/%d", onHand, reserved)
	}
}

func TestReconcilerConfirmsLatePaymentFlagsNoStock(t *testing.T) {
	f := newFixture(t, 1)
	ctx := context.Background()
	o, _ := f.pendingOrder(t, "you@wso2.com", 1)
	_, _ = f.s.FailOrder(ctx, o.ID, repository.Failure{Code: "payment_not_found"})
	other, _ := f.pendingOrder(t, "other@wso2.com", 1)
	_, _ = f.s.ConfirmOrder(ctx, other.ID)
	if err := f.s.ConfirmLatePayment(ctx, o.ID); !errors.Is(err, repository.ErrNoStockForLatePayment) {
		t.Fatalf("late payment without stock = %v", err)
	}
	got, _ := f.s.OrderByID(ctx, o.ID)
	if got.Status != orders.Failed || got.AttentionReason != "late_payment_no_stock" {
		t.Fatalf("order = %+v", got)
	}
}

func TestNotifyOnReceiveAndRemovalClosesAlerts(t *testing.T) {
	f := newFixture(t, 0)
	ctx := context.Background()
	if _, err := f.s.CreateAlert(ctx, "a@wso2.com", f.variantID); err != nil {
		t.Fatal(err)
	}
	id2, _ := f.s.CreateAlert(ctx, "a@wso2.com", f.variantID)
	if alerts, _ := f.s.Alerts(ctx, "a@wso2.com"); len(alerts) != 1 || alerts[0].ID != id2 {
		t.Fatalf("duplicate pending alert: %+v", alerts)
	}
	res, err := f.s.RecordStockMovement(ctx, admin, f.variantID, repository.MoveReceive, 5, "restock")
	if err != nil || res.Notified != 1 {
		t.Fatalf("receive = %+v %v", res, err)
	}
	if alerts, _ := f.s.Alerts(ctx, "a@wso2.com"); alerts[0].Status != "NOTIFIED" {
		t.Fatalf("alert = %+v", alerts[0])
	}

	g := newFixture(t, 0)
	if _, err := g.s.CreateAlert(ctx, "b@wso2.com", g.variantID); err != nil {
		t.Fatal(err)
	}
	if err := g.s.SetProductStatus(ctx, admin, g.productID, "REMOVED", ""); err != nil {
		t.Fatal(err)
	}
	if alerts, _ := g.s.Alerts(ctx, "b@wso2.com"); alerts[0].Status != "CLOSED" {
		t.Fatalf("alert after removal = %+v", alerts[0])
	}
	emails, err := g.s.ClaimDueEmails(ctx)
	if err != nil {
		t.Fatal(err)
	}
	removed := 0
	for _, e := range emails {
		if e.Template == "product_removed" {
			removed++
		}
	}
	if removed != 1 {
		t.Fatalf("product_removed emails = %d", removed)
	}
}

func TestDeleteDraftOnlyWhenNeverOrdered(t *testing.T) {
	f := newFixture(t, 1)
	ctx := context.Background()
	if err := f.s.DeleteDraft(ctx, admin, f.productID); !errors.Is(err, repository.ErrStateConflict) {
		t.Fatalf("delete ACTIVE = %v", err)
	}
	d, err := f.s.CreateProduct(ctx, admin, repository.ProductInput{Name: "Draft", Slug: "draft", Price: 10, RestockPolicy: "LIMITED"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.DeleteDraft(ctx, admin, d.Product.ID); err != nil {
		t.Fatal(err)
	}
}

func TestSetOptionsKeepsSKUs(t *testing.T) {
	f := newFixture(t, 0)
	ctx := context.Background()
	size := func(vals ...string) []repository.OptionInput {
		in := repository.OptionInput{Name: "Size"}
		for _, v := range vals {
			in.Values = append(in.Values, repository.OptionValueInput{Value: v})
		}
		return []repository.OptionInput{in}
	}
	d, err := f.s.SetOptions(ctx, admin, f.productID, size("S", "M"))
	if err != nil {
		t.Fatal(err)
	}
	skus := map[string]string{}
	for _, v := range d.Variants {
		if v.Active {
			skus[v.Options] = v.SKU
		}
	}
	if len(skus) != 2 {
		t.Fatalf("variants = %+v", d.Variants)
	}
	d, err = f.s.SetOptions(ctx, admin, f.productID, size("M", "L"))
	if err != nil {
		t.Fatal(err)
	}
	active := map[string]string{}
	for _, v := range d.Variants {
		if v.Active {
			active[v.Options] = v.SKU
		}
	}
	if active["M"] != skus["M"] || active["L"] == "" || active["S"] != "" {
		t.Fatalf("after change: %+v (before %+v)", active, skus)
	}
}
