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
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/catalog"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/checkout"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/repository"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/reqctx"
)

// CartStore is the cart's persistence.
type CartStore interface {
	CartForQuote(ctx context.Context, email string) ([]repository.CartItem, error)
	CartItemCount(ctx context.Context, email string) (int, error)
	AddToCart(ctx context.Context, email, variantID string, quantity int) (string, error)
	SetCartQuantity(ctx context.Context, email, lineID string, quantity int) error
	RemoveCartLine(ctx context.Context, email, lineID string) error
}

// CartHandler serves the caller's own cart.
type CartHandler struct {
	store    CartStore
	lowStock int
	photos   PhotoURLs
}

// NewCartHandler builds the handler.
func NewCartHandler(s CartStore, lowStock int, photos PhotoURLs) *CartHandler {
	return &CartHandler{store: s, lowStock: lowStock, photos: photos}
}

type cartLineJSON struct {
	ID           string               `json:"id"`
	VariantID    string               `json:"variantId"`
	ProductSlug  string               `json:"productSlug"`
	Name         string               `json:"name"`
	Options      string               `json:"options"`
	Image        *string              `json:"image"`
	UnitPrice    int64                `json:"unitPrice"`
	Quantity     int                  `json:"quantity"`
	LineTotal    int64                `json:"lineTotal"`
	State        catalog.LineState    `json:"state"`
	Reason       *string              `json:"reason"`
	Availability catalog.Availability `json:"availability"`
}

type cartJSON struct {
	Lines     []cartLineJSON `json:"lines"`
	ItemCount int            `json:"itemCount"`
	Total     int64          `json:"total"`
}

// Get returns the cart with live prices, each line's state and reason, and
// the total of the lines that can be ordered.
func (h *CartHandler) Get(c *gin.Context) {
	u := reqctx.UserFrom(c.Request.Context())
	items, err := h.store.CartForQuote(c.Request.Context(), u.Email)
	if err != nil {
		writeInternal(c, "cart: read failed", err)
		return
	}
	out := cartJSON{Lines: make([]cartLineJSON, 0, len(items))}
	results := catalog.CheckLines(checkout.Facts(items))
	for i, it := range items {
		state, reason := results[i].State, results[i].Reason
		line := cartLineJSON{ID: it.LineID, VariantID: it.VariantID, ProductSlug: it.Slug, Name: it.Name, Options: it.Options,
			Image: photoURL(h.photos, it.ImagePath), UnitPrice: it.UnitPrice, Quantity: it.Quantity, LineTotal: it.UnitPrice * int64(it.Quantity),
			State: state, Reason: strPtr(reason),
			Availability: catalog.AvailabilityOf(it.Free, h.lowStock, catalog.RestockPolicy(it.RestockPolicy))}
		out.ItemCount += it.Quantity
		if state == catalog.LineOK {
			out.Total += line.LineTotal
		}
		out.Lines = append(out.Lines, line)
	}
	writeJSON(c, http.StatusOK, out)
}

// Summary returns {itemCount} for the top-bar badge.
func (h *CartHandler) Summary(c *gin.Context) {
	u := reqctx.UserFrom(c.Request.Context())
	n, err := h.store.CartItemCount(c.Request.Context(), u.Email)
	if err != nil {
		writeInternal(c, "cart: count failed", err)
		return
	}
	writeJSON(c, http.StatusOK, gin.H{"itemCount": n})
}

type addLineBody struct {
	VariantID string `json:"variantId"`
	Quantity  int    `json:"quantity"`
}

func (b addLineBody) validate() error {
	if !uuidRe.MatchString(b.VariantID) {
		return errors.New("variantId must be a UUID.")
	}
	if b.Quantity < 1 || b.Quantity > 99 {
		return errors.New("quantity must be between 1 and 99.")
	}
	return nil
}

// AddLine merges {variantId, quantity} into the cart.
func (h *CartHandler) AddLine(c *gin.Context) {
	var b addLineBody
	if !readJSON(c, &b) {
		return
	}
	u := reqctx.UserFrom(c.Request.Context())
	id, err := h.store.AddToCart(c.Request.Context(), u.Email, b.VariantID, b.Quantity)
	switch {
	case errors.Is(err, repository.ErrNotFound):
		writeError(c, http.StatusNotFound, ErrMsgNotFound)
	case errors.Is(err, repository.ErrNotAvailable):
		writeErrorCode(c, http.StatusConflict, ErrMsgNotAvailable, CodeNotAvailable)
	case err != nil:
		writeInternal(c, "cart: add failed", err)
	default:
		writeJSON(c, http.StatusCreated, gin.H{"id": id})
	}
}

type setQuantityBody struct {
	Quantity int `json:"quantity"`
}

func (b setQuantityBody) validate() error {
	if b.Quantity < 1 || b.Quantity > 99 {
		return errors.New("quantity must be between 1 and 99.")
	}
	return nil
}

// UpdateLine changes the quantity of one of the caller's lines.
func (h *CartHandler) UpdateLine(c *gin.Context) {
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	var b setQuantityBody
	if !readJSON(c, &b) {
		return
	}
	u := reqctx.UserFrom(c.Request.Context())
	err := h.store.SetCartQuantity(c.Request.Context(), u.Email, id, b.Quantity)
	switch {
	case errors.Is(err, repository.ErrNotFound):
		writeError(c, http.StatusNotFound, ErrMsgNotFound)
	case err != nil:
		writeInternal(c, "cart: update failed", err)
	default:
		c.Status(http.StatusNoContent)
	}
}

// DeleteLine removes one of the caller's lines. Another user's id is 404.
func (h *CartHandler) DeleteLine(c *gin.Context) {
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	u := reqctx.UserFrom(c.Request.Context())
	err := h.store.RemoveCartLine(c.Request.Context(), u.Email, id)
	switch {
	case errors.Is(err, repository.ErrNotFound):
		writeError(c, http.StatusNotFound, ErrMsgNotFound)
	case err != nil:
		writeInternal(c, "cart: delete failed", err)
	default:
		c.Status(http.StatusNoContent)
	}
}
