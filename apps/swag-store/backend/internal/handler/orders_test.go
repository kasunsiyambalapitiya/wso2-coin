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
	"strings"
	"testing"
	"time"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/checkout"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/orders"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/repository"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/reqctx"
)

const (
	testOffice = "0192f3a4-0000-7000-8000-000000000001"
	testKey    = "0192f3a4-0000-4000-8000-0000000000aa"
)

type fakeCheckout struct {
	quote      *checkout.Quote
	order      *repository.Order
	err        error
	placeCalls []checkout.PlaceInput
	quoteCalls int
}

func (f *fakeCheckout) Quote(context.Context, checkout.QuoteInput) (*checkout.Quote, error) {
	f.quoteCalls++
	return f.quote, f.err
}

func (f *fakeCheckout) Place(_ context.Context, in checkout.PlaceInput) (*repository.Order, error) {
	f.placeCalls = append(f.placeCalls, in)
	return f.order, f.err
}

func (f *fakeCheckout) RetryPayment(context.Context, *reqctx.User, string) (*repository.Order, error) {
	return f.order, f.err
}

type fakeOrders struct{ detail *repository.OrderDetail }

func (f *fakeOrders) OrdersForUser(context.Context, string, int, int) ([]repository.Order, int, error) {
	return nil, 0, nil
}

func (f *fakeOrders) OrderDetail(_ context.Context, no, email string) (*repository.OrderDetail, error) {
	if f.detail == nil || f.detail.Order.OrderNo != no || (email != "" && f.detail.Order.UserEmail != email) {
		return nil, repository.ErrNotFound
	}
	return f.detail, nil
}

func orderOf(status orders.Status, failure string) *repository.Order {
	return &repository.Order{ID: "0192f3a4-0000-7000-8000-0000000000bb", OrderNo: "SS-10001", UserEmail: shopper.Email,
		Status: status, Total: 180, OfficeName: "Colombo office", FailureCode: failure, PlacedAt: time.Unix(1_800_000_000, 0)}
}

const orderBody = `{"pickupOfficeId":"` + testOffice + `","walletAddress":"0xuser"}`

func placeOrder(f *fakeCheckout, body string, headers ...string) (int, errorBody, string) {
	h := NewOrdersHandler(f, &fakeOrders{})
	rec := do(testRouter(shopper, http.MethodPost, "/orders", h.Place), http.MethodPost, "/orders", body, headers...)
	var eb errorBody
	_ = jsonUnmarshal(rec.Body.Bytes(), &eb)
	return rec.Code, eb, rec.Body.String()
}

func TestPlaceOrderStatusCodes(t *testing.T) {
	cases := []struct {
		name   string
		order  *repository.Order
		err    error
		status int
		code   string
	}{
		{"confirmed", orderOf(orders.Confirmed, ""), nil, 201, ""},
		{"pending", orderOf(orders.PendingPayment, ""), nil, 202, ""},
		{"replay of a failed order", orderOf(orders.Failed, "insufficient_funds"), nil, 402, CodeInsufficientFunds},
		{"replay of a wallet refusal", orderOf(orders.Failed, "wallet_not_owned"), nil, 403, CodeWalletNotOwned},
		{"replay of a mismatch", orderOf(orders.Failed, "payment_mismatch"), nil, 402, CodePaymentFailed},
		{"hr down", nil, checkout.ErrHRUnavailable, 503, CodeHRUnavailable},
		{"not active", nil, checkout.ErrNotActiveEmployee, 403, CodeNotActiveEmployee},
		{"paused", nil, checkout.ErrPaymentsPaused, 503, CodePaymentsPaused},
		{"quote changed", nil, checkout.ErrQuoteChanged, 409, CodeQuoteChanged},
		{"insufficient", orderOf(orders.Failed, "insufficient_funds"), checkout.ErrInsufficientFunds, 402, CodeInsufficientFunds},
		{"wallet", orderOf(orders.Failed, "wallet_not_owned"), checkout.ErrWalletNotOwned, 403, CodeWalletNotOwned},
		{"refused", orderOf(orders.Failed, "payment_failed"), checkout.ErrPaymentFailed, 402, CodePaymentFailed},
		{"mismatch", orderOf(orders.Failed, "payment_mismatch"), checkout.ErrPaymentMismatch, 402, CodePaymentFailed},
	}
	for _, tc := range cases {
		status, eb, raw := placeOrder(&fakeCheckout{order: tc.order, err: tc.err}, orderBody, "Idempotency-Key", testKey)
		if status != tc.status || eb.ErrorCode != tc.code {
			t.Errorf("%s: %d %s, want %d %s", tc.name, status, raw, tc.status, tc.code)
		}
	}
	status, _, raw := placeOrder(&fakeCheckout{order: orderOf(orders.Confirmed, "")}, orderBody, "Idempotency-Key", testKey)
	if status != 201 || !strings.Contains(raw, `"orderNo":"SS-10001"`) || !strings.Contains(raw, `"status":"CONFIRMED"`) {
		t.Fatalf("201 body = %s", raw)
	}
}

func TestPlaceOrderBodyCannotNamePayee(t *testing.T) {
	f := &fakeCheckout{order: orderOf(orders.Confirmed, "")}
	body := `{"pickupOfficeId":"` + testOffice + `","walletAddress":"0xuser","toAddress":"0xattacker"}`
	status, _, raw := placeOrder(f, body, "Idempotency-Key", testKey)
	if status != 400 || !strings.Contains(raw, "toAddress") || len(f.placeCalls) != 0 {
		t.Fatalf("payee in body: %d %s, calls %d", status, raw, len(f.placeCalls))
	}
}

func TestPlaceOrderNeedsIdempotencyKey(t *testing.T) {
	f := &fakeCheckout{order: orderOf(orders.Confirmed, "")}
	for _, hdr := range [][]string{nil, {"Idempotency-Key", "not-a-uuid"}} {
		status, _, _ := placeOrder(f, orderBody, hdr...)
		if status != 400 {
			t.Errorf("key %v: %d", hdr, status)
		}
	}
	if len(f.placeCalls) != 0 {
		t.Fatal("placed without a key")
	}
	placeOrder(f, orderBody, "Idempotency-Key", strings.ToUpper(testKey))
	if len(f.placeCalls) != 1 || f.placeCalls[0].IdempotencyKey != testKey || f.placeCalls[0].User.Email != shopper.Email {
		t.Fatalf("calls = %+v", f.placeCalls)
	}
}

func TestPlaceOrderValidatesBody(t *testing.T) {
	for _, body := range []string{`{}`, `{"pickupOfficeId":"x","walletAddress":"0xuser"}`, `{"pickupOfficeId":"` + testOffice + `"}`} {
		status, _, _ := placeOrder(&fakeCheckout{}, body, "Idempotency-Key", testKey)
		if status != 400 {
			t.Errorf("body %s: %d", body, status)
		}
	}
}

func TestQuoteResponse(t *testing.T) {
	reason := "Only 1 left of WSO2 Hoodie · Black · M."
	f := &fakeCheckout{quote: &checkout.Quote{
		Lines: []checkout.QuoteLine{
			{Item: repository.CartItem{LineID: "l1", Name: "WSO2 Hoodie", Options: "Black · M", UnitPrice: 90, Quantity: 2}, State: "OK"},
			{Item: repository.CartItem{LineID: "l2", Name: "Umbrella", UnitPrice: 40, Quantity: 1}, State: "OUT_OF_STOCK", Reason: &reason},
		},
		Total: 180, Office: &repository.Office{ID: testOffice, Name: "Colombo office", LeadTimeDays: 2},
		Wallet: &checkout.WalletView{Address: "0xuser", Balance: "202.50"}, CanPlace: false,
		Problems: []checkout.Problem{{Code: "lines_unavailable", Message: "x"}},
	}}
	h := NewOrdersHandler(f, &fakeOrders{})
	rec := do(testRouter(shopper, http.MethodPost, "/quotes", h.Quote), http.MethodPost, "/quotes", orderBody)
	body := decode[map[string]any](t, rec)
	if rec.Code != 200 || body["total"] != float64(180) || body["canPlace"] != false {
		t.Fatalf("quote = %d %v", rec.Code, body)
	}
	lines := body["lines"].([]any)
	l1 := lines[0].(map[string]any)
	if l1["lineId"] != "l1" || l1["unitPrice"] != float64(90) || l1["reason"] != nil {
		t.Fatalf("line 1 = %v", l1)
	}
	if lines[1].(map[string]any)["reason"] != reason {
		t.Fatalf("line 2 = %v", lines[1])
	}
	if body["wallet"].(map[string]any)["balance"] != "202.50" || body["pickupOffice"].(map[string]any)["leadTimeDays"] != float64(2) {
		t.Fatalf("wallet/office = %v", body)
	}
}

func TestQuoteHRDown(t *testing.T) {
	h := NewOrdersHandler(&fakeCheckout{err: checkout.ErrHRUnavailable}, &fakeOrders{})
	rec := do(testRouter(shopper, http.MethodPost, "/quotes", h.Quote), http.MethodPost, "/quotes", orderBody)
	if rec.Code != 503 || decode[errorBody](t, rec).ErrorCode != CodeHRUnavailable {
		t.Fatalf("quote with HR down = %d %s", rec.Code, rec.Body)
	}
	h = NewOrdersHandler(&fakeCheckout{err: checkout.ErrCoinsUnavailable}, &fakeOrders{})
	rec = do(testRouter(shopper, http.MethodPost, "/quotes", h.Quote), http.MethodPost, "/quotes", orderBody)
	if rec.Code != 503 || decode[errorBody](t, rec).ErrorCode != CodeCoinsUnavailable {
		t.Fatalf("quote with coins down = %d %s", rec.Code, rec.Body)
	}
}

func TestOrderDetailIsOwnOnly(t *testing.T) {
	other := *orderOf(orders.Confirmed, "")
	other.UserEmail = "someone@wso2.com"
	h := NewOrdersHandler(&fakeCheckout{}, &fakeOrders{detail: &repository.OrderDetail{Order: other}})
	rec := do(testRouter(shopper, http.MethodGet, "/orders/:orderNo", h.Get), http.MethodGet, "/orders/SS-10001", "")
	if rec.Code != 404 {
		t.Fatalf("another user's order = %d", rec.Code)
	}
	mine := *orderOf(orders.Confirmed, "")
	h = NewOrdersHandler(&fakeCheckout{}, &fakeOrders{detail: &repository.OrderDetail{Order: mine,
		Lines:  []repository.OrderLine{{Name: "WSO2 Hoodie", Quantity: 2, UnitPrice: 90}},
		Office: &repository.Office{Name: "Colombo office", CollectionPoint: "Reception", Hours: "9-5"}}})
	rec = do(testRouter(shopper, http.MethodGet, "/orders/:orderNo", h.Get), http.MethodGet, "/orders/SS-10001", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"collectionPoint":"Reception"`) ||
		strings.Contains(rec.Body.String(), "walletAddress") {
		t.Fatalf("own order = %d %s", rec.Code, rec.Body)
	}
}

func TestRetryPaymentMapping(t *testing.T) {
	cases := map[error]int{checkout.ErrStateConflict: 409, checkout.ErrPaymentsPaused: 503, repository.ErrNotFound: 404}
	for err, want := range cases {
		h := NewOrdersHandler(&fakeCheckout{order: orderOf(orders.Confirmed, ""), err: err}, &fakeOrders{})
		rec := do(testRouter(shopper, http.MethodPost, "/orders/:orderNo/payments", h.RetryPayment), http.MethodPost, "/orders/SS-10001/payments", "")
		if rec.Code != want {
			t.Errorf("%v: %d, want %d", err, rec.Code, want)
		}
	}
	h := NewOrdersHandler(&fakeCheckout{order: orderOf(orders.Confirmed, "")}, &fakeOrders{})
	rec := do(testRouter(shopper, http.MethodPost, "/orders/:orderNo/payments", h.RetryPayment), http.MethodPost, "/orders/SS-10001/payments", "")
	if rec.Code != 201 {
		t.Fatalf("retry ok = %d", rec.Code)
	}
	rec = do(testRouter(shopper, http.MethodPost, "/orders/:orderNo/payments", h.RetryPayment), http.MethodPost, "/orders/bad%20no/payments", "")
	if rec.Code != 404 {
		t.Fatalf("bad order number = %d", rec.Code)
	}
}
