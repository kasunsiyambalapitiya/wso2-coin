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
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/catalog"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/repository"
)

// CatalogReader is the shop's read side of the catalog.
type CatalogReader interface {
	ActiveCategories(ctx context.Context) ([]repository.Category, error)
	SearchProducts(ctx context.Context, q repository.ProductQuery) ([]repository.ProductCard, int, error)
	ProductBySlug(ctx context.Context, slug string) (*repository.ProductDetail, error)
}

// ListCache keeps product-list responses for 60 s. A product status change
// clears it (backend-spec §8.5).
type ListCache struct {
	ttl time.Duration
	now func() time.Time
	mu  sync.Mutex
	m   map[string]listEntry
}

type listEntry struct {
	body any
	at   time.Time
}

// NewListCache returns an empty cache with a 60 s lifetime.
func NewListCache() *ListCache {
	return &ListCache{ttl: 60 * time.Second, now: time.Now, m: map[string]listEntry{}}
}

func (c *ListCache) get(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok || c.now().Sub(e.at) >= c.ttl {
		return nil, false
	}
	return e.body, true
}

func (c *ListCache) put(key string, body any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) > 500 {
		c.m = map[string]listEntry{}
	}
	c.m[key] = listEntry{body: body, at: c.now()}
}

// Clear drops every cached list.
func (c *ListCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m = map[string]listEntry{}
}

// CatalogHandler serves the shop's categories and products. It never calls
// HR, so browsing works while HR is down.
type CatalogHandler struct {
	store    CatalogReader
	cache    *ListCache
	lowStock int
	photos   PhotoURLs
}

// NewCatalogHandler builds the handler.
func NewCatalogHandler(s CatalogReader, cache *ListCache, lowStock int, photos PhotoURLs) *CatalogHandler {
	return &CatalogHandler{store: s, cache: cache, lowStock: lowStock, photos: photos}
}

type categoryJSON struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// Categories lists categories with active products.
func (h *CatalogHandler) Categories(c *gin.Context) {
	cats, err := h.store.ActiveCategories(c.Request.Context())
	if err != nil {
		writeInternal(c, "categories: list failed", err)
		return
	}
	out := make([]categoryJSON, 0, len(cats))
	for _, cat := range cats {
		out = append(out, categoryJSON{ID: cat.ID, Slug: cat.Slug, Name: cat.Name})
	}
	writeJSON(c, http.StatusOK, out)
}

type productCardJSON struct {
	ID           string               `json:"id"`
	Slug         string               `json:"slug"`
	Name         string               `json:"name"`
	Price        int64                `json:"price"`
	Image        *string              `json:"image"`
	Availability catalog.Availability `json:"availability"`
	Status       string               `json:"status"`
}

func (h *CatalogHandler) card(p repository.ProductCard) productCardJSON {
	return productCardJSON{ID: p.ID, Slug: p.Slug, Name: p.Name, Price: p.Price, Image: photoURL(h.photos, p.ImagePath), Status: p.Status,
		Availability: catalog.AvailabilityOf(p.MaxFree, h.lowStock, catalog.RestockPolicy(p.RestockPolicy))}
}

type productListJSON struct {
	Items []productCardJSON `json:"items"`
	Total int               `json:"total"`
}

var shopSorts = map[string]bool{"popular": true, "price_asc": true, "price_desc": true, "newest": true}

// Products lists ACTIVE products: q, category, sort, inStock, limit, offset.
func (h *CatalogHandler) Products(c *gin.Context) {
	limit, offset, ok := paging(c)
	if !ok {
		return
	}
	sort := c.DefaultQuery("sort", "popular")
	if !shopSorts[sort] {
		writeError(c, http.StatusBadRequest, "sort must be popular, price_asc, price_desc or newest.")
		return
	}
	inStock, err := strconv.ParseBool(c.DefaultQuery("inStock", "false"))
	if err != nil {
		writeError(c, http.StatusBadRequest, "inStock must be true or false.")
		return
	}
	q := repository.ProductQuery{Q: c.Query("q"), CategorySlug: c.Query("category"), Sort: sort, InStock: inStock, Limit: limit, Offset: offset}
	if len(q.Q) > 100 {
		writeError(c, http.StatusBadRequest, "q is too long.")
		return
	}
	key := url.Values{"q": {q.Q}, "c": {q.CategorySlug}, "s": {sort}, "i": {strconv.FormatBool(inStock)},
		"l": {strconv.Itoa(limit)}, "o": {strconv.Itoa(offset)}}.Encode()
	if body, ok := h.cache.get(key); ok {
		writeJSON(c, http.StatusOK, body)
		return
	}
	cards, total, err := h.store.SearchProducts(c.Request.Context(), q)
	if err != nil {
		writeInternal(c, "products: search failed", err)
		return
	}
	body := productListJSON{Items: make([]productCardJSON, 0, len(cards)), Total: total}
	for _, p := range cards {
		body.Items = append(body.Items, h.card(p))
	}
	h.cache.put(key, body)
	writeJSON(c, http.StatusOK, body)
}

type optionValueJSON struct {
	ID        string  `json:"id"`
	Value     string  `json:"value"`
	SwatchHex *string `json:"swatchHex"`
}

type optionJSON struct {
	Name   string            `json:"name"`
	Values []optionValueJSON `json:"values"`
}

type variantJSON struct {
	ID             string               `json:"id"`
	SKU            string               `json:"sku"`
	Price          int64                `json:"price"`
	Options        string               `json:"options"`
	OptionValueIDs []string             `json:"optionValueIds"`
	Availability   catalog.Availability `json:"availability"`
	StockLeft      *int                 `json:"stockLeft"`
	RestockETA     *string              `json:"restockEta"`
}

type productDetailJSON struct {
	ID            string            `json:"id"`
	Slug          string            `json:"slug"`
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	Price         int64             `json:"price"`
	Status        string            `json:"status"`
	RestockPolicy string            `json:"restockPolicy"`
	MaxPerUser    *int              `json:"maxPerUser"`
	Options       []optionJSON      `json:"options"`
	Variants      []variantJSON     `json:"variants"`
	Images        []imageJSON       `json:"images"`
	Alternatives  []productCardJSON `json:"alternatives"`
}

func dateString(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.Format(time.DateOnly)
	return &s
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Product returns one product by slug. A REMOVED product answers 200 with its
// status and alternatives only, so the UI can show "no longer sold". A DRAFT
// is not found.
func (h *CatalogHandler) Product(c *gin.Context) {
	slug := c.Param("slug")
	if len(slug) > 120 {
		writeError(c, http.StatusNotFound, ErrMsgNotFound)
		return
	}
	d, err := h.store.ProductBySlug(c.Request.Context(), slug)
	if err != nil {
		writeInternal(c, "product: read failed", err)
		return
	}
	if d == nil || d.Product.Status == catalog.StatusDraft {
		writeError(c, http.StatusNotFound, ErrMsgNotFound)
		return
	}
	p := d.Product
	out := productDetailJSON{ID: p.ID, Slug: p.Slug, Name: p.Name, Description: p.Description, Price: p.Price,
		Status: p.Status, RestockPolicy: p.RestockPolicy, MaxPerUser: p.MaxPerUser,
		Options: []optionJSON{}, Variants: []variantJSON{}, Images: []imageJSON{}, Alternatives: []productCardJSON{}}
	for _, a := range d.Alternatives {
		out.Alternatives = append(out.Alternatives, h.card(a))
	}
	if p.Status == catalog.StatusRemoved {
		writeJSON(c, http.StatusOK, out)
		return
	}
	used := map[string]bool{}
	for _, v := range d.Variants {
		if !v.Active {
			continue
		}
		for _, id := range v.ValueIDs {
			used[id] = true
		}
		avail := catalog.AvailabilityOf(v.Free, h.lowStock, catalog.RestockPolicy(p.RestockPolicy))
		var left *int
		if avail == catalog.LowStock {
			n := v.Free
			left = &n
		}
		var eta *string
		if avail == catalog.OutOfStock {
			eta = dateString(v.RestockETA)
		}
		out.Variants = append(out.Variants, variantJSON{ID: v.ID, SKU: v.SKU, Price: p.Price + v.PriceDelta, Options: v.Options,
			OptionValueIDs: v.ValueIDs, Availability: avail, StockLeft: left, RestockETA: eta})
	}
	for _, o := range d.Options {
		oj := optionJSON{Name: o.Name, Values: []optionValueJSON{}}
		for _, v := range o.Values {
			if used[v.ID] {
				oj.Values = append(oj.Values, optionValueJSON{ID: v.ID, Value: v.Value, SwatchHex: strPtr(v.SwatchHex)})
			}
		}
		if len(oj.Values) > 0 {
			out.Options = append(out.Options, oj)
		}
	}
	out.Images = imagesJSON(h.photos, d.Images)
	writeJSON(c, http.StatusOK, out)
}
