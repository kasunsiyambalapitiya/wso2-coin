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
	"encoding/csv"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/orders"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/repository"
)

// csvExportLimit caps one CSV export.
const csvExportLimit = 5000

// AdminOrderStore is Fulfilment Ops' persistence.
type AdminOrderStore interface {
	SearchOrders(ctx context.Context, f repository.OrderFilter) ([]repository.Order, int, error)
	OrderDetail(ctx context.Context, orderNo, email string) (*repository.OrderDetail, error)
	TransitionOrder(ctx context.Context, orderNo string, to orders.Status, a repository.Actor, note string) (*repository.Order, error)
	AddOrderNote(ctx context.Context, orderNo string, a repository.Actor, note string) error
}

// AdminOrdersHandler serves the orders board.
type AdminOrdersHandler struct{ store AdminOrderStore }

// NewAdminOrdersHandler builds the handler.
func NewAdminOrdersHandler(s AdminOrderStore) *AdminOrdersHandler {
	return &AdminOrdersHandler{store: s}
}

type searchBody struct {
	Filters struct {
		Status         string `json:"status"`
		PickupOfficeID string `json:"pickupOfficeId"`
		SearchQuery    string `json:"searchQuery"`
		From           string `json:"from"`
		To             string `json:"to"`
	} `json:"filters"`
	Pagination struct {
		Limit  int `json:"limit"`
		Offset int `json:"offset"`
	} `json:"pagination"`
}

func parseTime(s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	for _, layout := range []string{time.RFC3339, time.DateOnly} {
		if t, err := time.Parse(layout, s); err == nil {
			return &t, nil
		}
	}
	return nil, errors.New("bad time")
}

func (b searchBody) filter() (repository.OrderFilter, error) {
	f := repository.OrderFilter{Status: b.Filters.Status, OfficeID: strings.ToLower(b.Filters.PickupOfficeID),
		SearchQuery: strings.TrimSpace(b.Filters.SearchQuery), Limit: b.Pagination.Limit, Offset: b.Pagination.Offset}
	if f.Status != "" {
		if _, ok := orders.ParseStatus(f.Status); !ok {
			return f, errors.New("filters.status is not an order status.")
		}
	}
	if f.OfficeID != "" && !uuidRe.MatchString(f.OfficeID) {
		return f, errors.New("filters.pickupOfficeId must be a UUID.")
	}
	if len(f.SearchQuery) > 320 {
		return f, errors.New("filters.searchQuery is too long.")
	}
	var err error
	if f.From, err = parseTime(b.Filters.From); err != nil {
		return f, errors.New("filters.from must be a date or RFC 3339 time.")
	}
	if f.To, err = parseTime(b.Filters.To); err != nil {
		return f, errors.New("filters.to must be a date or RFC 3339 time.")
	}
	if f.Limit == 0 {
		f.Limit = 20
	}
	if f.Limit < 1 || f.Limit > 100 || f.Offset < 0 {
		return f, errors.New("pagination.limit must be 1 to 100 and offset 0 or more.")
	}
	return f, nil
}

type adminOrderJSON struct {
	orderSummaryJSON
	UserEmail       string  `json:"userEmail"`
	UserName        string  `json:"userName"`
	AttentionReason *string `json:"attentionReason"`
}

func adminOrder(o repository.Order) adminOrderJSON {
	return adminOrderJSON{orderSummaryJSON: summary(o), UserEmail: o.UserEmail, UserName: o.UserName, AttentionReason: strPtr(o.AttentionReason)}
}

// Search filters the board. With Accept: text/csv it returns the same rows as
// CSV, up to 5,000.
func (h *AdminOrdersHandler) Search(c *gin.Context) {
	var b searchBody
	if !readJSON(c, &b) {
		return
	}
	f, err := b.filter()
	if err != nil {
		writeError(c, http.StatusBadRequest, err.Error())
		return
	}
	wantCSV := strings.Contains(c.GetHeader("Accept"), "text/csv")
	if wantCSV {
		f.Limit, f.Offset = csvExportLimit, 0
	}
	list, total, err := h.store.SearchOrders(c.Request.Context(), f)
	if err != nil {
		writeInternal(c, "admin orders: search failed", err)
		return
	}
	if wantCSV {
		writeOrdersCSV(c, list)
		return
	}
	items := make([]adminOrderJSON, 0, len(list))
	for _, o := range list {
		items = append(items, adminOrder(o))
	}
	writeJSON(c, http.StatusOK, gin.H{"items": items, "total": total})
}

// csvCell stops a spreadsheet from running a cell as a formula.
func csvCell(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

func writeOrdersCSV(c *gin.Context, list []repository.Order) {
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", `attachment; filename="swag-orders.csv"`)
	c.Header("Cache-Control", "no-store")
	c.Status(http.StatusOK)
	w := csv.NewWriter(c.Writer)
	_ = w.Write([]string{"orderNo", "status", "placedAt", "email", "name", "office", "total", "attention"})
	for _, o := range list {
		_ = w.Write([]string{csvCell(o.OrderNo), string(o.Status), o.PlacedAt.UTC().Format(time.RFC3339), csvCell(o.UserEmail),
			csvCell(o.UserName), csvCell(o.OfficeName), strconv.FormatInt(o.Total, 10), csvCell(o.AttentionReason)})
	}
	w.Flush()
}

type adminOrderDetailJSON struct {
	orderDetailJSON
	UserEmail        string     `json:"userEmail"`
	UserName         string     `json:"userName"`
	AttentionReason  *string    `json:"attentionReason"`
	PaymentReference string     `json:"paymentReference"`
	CollectedBy      *string    `json:"collectedBy"`
	Notes            []noteJSON `json:"notes"`
}

type noteJSON struct {
	Note  string    `json:"note"`
	Actor string    `json:"actor"`
	At    time.Time `json:"at"`
}

// Get returns any order with its notes.
func (h *AdminOrdersHandler) Get(c *gin.Context) {
	no, ok := orderNoParam(c)
	if !ok {
		return
	}
	d, err := h.store.OrderDetail(c.Request.Context(), no, "")
	if errors.Is(err, repository.ErrNotFound) {
		writeError(c, http.StatusNotFound, ErrMsgNotFound)
		return
	}
	if err != nil {
		writeInternal(c, "admin orders: read failed", err)
		return
	}
	out := adminOrderDetailJSON{orderDetailJSON: detailJSON(d), UserEmail: d.Order.UserEmail, UserName: d.Order.UserName,
		AttentionReason: strPtr(d.Order.AttentionReason), PaymentReference: d.Order.PaymentReference,
		CollectedBy: strPtr(d.Order.CollectedBy), Notes: []noteJSON{}}
	for _, n := range d.Notes {
		out.Notes = append(out.Notes, noteJSON{Note: n.Note, Actor: n.Actor, At: n.CreatedAt.UTC()})
	}
	writeJSON(c, http.StatusOK, out)
}

type orderPatchBody struct {
	Status    string `json:"status"`
	IDChecked *bool  `json:"idChecked"`
	Note      string `json:"note"`
}

func (b orderPatchBody) validate() error {
	switch orders.Status(b.Status) {
	case orders.ReadyForPickup:
	case orders.Collected:
		if b.IDChecked == nil || !*b.IDChecked {
			return errors.New("idChecked must be true to mark an order collected.")
		}
	default:
		return errors.New("status must be READY_FOR_PICKUP or COLLECTED.")
	}
	if utf8.RuneCountInString(b.Note) > 1000 {
		return errors.New("note is at most 1,000 characters.")
	}
	return nil
}

// Patch marks an order ready for pickup or collected (with an ID check).
func (h *AdminOrdersHandler) Patch(c *gin.Context) {
	no, ok := orderNoParam(c)
	if !ok {
		return
	}
	var b orderPatchBody
	if !readJSON(c, &b) {
		return
	}
	o, err := h.store.TransitionOrder(c.Request.Context(), no, orders.Status(b.Status), actorOf(c), strings.TrimSpace(b.Note))
	switch {
	case errors.Is(err, repository.ErrNotFound):
		writeError(c, http.StatusNotFound, ErrMsgNotFound)
	case errors.Is(err, repository.ErrStateConflict):
		writeErrorCode(c, http.StatusConflict, ErrMsgStateConflict, CodeStateConflict)
	case err != nil:
		writeInternal(c, "admin orders: transition failed", err)
	default:
		writeJSON(c, http.StatusOK, adminOrder(*o))
	}
}

type noteBody struct {
	Note string `json:"note"`
}

func (b noteBody) validate() error {
	if n := utf8.RuneCountInString(strings.TrimSpace(b.Note)); n == 0 || n > 1000 {
		return errors.New("note must be 1 to 1,000 characters.")
	}
	return nil
}

// AddNote adds a fulfilment note.
func (h *AdminOrdersHandler) AddNote(c *gin.Context) {
	no, ok := orderNoParam(c)
	if !ok {
		return
	}
	var b noteBody
	if !readJSON(c, &b) {
		return
	}
	err := h.store.AddOrderNote(c.Request.Context(), no, actorOf(c), strings.TrimSpace(b.Note))
	switch {
	case errors.Is(err, repository.ErrNotFound):
		writeError(c, http.StatusNotFound, ErrMsgNotFound)
	case err != nil:
		writeInternal(c, "admin orders: note failed", err)
	default:
		c.Status(http.StatusCreated)
	}
}
