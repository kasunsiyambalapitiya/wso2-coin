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

package checkout

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"testing"
	"time"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/apierror"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/catalog"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/coins"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/hr"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/orders"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/repository"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/reqctx"
)

const (
	pin       = "0xtreasury"
	userWall  = "0xuser"
	officeID  = "0192f3a4-0000-7000-8000-000000000001"
	idemKey   = "0192f3a4-0000-4000-8000-0000000000aa"
	orderUUID = "0192f3a4-0000-7000-8000-0000000000bb"
)

// --- fakes ---

type fakeStore struct {
	items     []repository.CartItem
	office    *repository.Office
	byKey     *repository.Order
	created   []*repository.Order
	confirmed []string
	failed    map[string]repository.Failure
	bumped    []string
	createErr error
	orders    map[string]*repository.Order
	// confirmLost / failLost make the CAS lose to another path.
	confirmLost bool
	failLost    bool
	late        []string
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		items: []repository.CartItem{
			{LineID: "l1", VariantID: "v1", ProductID: "p1", Name: "WSO2 Hoodie", Options: "Black · M", Quantity: 2,
				UnitPrice: 90, Free: 5, ProductStatus: catalog.StatusActive, VariantActive: true},
		},
		office: &repository.Office{ID: officeID, Name: "Colombo office", LeadTimeDays: 2, Active: true},
		failed: map[string]repository.Failure{},
		orders: map[string]*repository.Order{},
	}
}

func (f *fakeStore) CartForQuote(context.Context, string) ([]repository.CartItem, error) {
	return f.items, nil
}

func (f *fakeStore) Office(_ context.Context, id string) (*repository.Office, error) {
	if f.office == nil || f.office.ID != id {
		return nil, nil
	}
	return f.office, nil
}

func (f *fakeStore) OrderByKey(context.Context, string, string) (*repository.Order, error) {
	return f.byKey, nil
}

func (f *fakeStore) CreatePendingOrder(_ context.Context, n repository.NewOrder) (*repository.Order, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	total, err := n.Check(f.items)
	if err != nil {
		return nil, err
	}
	o := &repository.Order{ID: n.ID, OrderNo: "SS-10001", UserEmail: n.UserEmail, Status: orders.PendingPayment,
		OfficeID: n.OfficeID, OfficeName: n.OfficeName, WalletAddress: n.WalletAddress, Total: total,
		PaymentReference: orders.PaymentReference(n.ID), IdempotencyKey: n.IdempotencyKey, PlacedAt: time.Now()}
	f.created = append(f.created, o)
	f.orders[o.ID] = o
	return o, nil
}

func (f *fakeStore) ConfirmOrder(_ context.Context, id string) (bool, error) {
	if f.confirmLost {
		f.orders[id].Status = orders.Failed
		return false, nil
	}
	f.confirmed = append(f.confirmed, id)
	if o := f.orders[id]; o != nil {
		o.Status = orders.Confirmed
	}
	return true, nil
}

func (f *fakeStore) FailOrder(_ context.Context, id string, fl repository.Failure) (bool, error) {
	if f.failLost {
		f.orders[id].Status = orders.Confirmed
		return false, nil
	}
	f.failed[id] = fl
	if o := f.orders[id]; o != nil {
		o.Status = orders.Failed
		o.FailureCode = fl.Code
	}
	return true, nil
}

func (f *fakeStore) OrderByNoForUser(_ context.Context, no, email string) (*repository.Order, error) {
	for _, o := range f.orders {
		if o.OrderNo == no && o.UserEmail == email {
			return o, nil
		}
	}
	return nil, nil
}

func (f *fakeStore) OrderByID(_ context.Context, id string) (*repository.Order, error) {
	if o := f.orders[id]; o != nil {
		c := *o
		return &c, nil
	}
	return nil, nil
}

func (f *fakeStore) ConfirmLatePayment(_ context.Context, id string) error {
	f.late = append(f.late, id)
	f.orders[id].Status = orders.Confirmed
	return nil
}

func (f *fakeStore) BumpPaymentChecks(_ context.Context, id string) error {
	f.bumped = append(f.bumped, id)
	return nil
}

type fakeCoins struct {
	wallets    []coins.Wallet
	walletsErr error
	pays       []coins.PayRequest
	payReply   func(coins.PayRequest) (*coins.Payment, error)
	tx         *coins.Transaction
	txErr      error
	treasury   string
	treasErr   error
}

func newFakeCoins() *fakeCoins {
	return &fakeCoins{
		wallets:  []coins.Wallet{{Address: userWall, Balance: big.NewRat(500, 1), BalanceText: "500", Default: true}},
		treasury: pin,
		payReply: func(r coins.PayRequest) (*coins.Payment, error) {
			return &coins.Payment{Reference: r.Reference, FromAddress: r.FromAddress, ToAddress: r.ToAddress, Amount: r.Amount}, nil
		},
	}
}

func (f *fakeCoins) Wallets(context.Context) ([]coins.Wallet, error) { return f.wallets, f.walletsErr }

func (f *fakeCoins) Pay(_ context.Context, r coins.PayRequest) (*coins.Payment, error) {
	f.pays = append(f.pays, r)
	return f.payReply(r)
}

func (f *fakeCoins) Transaction(context.Context, string) (*coins.Transaction, error) {
	return f.tx, f.txErr
}

func (f *fakeCoins) Treasury(context.Context) (string, error) { return f.treasury, f.treasErr }

type fakeHR struct {
	emp *hr.Employee
	err error
}

func (f *fakeHR) Lookup(context.Context, string) (*hr.Employee, error) { return f.emp, f.err }

type rig struct {
	store *fakeStore
	coins *fakeCoins
	hr    *fakeHR
	svc   *Service
}

func newRig() *rig {
	r := &rig{store: newFakeStore(), coins: newFakeCoins(), hr: &fakeHR{emp: &hr.Employee{EmployeeStatus: "Active"}}}
	r.svc = NewService(r.store, r.coins, r.hr, NewTreasuryGuard(pin, r.coins))
	r.svc.newID = func() string { return orderUUID }
	return r
}

var user = &reqctx.User{Email: "you@wso2.com", UserID: "u-1", FirstName: "Your", LastName: "Name"}

func ctx() context.Context {
	return reqctx.WithUserAssertion(reqctx.WithUser(context.Background(), user), "jwt")
}

func placeInput() PlaceInput {
	return PlaceInput{User: user, OfficeID: officeID, WalletAddress: userWall, IdempotencyKey: idemKey}
}

// --- quote ---

func TestQuoteHappyPath(t *testing.T) {
	r := newRig()
	q, err := r.svc.Quote(ctx(), QuoteInput{User: user, OfficeID: officeID, WalletAddress: userWall})
	if err != nil {
		t.Fatal(err)
	}
	if q.Total != 180 || !q.CanPlace || len(q.Problems) != 0 || q.Lines[0].State != catalog.LineOK {
		t.Fatalf("quote = %+v", q)
	}
	if q.Wallet == nil || q.Wallet.Balance != "500" || q.Office == nil || q.Office.Name != "Colombo office" {
		t.Fatalf("quote wallet/office = %+v %+v", q.Wallet, q.Office)
	}
	if len(r.coins.pays) != 0 {
		t.Fatal("a quote sent a payment")
	}
}

func TestQuoteProblems(t *testing.T) {
	cases := map[string]struct {
		mutate func(*rig)
		code   string
	}{
		"low balance":   {func(r *rig) { r.coins.wallets[0].Balance = big.NewRat(10, 1) }, CodeInsufficientFunds},
		"leaver gone":   {func(r *rig) { r.hr.emp = &hr.Employee{EmployeeStatus: "Left"} }, CodeNotActiveEmployee},
		"no hr record":  {func(r *rig) { r.hr.emp = nil }, CodeNotActiveEmployee},
		"office closed": {func(r *rig) { r.store.office.Active = false }, CodeOfficeUnavailable},
		"other wallet":  {func(r *rig) { r.coins.wallets[0].Address = "0xother" }, CodeWalletNotOwned},
		"empty cart":    {func(r *rig) { r.store.items = nil }, CodeEmptyCart},
		"out of stock":  {func(r *rig) { r.store.items[0].Free = 0 }, CodeEmptyCart},
	}
	for name, tc := range cases {
		r := newRig()
		tc.mutate(r)
		q, err := r.svc.Quote(ctx(), QuoteInput{User: user, OfficeID: officeID, WalletAddress: userWall})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if q.CanPlace || !hasProblem(q, tc.code) {
			t.Errorf("%s: canPlace=%v problems=%+v, want %s", name, q.CanPlace, q.Problems, tc.code)
		}
	}
}

func TestQuoteLeavesFailedLinesOutOfTotal(t *testing.T) {
	r := newRig()
	r.store.items = append(r.store.items, repository.CartItem{LineID: "l2", Name: "Umbrella", Quantity: 1, UnitPrice: 40,
		Free: 9, ProductStatus: catalog.StatusRemoved, VariantActive: true})
	q, err := r.svc.Quote(ctx(), QuoteInput{User: user, OfficeID: officeID, WalletAddress: userWall})
	if err != nil {
		t.Fatal(err)
	}
	if q.Total != 180 || q.Lines[1].State != catalog.LineRemoved || q.Lines[1].Reason == nil || q.CanPlace {
		t.Fatalf("quote = %+v line2=%+v", q, q.Lines[1])
	}
}

func hasProblem(q *Quote, code string) bool {
	for _, p := range q.Problems {
		if p.Code == code {
			return true
		}
	}
	return false
}

func TestHRDownFailsClosed(t *testing.T) {
	r := newRig()
	r.hr.err = errors.New("hr down")
	if _, err := r.svc.Quote(ctx(), QuoteInput{User: user, OfficeID: officeID, WalletAddress: userWall}); !errors.Is(err, ErrHRUnavailable) {
		t.Fatalf("quote err = %v", err)
	}
	if _, err := r.svc.Place(ctx(), placeInput()); !errors.Is(err, ErrHRUnavailable) {
		t.Fatalf("place err = %v", err)
	}
	if len(r.store.created) != 0 || len(r.coins.pays) != 0 {
		t.Fatal("an order was created or paid while HR was down")
	}
}

func TestPlaceRefusesInactiveEmployee(t *testing.T) {
	r := newRig()
	r.hr.emp = &hr.Employee{EmployeeStatus: "Left"}
	if _, err := r.svc.Place(ctx(), placeInput()); !errors.Is(err, ErrNotActiveEmployee) {
		t.Fatalf("err = %v", err)
	}
	if len(r.store.created) != 0 {
		t.Fatal("order created")
	}
}

// --- place order ---

func TestPayAlwaysTargetsTreasury(t *testing.T) {
	r := newRig()
	o, err := r.svc.Place(ctx(), placeInput())
	if err != nil {
		t.Fatal(err)
	}
	if len(r.coins.pays) != 1 {
		t.Fatalf("pays = %d", len(r.coins.pays))
	}
	p := r.coins.pays[0]
	want := coins.PayRequest{FromAddress: userWall, ToAddress: pin, Amount: "180",
		Reference: "swag:ord:" + orderUUID, Source: "SWAG_STORE"}
	if p != want {
		t.Fatalf("pay = %+v, want %+v", p, want)
	}
	if o.Status != orders.Confirmed {
		t.Fatalf("status = %s", o.Status)
	}
}

func TestPaymentOutcomes(t *testing.T) {
	refuse := func(status int, msg string) func(coins.PayRequest) (*coins.Payment, error) {
		return func(coins.PayRequest) (*coins.Payment, error) {
			return nil, &apierror.Error{StatusCode: status, Body: fmt.Sprintf(`{"message":%q}`, msg)}
		}
	}
	cases := []struct {
		name     string
		reply    func(coins.PayRequest) (*coins.Payment, error)
		err      error
		status   orders.Status
		failCode string
		notify   bool
	}{
		{"201", nil, nil, orders.Confirmed, "", false},
		{"200 replay", func(r coins.PayRequest) (*coins.Payment, error) {
			return &coins.Payment{Reference: r.Reference, FromAddress: r.FromAddress, ToAddress: r.ToAddress, Amount: "180.00", Replayed: true}, nil
		}, nil, orders.Confirmed, "", false},
		{"400 insufficient", refuse(400, "Insufficient funds."), ErrInsufficientFunds, orders.Failed, CodeInsufficientFunds, true},
		{"400 other", refuse(400, "Invalid amount."), ErrPaymentFailed, orders.Failed, CodePaymentFailed, true},
		{"403", refuse(403, "Forbidden."), ErrWalletNotOwned, orders.Failed, CodeWalletNotOwned, true},
		{"409", refuse(409, "Reference reused."), ErrPaymentFailed, orders.Failed, CodePaymentFailed, true},
		{"503", refuse(503, "down"), nil, orders.PendingPayment, "", false},
		{"401", refuse(401, "no"), nil, orders.PendingPayment, "", false},
		{"timeout", func(coins.PayRequest) (*coins.Payment, error) { return nil, context.DeadlineExceeded }, nil, orders.PendingPayment, "", false},
	}
	for _, tc := range cases {
		r := newRig()
		if tc.reply != nil {
			r.coins.payReply = tc.reply
		}
		o, err := r.svc.Place(ctx(), placeInput())
		if !errors.Is(err, tc.err) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.err)
			continue
		}
		if o == nil || o.Status != tc.status {
			t.Errorf("%s: order = %+v, want status %s", tc.name, o, tc.status)
			continue
		}
		if f, failed := r.store.failed[orderUUID]; tc.failCode != "" {
			if !failed || f.Code != tc.failCode || f.Notify != tc.notify || f.Attention != "" {
				t.Errorf("%s: failure = %+v (failed=%v)", tc.name, f, failed)
			}
		} else if failed {
			t.Errorf("%s: order failed unexpectedly: %+v", tc.name, f)
		}
		if tc.status == orders.PendingPayment && (len(r.store.confirmed) != 0 || len(r.store.failed) != 0) {
			t.Errorf("%s: pending order was settled", tc.name)
		}
	}
}

func TestTreasuryMismatchPausesPayments(t *testing.T) {
	for name, mutate := range map[string]func(*fakeCoins){
		"mismatch":     func(c *fakeCoins) { c.treasury = "0xsomeone-else" },
		"check failed": func(c *fakeCoins) { c.treasErr = errors.New("down") },
	} {
		r := newRig()
		mutate(r.coins)
		q, err := r.svc.Quote(ctx(), QuoteInput{User: user, OfficeID: officeID, WalletAddress: userWall})
		if err != nil || q.CanPlace || !hasProblem(q, CodePaymentsPaused) {
			t.Fatalf("%s: quote = %+v, %v", name, q, err)
		}
		if _, err := r.svc.Place(ctx(), placeInput()); !errors.Is(err, ErrPaymentsPaused) {
			t.Fatalf("%s: place err = %v", name, err)
		}
		if len(r.coins.pays) != 0 || len(r.store.created) != 0 {
			t.Fatalf("%s: paid or created while paused", name)
		}
	}
}

func TestPaymentReplyMismatchNotConfirmed(t *testing.T) {
	cases := map[string]func(*coins.Payment){
		"other payee":     func(p *coins.Payment) { p.ToAddress = "0xattacker" },
		"other amount":    func(p *coins.Payment) { p.Amount = "1" },
		"other reference": func(p *coins.Payment) { p.Reference = "swag:ord:someone-else" },
	}
	for name, mutate := range cases {
		r := newRig()
		r.coins.payReply = func(req coins.PayRequest) (*coins.Payment, error) {
			p := &coins.Payment{Reference: req.Reference, FromAddress: req.FromAddress, ToAddress: req.ToAddress, Amount: req.Amount}
			mutate(p)
			return p, nil
		}
		o, err := r.svc.Place(ctx(), placeInput())
		if !errors.Is(err, ErrPaymentMismatch) {
			t.Errorf("%s: err = %v", name, err)
		}
		f, failed := r.store.failed[orderUUID]
		if !failed || f.Attention != AttentionPaymentMismatch || f.Notify || len(r.store.confirmed) != 0 {
			t.Errorf("%s: failure = %+v failed=%v confirmed=%v", name, f, failed, r.store.confirmed)
		}
		if o == nil || o.Status != orders.Failed {
			t.Errorf("%s: order = %+v", name, o)
		}
	}
}

func TestPlaceReplaysExistingKey(t *testing.T) {
	r := newRig()
	r.store.byKey = &repository.Order{ID: "old", OrderNo: "SS-10001", Status: orders.Confirmed}
	o, err := r.svc.Place(ctx(), placeInput())
	if err != nil || o.ID != "old" || len(r.coins.pays) != 0 || len(r.store.created) != 0 {
		t.Fatalf("replay = %+v, %v (pays %d)", o, err, len(r.coins.pays))
	}
}

func TestPlaceQuoteChanged(t *testing.T) {
	r := newRig()
	r.store.items[0].Free = 1
	if _, err := r.svc.Place(ctx(), placeInput()); !errors.Is(err, ErrQuoteChanged) {
		t.Fatalf("err = %v", err)
	}
	r = newRig()
	in := placeInput()
	want := int64(170)
	in.ExpectedTotal = &want
	if _, err := r.svc.Place(ctx(), in); !errors.Is(err, ErrQuoteChanged) {
		t.Fatalf("expected-total err = %v", err)
	}
	if len(r.coins.pays) != 0 {
		t.Fatal("paid after a quote change")
	}
}

func TestPlaceOfficeInactive(t *testing.T) {
	r := newRig()
	r.store.office.Active = false
	if _, err := r.svc.Place(ctx(), placeInput()); !errors.Is(err, ErrQuoteChanged) {
		t.Fatalf("err = %v", err)
	}
}

// --- retry and reconcile ---

func TestRetryPaymentReusesReference(t *testing.T) {
	r := newRig()
	r.coins.payReply = func(coins.PayRequest) (*coins.Payment, error) {
		return nil, &apierror.Error{StatusCode: http.StatusBadGateway}
	}
	o, _ := r.svc.Place(ctx(), placeInput())
	if o.Status != orders.PendingPayment {
		t.Fatalf("status = %s", o.Status)
	}
	r.coins.payReply = newFakeCoins().payReply
	o, err := r.svc.RetryPayment(ctx(), user, o.OrderNo)
	if err != nil || o.Status != orders.Confirmed {
		t.Fatalf("retry = %+v, %v", o, err)
	}
	if len(r.coins.pays) != 2 || r.coins.pays[0].Reference != r.coins.pays[1].Reference {
		t.Fatalf("pays = %+v", r.coins.pays)
	}
	if _, err := r.svc.RetryPayment(ctx(), user, o.OrderNo); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("retry of a confirmed order: %v", err)
	}
	if _, err := r.svc.RetryPayment(ctx(), user, "SS-99999"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("retry of unknown order: %v", err)
	}
}

func TestReconcilePending(t *testing.T) {
	base := time.Unix(1_800_000_000, 0)
	order := func(age time.Duration) *repository.Order {
		return &repository.Order{ID: orderUUID, Status: orders.PendingPayment, Total: 180,
			PaymentReference: orders.PaymentReference(orderUUID), PlacedAt: base.Add(-age)}
	}
	match := &coins.Transaction{Reference: orders.PaymentReference(orderUUID), ToAddress: pin, Amount: "180"}

	r := newRig()
	r.svc.now = func() time.Time { return base }
	r.coins.tx = match
	if res, err := r.svc.ReconcilePending(context.Background(), order(2*time.Minute)); err != nil || res != "confirmed" || len(r.store.confirmed) != 1 {
		t.Fatalf("found: %s %v %v", res, err, r.store.confirmed)
	}

	r = newRig()
	r.svc.now = func() time.Time { return base }
	if res, _ := r.svc.ReconcilePending(context.Background(), order(5*time.Minute)); res != "pending" || len(r.store.bumped) != 1 {
		t.Fatalf("missing early: %s bumped=%v", res, r.store.bumped)
	}

	r = newRig()
	r.svc.now = func() time.Time { return base }
	res, _ := r.svc.ReconcilePending(context.Background(), order(16*time.Minute))
	if f := r.store.failed[orderUUID]; res != "failed" || f.Code != CodePaymentNotFound || !f.Notify {
		t.Fatalf("missing late: %s %+v", res, f)
	}

	r = newRig()
	r.svc.now = func() time.Time { return base }
	r.coins.tx = &coins.Transaction{Reference: match.Reference, ToAddress: "0xelsewhere", Amount: "180"}
	res, _ = r.svc.ReconcilePending(context.Background(), order(2*time.Minute))
	if f := r.store.failed[orderUUID]; res != "failed" || f.Attention != AttentionPaymentMismatch {
		t.Fatalf("mismatch: %s %+v", res, f)
	}

	r = newRig()
	r.coins.txErr = errors.New("down")
	if _, err := r.svc.ReconcilePending(context.Background(), order(20*time.Minute)); err == nil || len(r.store.failed) != 0 {
		t.Fatal("an outage failed the order")
	}
}

func TestPaidOrderFailedMeanwhileIsConfirmedLate(t *testing.T) {
	r := newRig()
	r.store.confirmLost = true // the reconciler failed the order while the payment was in flight
	o, err := r.svc.Place(ctx(), placeInput())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(r.store.late) != 1 || o.Status != orders.Confirmed {
		t.Fatalf("late confirms %v, status %s", r.store.late, o.Status)
	}
}

func TestFailLostToConfirmReportsStoredState(t *testing.T) {
	r := newRig()
	r.store.failLost = true
	r.coins.payReply = func(coins.PayRequest) (*coins.Payment, error) {
		return nil, &apierror.Error{StatusCode: 400, Body: `{"message":"Insufficient funds."}`}
	}
	o, err := r.svc.Place(ctx(), placeInput())
	if err != nil || o.Status != orders.Confirmed {
		t.Fatalf("lost fail CAS: %+v %v", o, err)
	}
}

func TestTreasuryCheckIgnoresCallerCancellation(t *testing.T) {
	seen := make(chan error, 1)
	lookup := lookupFunc(func(c context.Context) (string, error) { seen <- c.Err(); return pin, nil })
	g := NewTreasuryGuard(pin, lookup)
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !g.Allow(cctx) || <-seen != nil {
		t.Fatal("a cancelled caller context reached the treasury lookup")
	}
}

type lookupFunc func(context.Context) (string, error)

func (f lookupFunc) Treasury(c context.Context) (string, error) { return f(c) }
