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
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/catalog"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/checkout"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/orders"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/repository"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/reqctx"
)

// Checkout is the quote and order flow (checkout.Service).
type Checkout interface {
	Quote(ctx context.Context, in checkout.QuoteInput) (*checkout.Quote, error)
	Place(ctx context.Context, in checkout.PlaceInput) (*repository.Order, error)
	RetryPayment(ctx context.Context, u *reqctx.User, orderNo string) (*repository.Order, error)
}

// OrderReader reads orders.
type OrderReader interface {
	OrdersForUser(ctx context.Context, email string, limit, offset int) ([]repository.Order, int, error)
	OrderDetail(ctx context.Context, orderNo, email string) (*repository.OrderDetail, error)
}

// OrdersHandler serves quotes and the caller's own orders.
type OrdersHandler struct {
	checkout Checkout
	orders   OrderReader
}

// NewOrdersHandler builds the handler.
func NewOrdersHandler(c Checkout, o OrderReader) *OrdersHandler {
	return &OrdersHandler{checkout: c, orders: o}
}

var orderNoRe = regexp.MustCompile(`^SS-[0-9]{5,12}$`)

func orderNoParam(c *gin.Context) (string, bool) {
	no := strings.ToUpper(c.Param("orderNo"))
	if !orderNoRe.MatchString(no) {
		writeError(c, http.StatusNotFound, ErrMsgNotFound)
		return "", false
	}
	return no, true
}

// checkoutBody is the body of POST /quotes and POST /orders. It has no payee
// field, and the strict decoder refuses one (backend-spec §8.7).
type checkoutBody struct {
	PickupOfficeID string `json:"pickupOfficeId"`
	WalletAddress  string `json:"walletAddress"`
	ExpectedTotal  *int64 `json:"expectedTotal"`
}

func (b checkoutBody) validate() error {
	if !uuidRe.MatchString(b.PickupOfficeID) {
		return errors.New("pickupOfficeId must be a UUID.")
	}
	if w := strings.TrimSpace(b.WalletAddress); w == "" || len(w) > 128 {
		return errors.New("walletAddress is required.")
	}
	if b.ExpectedTotal != nil && *b.ExpectedTotal <= 0 {
		return errors.New("expectedTotal must be above 0.")
	}
	return nil
}

type quoteLineJSON struct {
	LineID    string            `json:"lineId"`
	Name      string            `json:"name"`
	Options   string            `json:"options"`
	UnitPrice int64             `json:"unitPrice"`
	Quantity  int               `json:"quantity"`
	State     catalog.LineState `json:"state"`
	Reason    *string           `json:"reason"`
}

type quoteOfficeJSON struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	LeadTimeDays int    `json:"leadTimeDays"`
}

type quoteWalletJSON struct {
	WalletAddress string `json:"walletAddress"`
	Balance       string `json:"balance"`
}

type quoteJSON struct {
	Lines        []quoteLineJSON    `json:"lines"`
	Total        int64              `json:"total"`
	PickupOffice *quoteOfficeJSON   `json:"pickupOffice"`
	Wallet       *quoteWalletJSON   `json:"wallet"`
	CanPlace     bool               `json:"canPlace"`
	Problems     []checkout.Problem `json:"problems"`
}

// Quote prices the cart. Nothing is stored and nothing is paid.
func (h *OrdersHandler) Quote(c *gin.Context) {
	var b checkoutBody
	if !readJSON(c, &b) {
		return
	}
	u := reqctx.UserFrom(c.Request.Context())
	q, err := h.checkout.Quote(c.Request.Context(), checkout.QuoteInput{User: u, OfficeID: strings.ToLower(b.PickupOfficeID), WalletAddress: strings.TrimSpace(b.WalletAddress)})
	if err != nil {
		h.writeCheckoutError(c, err, nil)
		return
	}
	out := quoteJSON{Lines: make([]quoteLineJSON, 0, len(q.Lines)), Total: q.Total, CanPlace: q.CanPlace, Problems: q.Problems}
	for _, l := range q.Lines {
		out.Lines = append(out.Lines, quoteLineJSON{LineID: l.Item.LineID, Name: l.Item.Name, Options: l.Item.Options,
			UnitPrice: l.Item.UnitPrice, Quantity: l.Item.Quantity, State: l.State, Reason: l.Reason})
	}
	if q.Office != nil {
		out.PickupOffice = &quoteOfficeJSON{ID: q.Office.ID, Name: q.Office.Name, LeadTimeDays: q.Office.LeadTimeDays}
	}
	if q.Wallet != nil {
		out.Wallet = &quoteWalletJSON{WalletAddress: q.Wallet.Address, Balance: q.Wallet.Balance}
	}
	c.Header("Cache-Control", "no-store")
	writeJSON(c, http.StatusOK, out)
}

type placedJSON struct {
	ID           string          `json:"id"`
	OrderNo      string          `json:"orderNo"`
	Status       orders.Status   `json:"status"`
	Total        int64           `json:"total"`
	PickupOffice placedOfficeRef `json:"pickupOffice"`
	PlacedAt     time.Time       `json:"placedAt"`
}

type placedOfficeRef struct {
	Name string `json:"name"`
}

// Place runs POST /orders: 201 CONFIRMED, 202 PENDING_PAYMENT, or an error.
func (h *OrdersHandler) Place(c *gin.Context) {
	key := strings.ToLower(strings.TrimSpace(c.GetHeader("Idempotency-Key")))
	if !uuidRe.MatchString(key) {
		writeError(c, http.StatusBadRequest, ErrMsgIdempotencyMissing)
		return
	}
	var b checkoutBody
	if !readJSON(c, &b) {
		return
	}
	u := reqctx.UserFrom(c.Request.Context())
	o, err := h.checkout.Place(c.Request.Context(), checkout.PlaceInput{User: u, OfficeID: strings.ToLower(b.PickupOfficeID),
		WalletAddress: strings.TrimSpace(b.WalletAddress), IdempotencyKey: key, ExpectedTotal: b.ExpectedTotal})
	h.writePlaced(c, o, err)
}

// RetryPayment pays a PENDING_PAYMENT order again with the same reference.
func (h *OrdersHandler) RetryPayment(c *gin.Context) {
	no, ok := orderNoParam(c)
	if !ok {
		return
	}
	u := reqctx.UserFrom(c.Request.Context())
	o, err := h.checkout.RetryPayment(c.Request.Context(), u, no)
	h.writePlaced(c, o, err)
}

func (h *OrdersHandler) writePlaced(c *gin.Context, o *repository.Order, err error) {
	if err != nil {
		h.writeCheckoutError(c, err, o)
		return
	}
	switch o.Status {
	case orders.PendingPayment:
		writeJSON(c, http.StatusAccepted, placed(o))
	case orders.Failed:
		// A replayed key for an order that failed answers with its failure.
		h.writeCheckoutError(c, failureError(o.FailureCode), o)
	default:
		writeJSON(c, http.StatusCreated, placed(o))
	}
}

func placed(o *repository.Order) placedJSON {
	return placedJSON{ID: o.ID, OrderNo: o.OrderNo, Status: o.Status, Total: o.Total,
		PickupOffice: placedOfficeRef{Name: o.OfficeName}, PlacedAt: o.PlacedAt.UTC()}
}

func failureError(code string) error {
	switch code {
	case checkout.CodeInsufficientFunds:
		return checkout.ErrInsufficientFunds
	case checkout.CodeWalletNotOwned:
		return checkout.ErrWalletNotOwned
	}
	return checkout.ErrPaymentFailed
}

func (h *OrdersHandler) writeCheckoutError(c *gin.Context, err error, _ *repository.Order) {
	switch {
	case errors.Is(err, checkout.ErrHRUnavailable):
		writeErrorCode(c, http.StatusServiceUnavailable, ErrMsgHRUnavailable, CodeHRUnavailable)
	case errors.Is(err, checkout.ErrCoinsUnavailable):
		writeErrorCode(c, http.StatusServiceUnavailable, ErrMsgCoinsUnavailable, CodeCoinsUnavailable)
	case errors.Is(err, checkout.ErrNotActiveEmployee):
		writeErrorCode(c, http.StatusForbidden, ErrMsgNotActiveEmployee, CodeNotActiveEmployee)
	case errors.Is(err, checkout.ErrPaymentsPaused):
		writeErrorCode(c, http.StatusServiceUnavailable, ErrMsgPaymentsPaused, CodePaymentsPaused)
	case errors.Is(err, checkout.ErrQuoteChanged):
		writeErrorCode(c, http.StatusConflict, ErrMsgQuoteChanged, CodeQuoteChanged)
	case errors.Is(err, checkout.ErrInsufficientFunds):
		writeErrorCode(c, http.StatusPaymentRequired, ErrMsgInsufficientFunds, CodeInsufficientFunds)
	case errors.Is(err, checkout.ErrWalletNotOwned):
		writeErrorCode(c, http.StatusForbidden, ErrMsgWalletNotOwned, CodeWalletNotOwned)
	case errors.Is(err, checkout.ErrPaymentFailed), errors.Is(err, checkout.ErrPaymentMismatch):
		writeErrorCode(c, http.StatusPaymentRequired, ErrMsgPaymentFailed, CodePaymentFailed)
	case errors.Is(err, checkout.ErrStateConflict):
		writeErrorCode(c, http.StatusConflict, ErrMsgStateConflict, CodeStateConflict)
	case errors.Is(err, repository.ErrNotFound):
		writeError(c, http.StatusNotFound, ErrMsgNotFound)
	default:
		slog.ErrorContext(c.Request.Context(), "checkout failed", "err", err)
		writeError(c, http.StatusInternalServerError, ErrMsgInternal)
	}
}

type orderSummaryJSON struct {
	ID          string        `json:"id"`
	OrderNo     string        `json:"orderNo"`
	Status      orders.Status `json:"status"`
	Total       int64         `json:"total"`
	OfficeName  string        `json:"officeName"`
	PlacedAt    time.Time     `json:"placedAt"`
	FailureCode *string       `json:"failureCode"`
}

func summary(o repository.Order) orderSummaryJSON {
	return orderSummaryJSON{ID: o.ID, OrderNo: o.OrderNo, Status: o.Status, Total: o.Total, OfficeName: o.OfficeName,
		PlacedAt: o.PlacedAt.UTC(), FailureCode: strPtr(o.FailureCode)}
}

// List returns the caller's orders, newest first.
func (h *OrdersHandler) List(c *gin.Context) {
	limit, offset, ok := paging(c)
	if !ok {
		return
	}
	u := reqctx.UserFrom(c.Request.Context())
	list, total, err := h.orders.OrdersForUser(c.Request.Context(), u.Email, limit, offset)
	if err != nil {
		writeInternal(c, "orders: list failed", err)
		return
	}
	items := make([]orderSummaryJSON, 0, len(list))
	for _, o := range list {
		items = append(items, summary(o))
	}
	writeJSON(c, http.StatusOK, gin.H{"items": items, "total": total})
}

type orderLineJSON struct {
	Name      string `json:"name"`
	Options   string `json:"options"`
	SKU       string `json:"sku"`
	UnitPrice int64  `json:"unitPrice"`
	Quantity  int    `json:"quantity"`
}

type timelineJSON struct {
	From *string   `json:"from"`
	To   string    `json:"to"`
	Note *string   `json:"note"`
	At   time.Time `json:"at"`
}

type officeJSON struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Address         string `json:"address"`
	CollectionPoint string `json:"collectionPoint"`
	Hours           string `json:"hours"`
	LeadTimeDays    int    `json:"leadTimeDays"`
}

type orderDetailJSON struct {
	orderSummaryJSON
	Lines       []orderLineJSON `json:"lines"`
	Timeline    []timelineJSON  `json:"timeline"`
	Office      *officeJSON     `json:"pickupOffice"`
	ConfirmedAt *time.Time      `json:"confirmedAt"`
	ReadyAt     *time.Time      `json:"readyAt"`
	CollectedAt *time.Time      `json:"collectedAt"`
}

func detailJSON(d *repository.OrderDetail) orderDetailJSON {
	out := orderDetailJSON{orderSummaryJSON: summary(d.Order), Lines: []orderLineJSON{}, Timeline: []timelineJSON{},
		ConfirmedAt: d.Order.ConfirmedAt, ReadyAt: d.Order.ReadyAt, CollectedAt: d.Order.CollectedAt}
	for _, l := range d.Lines {
		out.Lines = append(out.Lines, orderLineJSON{Name: l.Name, Options: l.Options, SKU: l.SKU, UnitPrice: l.UnitPrice, Quantity: l.Quantity})
	}
	for _, e := range d.Events {
		out.Timeline = append(out.Timeline, timelineJSON{From: strPtr(e.From), To: e.To, Note: strPtr(e.Note), At: e.CreatedAt.UTC()})
	}
	if d.Office != nil {
		out.Office = &officeJSON{ID: d.Office.ID, Name: d.Office.Name, Address: d.Office.Address,
			CollectionPoint: d.Office.CollectionPoint, Hours: d.Office.Hours, LeadTimeDays: d.Office.LeadTimeDays}
	}
	return out
}

// Get returns one of the caller's orders. Another user's order is 404.
func (h *OrdersHandler) Get(c *gin.Context) {
	no, ok := orderNoParam(c)
	if !ok {
		return
	}
	u := reqctx.UserFrom(c.Request.Context())
	d, err := h.orders.OrderDetail(c.Request.Context(), no, u.Email)
	if errors.Is(err, repository.ErrNotFound) {
		writeError(c, http.StatusNotFound, ErrMsgNotFound)
		return
	}
	if err != nil {
		writeInternal(c, "orders: read failed", err)
		return
	}
	writeJSON(c, http.StatusOK, detailJSON(d))
}
