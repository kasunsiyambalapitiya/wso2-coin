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

// Package checkout is the quote and place-order orchestration. It is the only
// place that crosses the database and transaction-service in one flow, and
// payRequest is the only code that builds a payment.
package checkout

import (
	"context"
	"errors"
	"log/slog"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/apierror"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/catalog"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/coins"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/hr"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/orders"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/repository"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/reqctx"
)

// paymentSource is the source token transaction-service records.
const paymentSource = "SWAG_STORE"

// pendingGiveUp is how long the reconciler waits for a missing payment.
const pendingGiveUp = 15 * time.Minute

// Problem and failure codes. They match the handler's errorCode values.
const (
	CodeInsufficientFunds = "insufficient_funds"
	CodeNotActiveEmployee = "not_active_employee"
	CodeWalletNotOwned    = "wallet_not_owned"
	CodePaymentsPaused    = "payments_paused"
	CodePaymentFailed     = "payment_failed"
	CodeOfficeUnavailable = "office_unavailable"
	CodeEmptyCart         = "empty_cart"
	CodeLinesUnavailable  = "lines_unavailable"
	CodePaymentNotFound   = "payment_not_found"
	CodePaymentMismatch   = "payment_mismatch"

	// AttentionPaymentMismatch flags an order whose ledger entry did not match it.
	AttentionPaymentMismatch = "payment_mismatch"
)

// Errors returned to handlers.
var (
	ErrHRUnavailable     = errors.New("checkout: HR unavailable")
	ErrCoinsUnavailable  = errors.New("checkout: transaction-service unavailable")
	ErrNotActiveEmployee = errors.New("checkout: employee status does not allow buying")
	ErrPaymentsPaused    = errors.New("checkout: payments paused by the treasury check")
	ErrQuoteChanged      = errors.New("checkout: cart no longer matches its quote")
	ErrInsufficientFunds = errors.New("checkout: insufficient funds")
	ErrWalletNotOwned    = errors.New("checkout: wallet not owned by the caller")
	ErrPaymentFailed     = errors.New("checkout: payment refused")
	ErrPaymentMismatch   = errors.New("checkout: payment reply does not match the order")
	ErrStateConflict     = errors.New("checkout: order is not awaiting payment")
)

// Store is the persistence checkout needs (repository.Store).
type Store interface {
	CartForQuote(ctx context.Context, email string) ([]repository.CartItem, error)
	Office(ctx context.Context, id string) (*repository.Office, error)
	OrderByKey(ctx context.Context, email, key string) (*repository.Order, error)
	CreatePendingOrder(ctx context.Context, n repository.NewOrder) (*repository.Order, error)
	ConfirmOrder(ctx context.Context, orderID string) (bool, error)
	FailOrder(ctx context.Context, orderID string, f repository.Failure) (bool, error)
	OrderByNoForUser(ctx context.Context, orderNo, email string) (*repository.Order, error)
	OrderByID(ctx context.Context, id string) (*repository.Order, error)
	ConfirmLatePayment(ctx context.Context, orderID string) error
	BumpPaymentChecks(ctx context.Context, orderID string) error
}

// Coins is the part of transaction-service checkout uses (coins.Client).
type Coins interface {
	Wallets(ctx context.Context) ([]coins.Wallet, error)
	Pay(ctx context.Context, req coins.PayRequest) (*coins.Payment, error)
	Transaction(ctx context.Context, reference string) (*coins.Transaction, error)
}

// Employees reads HR status (hr.Cache).
type Employees interface {
	Lookup(ctx context.Context, email string) (*hr.Employee, error)
}

// Service runs quotes, orders, payment retries and payment reconciliation.
type Service struct {
	store    Store
	coins    Coins
	hr       Employees
	treasury *TreasuryGuard
	now      func() time.Time
	newID    func() string
}

// NewService wires the service.
func NewService(store Store, c Coins, employees Employees, treasury *TreasuryGuard) *Service {
	return &Service{store: store, coins: c, hr: employees, treasury: treasury, now: time.Now,
		newID: func() string { return uuid.Must(uuid.NewV7()).String() }}
}

// Treasury exposes the guard (health, reconciler).
func (s *Service) Treasury() *TreasuryGuard { return s.treasury }

// QuoteInput is the body of POST /quotes.
type QuoteInput struct {
	User          *reqctx.User
	OfficeID      string
	WalletAddress string
}

// QuoteLine is one priced line.
type QuoteLine struct {
	Item   repository.CartItem
	State  catalog.LineState
	Reason *string
}

// Problem is a reason the order cannot be placed yet.
type Problem struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// WalletView is the chosen wallet in a quote.
type WalletView struct {
	Address string
	Balance string
}

// Quote is the result of POST /quotes. Nothing is stored.
type Quote struct {
	Lines    []QuoteLine
	Total    int64
	Office   *repository.Office
	Wallet   *WalletView
	CanPlace bool
	Problems []Problem
}

// Facts maps cart items to the line rules' input.
func Facts(items []repository.CartItem) []catalog.LineFacts {
	facts := make([]catalog.LineFacts, 0, len(items))
	for _, it := range items {
		facts = append(facts, catalog.LineFacts{ProductID: it.ProductID,
			ProductName: it.Name, Options: it.Options, ProductStatus: it.ProductStatus,
			VariantActive: it.VariantActive, Free: it.Free, Quantity: it.Quantity,
			MaxPerUser: it.MaxPerUser, PaidUnits: it.PaidUnits,
		})
	}
	return facts
}

func evaluate(items []repository.CartItem) ([]QuoteLine, int64, bool) {
	lines := make([]QuoteLine, 0, len(items))
	var total int64
	allOK := true
	results := catalog.CheckLines(Facts(items))
	for i, it := range items {
		state, reason := results[i].State, results[i].Reason
		l := QuoteLine{Item: it, State: state}
		if state == catalog.LineOK {
			total += it.UnitPrice * int64(it.Quantity)
		} else {
			allOK = false
			r := reason
			l.Reason = &r
		}
		lines = append(lines, l)
	}
	return lines, total, allOK
}

// Quote prices the cart for an office and wallet. HR failure is an error
// (fail closed); every other blocker is a problem with canPlace false.
func (s *Service) Quote(ctx context.Context, in QuoteInput) (*Quote, error) {
	emp, err := s.hr.Lookup(ctx, in.User.Email)
	if err != nil {
		slog.ErrorContext(ctx, "quote: HR lookup failed", "err", err)
		return nil, ErrHRUnavailable
	}
	items, err := s.store.CartForQuote(ctx, in.User.Email)
	if err != nil {
		return nil, err
	}
	office, err := s.store.Office(ctx, in.OfficeID)
	if err != nil {
		return nil, err
	}
	wallets, err := s.coins.Wallets(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "quote: wallets failed", "err", err)
		return nil, ErrCoinsUnavailable
	}

	lines, total, allOK := evaluate(items)
	q := &Quote{Lines: lines, Total: total, Problems: []Problem{}}
	add := func(code, msg string) { q.Problems = append(q.Problems, Problem{Code: code, Message: msg}) }

	if total <= 0 {
		add(CodeEmptyCart, "There is nothing in your cart that can be ordered.")
	} else if !allOK {
		add(CodeLinesUnavailable, "Remove or change the items marked in your cart.")
	}
	if office == nil || !office.Active {
		add(CodeOfficeUnavailable, "Choose an open pickup office.")
	} else {
		q.Office = office
	}
	if !hr.CanBuy(emp) {
		add(CodeNotActiveEmployee, "Only active employees can place orders.")
	}
	var wallet *coins.Wallet
	for i := range wallets {
		if wallets[i].Address == in.WalletAddress {
			wallet = &wallets[i]
		}
	}
	if wallet == nil {
		add(CodeWalletNotOwned, "Choose one of your wallets.")
	} else {
		q.Wallet = &WalletView{Address: wallet.Address, Balance: wallet.BalanceText}
		if wallet.Balance.Cmp(new(big.Rat).SetInt64(total)) < 0 {
			add(CodeInsufficientFunds, "Your wallet balance is below the order total.")
		}
	}
	if !s.treasury.Allow(ctx) {
		add(CodePaymentsPaused, "Payments are paused for a moment. Your coins are safe. Try again later.")
	}
	q.CanPlace = len(q.Problems) == 0
	return q, nil
}

// PlaceInput is the body of POST /orders plus its Idempotency-Key.
type PlaceInput struct {
	User           *reqctx.User
	OfficeID       string
	WalletAddress  string
	IdempotencyKey string
	ExpectedTotal  *int64 // the total the shopper confirmed; a difference is quote_changed
}

// Place runs backend-spec §8.3. It returns the order as it stands (CONFIRMED,
// PENDING_PAYMENT or FAILED) and, for a refusal, the matching error.
func (s *Service) Place(ctx context.Context, in PlaceInput) (*repository.Order, error) {
	if o, err := s.store.OrderByKey(ctx, in.User.Email, in.IdempotencyKey); err != nil {
		return nil, err
	} else if o != nil {
		return o, nil
	}
	emp, err := s.hr.Lookup(ctx, in.User.Email)
	if err != nil {
		slog.ErrorContext(ctx, "order: HR lookup failed", "err", err)
		return nil, ErrHRUnavailable
	}
	if !hr.CanBuy(emp) {
		return nil, ErrNotActiveEmployee
	}
	office, err := s.store.Office(ctx, in.OfficeID)
	if err != nil {
		return nil, err
	}
	if office == nil || !office.Active {
		return nil, ErrQuoteChanged
	}
	if !s.treasury.Allow(ctx) {
		return nil, ErrPaymentsPaused
	}

	order, err := s.store.CreatePendingOrder(ctx, repository.NewOrder{
		ID:             s.newID(),
		UserEmail:      in.User.Email,
		UserID:         in.User.UserID,
		UserName:       strings.TrimSpace(in.User.FirstName + " " + in.User.LastName),
		OfficeID:       office.ID,
		OfficeName:     office.Name,
		WalletAddress:  in.WalletAddress,
		IdempotencyKey: in.IdempotencyKey,
		Check: func(items []repository.CartItem) (int64, error) {
			_, total, allOK := evaluate(items)
			if !allOK || total <= 0 {
				return 0, ErrQuoteChanged
			}
			if in.ExpectedTotal != nil && *in.ExpectedTotal != total {
				return 0, ErrQuoteChanged
			}
			return total, nil
		},
	})
	switch {
	case errors.Is(err, repository.ErrDuplicateKey):
		o, err := s.store.OrderByKey(ctx, in.User.Email, in.IdempotencyKey)
		if err != nil {
			return nil, err
		}
		if o == nil {
			return nil, ErrQuoteChanged
		}
		return o, nil
	case errors.Is(err, repository.ErrCartEmpty), errors.Is(err, repository.ErrOutOfStock),
		errors.Is(err, repository.ErrNotAvailable):
		return nil, ErrQuoteChanged
	case err != nil:
		return nil, err
	}
	return s.pay(ctx, order)
}

// RetryPayment pays a PENDING_PAYMENT order again with the same reference.
func (s *Service) RetryPayment(ctx context.Context, u *reqctx.User, orderNo string) (*repository.Order, error) {
	order, err := s.store.OrderByNoForUser(ctx, orderNo, u.Email)
	if err != nil {
		return nil, err
	}
	if order == nil {
		return nil, repository.ErrNotFound
	}
	if order.Status != orders.PendingPayment {
		return order, ErrStateConflict
	}
	if !s.treasury.Allow(ctx) {
		return order, ErrPaymentsPaused
	}
	return s.pay(ctx, order)
}

// payRequest is the only place a payment is built. toAddress is always the
// pinned treasury; nothing from a request can reach it.
func (s *Service) payRequest(o *repository.Order) coins.PayRequest {
	return coins.PayRequest{
		FromAddress: o.WalletAddress,
		ToAddress:   s.treasury.Pin(),
		Amount:      strconv.FormatInt(o.Total, 10),
		Reference:   orders.PaymentReference(o.ID),
		Source:      paymentSource,
	}
}

// pay sends the payment and settles the order from the reply.
func (s *Service) pay(ctx context.Context, o *repository.Order) (*repository.Order, error) {
	reply, err := s.coins.Pay(ctx, s.payRequest(o))
	if err != nil {
		return s.settleRefusal(ctx, o, err)
	}
	if !s.matches(o, reply.ToAddress, reply.Amount, reply.Reference) {
		slog.ErrorContext(ctx, "payment_outcome", "result", "mismatch", "orderNo", o.OrderNo,
			"replyToAddress", reply.ToAddress, "replyAmount", reply.Amount)
		return s.fail(ctx, o, repository.Failure{Code: CodePaymentMismatch, Attention: AttentionPaymentMismatch}, ErrPaymentMismatch)
	}
	done, err := s.store.ConfirmOrder(ctx, o.ID)
	if err != nil {
		return nil, err
	}
	if !done {
		return s.confirmLost(ctx, o)
	}
	o.Status = orders.Confirmed
	slog.InfoContext(ctx, "payment_outcome", "result", "confirmed", "orderNo", o.OrderNo, "replayed", reply.Replayed)
	return o, nil
}

// confirmLost handles a matching payment whose confirm CAS lost: another path
// (the reconciler) settled the order first. If it failed the order, the coins
// were still taken, so the late-payment path confirms it now (or flags it when
// the stock is gone). The response is the stored state.
func (s *Service) confirmLost(ctx context.Context, o *repository.Order) (*repository.Order, error) {
	cur, err := s.store.OrderByID(ctx, o.ID)
	if err != nil || cur == nil {
		return nil, errors.Join(err, repository.ErrNotFound)
	}
	if cur.Status == orders.Failed {
		slog.WarnContext(ctx, "payment_outcome", "result", "late_confirm", "orderNo", o.OrderNo)
		if err := s.store.ConfirmLatePayment(ctx, o.ID); err != nil && !errors.Is(err, repository.ErrNoStockForLatePayment) {
			return nil, err
		}
		if cur, err = s.store.OrderByID(ctx, o.ID); err != nil || cur == nil {
			return nil, errors.Join(err, repository.ErrNotFound)
		}
	}
	if cur.Status == orders.Failed {
		return cur, ErrPaymentFailed
	}
	return cur, nil
}

// settleRefusal maps a payment error (backend-spec §8.3). Timeouts, 5xx,
// network errors and a 401 leave the order pending for the reconciler.
func (s *Service) settleRefusal(ctx context.Context, o *repository.Order, err error) (*repository.Order, error) {
	status := apierror.Status(err)
	switch {
	case coins.IsInsufficientFunds(err):
		return s.fail(ctx, o, repository.Failure{Code: CodeInsufficientFunds, Notify: true}, ErrInsufficientFunds)
	case status == http.StatusForbidden:
		return s.fail(ctx, o, repository.Failure{Code: CodeWalletNotOwned, Notify: true}, ErrWalletNotOwned)
	case status == http.StatusConflict:
		slog.ErrorContext(ctx, "payment refused with 409; the reference was reused with other details", "orderNo", o.OrderNo, "err", err)
		return s.fail(ctx, o, repository.Failure{Code: CodePaymentFailed, Notify: true}, ErrPaymentFailed)
	case status >= 400 && status < 500 && status != http.StatusUnauthorized && status != http.StatusRequestTimeout && status != http.StatusTooManyRequests:
		slog.ErrorContext(ctx, "payment refused", "orderNo", o.OrderNo, "err", err)
		return s.fail(ctx, o, repository.Failure{Code: CodePaymentFailed, Notify: true}, ErrPaymentFailed)
	}
	slog.WarnContext(ctx, "payment_outcome", "result", "pending", "orderNo", o.OrderNo, "err", err)
	return o, nil
}

func (s *Service) fail(ctx context.Context, o *repository.Order, f repository.Failure, cause error) (*repository.Order, error) {
	done, err := s.store.FailOrder(ctx, o.ID, f)
	if err != nil {
		return nil, err
	}
	if !done {
		// Another path settled the order first; report what is stored.
		cur, err := s.store.OrderByID(ctx, o.ID)
		if err != nil || cur == nil {
			return nil, errors.Join(err, repository.ErrNotFound)
		}
		if cur.Status == orders.Failed {
			return cur, cause
		}
		return cur, nil
	}
	o.Status, o.FailureCode, o.AttentionReason = orders.Failed, f.Code, f.Attention
	slog.InfoContext(ctx, "payment_outcome", "result", "failed", "orderNo", o.OrderNo, "code", f.Code)
	return o, cause
}

// matches is the reply check: payee is the pin, amount is the order total and
// the reference is the order's own.
func (s *Service) matches(o *repository.Order, to, amount, reference string) bool {
	return s.treasury.IsPin(to) && AmountEquals(amount, o.Total) && reference == orders.PaymentReference(o.ID)
}

// AmountEquals compares a decimal string with whole coins exactly.
func AmountEquals(amount string, total int64) bool {
	r, ok := new(big.Rat).SetString(strings.TrimSpace(amount))
	return ok && r.Cmp(new(big.Rat).SetInt64(total)) == 0
}

// ReconcilePending settles one PENDING_PAYMENT order from the ledger: found
// and matching → confirmed; found but not matching → failed with attention;
// missing after 15 minutes → failed; otherwise pending. An outage is an error
// and changes nothing.
func (s *Service) ReconcilePending(ctx context.Context, o *repository.Order) (string, error) {
	tx, err := s.coins.Transaction(ctx, orders.PaymentReference(o.ID))
	if err != nil {
		return "", err
	}
	if tx != nil {
		if !s.matches(o, tx.ToAddress, tx.Amount, tx.Reference) {
			_, err := s.fail(ctx, o, repository.Failure{Code: CodePaymentMismatch, Attention: AttentionPaymentMismatch}, nil)
			return "failed", err
		}
		if _, err := s.store.ConfirmOrder(ctx, o.ID); err != nil {
			return "", err
		}
		return "confirmed", nil
	}
	if s.now().Sub(o.PlacedAt) >= pendingGiveUp {
		_, err := s.fail(ctx, o, repository.Failure{Code: CodePaymentNotFound, Notify: true}, nil)
		return "failed", err
	}
	return "pending", s.store.BumpPaymentChecks(ctx, o.ID)
}
