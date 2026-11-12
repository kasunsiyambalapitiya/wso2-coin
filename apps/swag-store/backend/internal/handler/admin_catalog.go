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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/catalog"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/repository"
)

// AdminCatalogStore is the Catalog Manager's persistence.
type AdminCatalogStore interface {
	SearchProducts(ctx context.Context, q repository.ProductQuery) ([]repository.ProductCard, int, error)
	ProductByID(ctx context.Context, id string) (*repository.ProductDetail, error)
	CreateProduct(ctx context.Context, a repository.Actor, in repository.ProductInput) (*repository.ProductDetail, error)
	UpdateProduct(ctx context.Context, a repository.Actor, id string, in repository.ProductInput) (*repository.ProductDetail, error)
	SetProductStatus(ctx context.Context, a repository.Actor, id, to, note string) error
	DeleteDraft(ctx context.Context, a repository.Actor, id string) error
	RemovalImpact(ctx context.Context, id string) (*repository.RemovalImpact, error)
	SetOptions(ctx context.Context, a repository.Actor, id string, opts []repository.OptionInput) (*repository.ProductDetail, error)
	UpdateVariant(ctx context.Context, a repository.Actor, productID, variantID string, in repository.VariantInput) error
	RecordStockMovement(ctx context.Context, a repository.Actor, variantID, reason string, delta int, note string) (*repository.StockResult, error)
	Categories(ctx context.Context) ([]repository.Category, error)
	Category(ctx context.Context, id string) (*repository.Category, error)
	SaveCategory(ctx context.Context, a repository.Actor, c repository.Category) (*repository.Category, error)
	DeleteCategory(ctx context.Context, a repository.Actor, id string) error
	Demand(ctx context.Context) ([]repository.DemandRow, error)
}

// AdminCatalogHandler serves the Catalog Manager's routes.
type AdminCatalogHandler struct {
	store      AdminCatalogStore
	cache      *ListCache
	imageHosts []string
}

// NewAdminCatalogHandler builds the handler. Every write clears the shop's
// 60 s product-list cache.
func NewAdminCatalogHandler(s AdminCatalogStore, cache *ListCache, imageHosts []string) *AdminCatalogHandler {
	return &AdminCatalogHandler{store: s, cache: cache, imageHosts: imageHosts}
}

var (
	slugRe   = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	skuRe    = regexp.MustCompile(`^[A-Z0-9][A-Z0-9-]{0,63}$`)
	swatchRe = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
)

func (h *AdminCatalogHandler) writeStoreError(c *gin.Context, msg string, err error) {
	switch {
	case errors.Is(err, repository.ErrNotFound):
		writeError(c, http.StatusNotFound, ErrMsgNotFound)
	case errors.Is(err, repository.ErrStateConflict):
		writeErrorCode(c, http.StatusConflict, ErrMsgStateConflict, CodeStateConflict)
	case errors.Is(err, repository.ErrInUse):
		writeErrorCode(c, http.StatusConflict, "This is still in use.", CodeStateConflict)
	case errors.Is(err, repository.ErrConflict):
		writeErrorCode(c, http.StatusConflict, "That slug or SKU is already taken.", CodeStateConflict)
	default:
		writeInternal(c, msg, err)
	}
}

type adminVariantJSON struct {
	ID         string   `json:"id"`
	SKU        string   `json:"sku"`
	Options    string   `json:"options"`
	ValueIDs   []string `json:"optionValueIds"`
	PriceDelta int64    `json:"priceDelta"`
	OnHand     int      `json:"onHand"`
	Reserved   int      `json:"reserved"`
	Free       int      `json:"free"`
	RestockETA *string  `json:"restockEta"`
	Active     bool     `json:"active"`
}

type adminProductJSON struct {
	ID              string             `json:"id"`
	Slug            string             `json:"slug"`
	Name            string             `json:"name"`
	Description     string             `json:"description"`
	CategoryID      *string            `json:"categoryId"`
	Price           int64              `json:"price"`
	RestockPolicy   string             `json:"restockPolicy"`
	MaxPerUser      *int               `json:"maxPerUser"`
	LimitWindowDays *int               `json:"limitWindowDays"`
	Status          string             `json:"status"`
	PublishedAt     *time.Time         `json:"publishedAt"`
	RemovedAt       *time.Time         `json:"removedAt"`
	RemovalNote     *string            `json:"removalNote"`
	Options         []optionJSON       `json:"options"`
	Variants        []adminVariantJSON `json:"variants"`
	Images          []imageJSON        `json:"images"`
	Alternatives    []string           `json:"alternatives"`
}

func adminProduct(d *repository.ProductDetail) adminProductJSON {
	p := d.Product
	out := adminProductJSON{ID: p.ID, Slug: p.Slug, Name: p.Name, Description: p.Description, CategoryID: strPtr(p.CategoryID),
		Price: p.Price, RestockPolicy: p.RestockPolicy, MaxPerUser: p.MaxPerUser, LimitWindowDays: p.LimitWindowDays,
		Status: p.Status, PublishedAt: p.PublishedAt, RemovedAt: p.RemovedAt, RemovalNote: strPtr(p.RemovalNote),
		Options: []optionJSON{}, Variants: []adminVariantJSON{}, Images: []imageJSON{}, Alternatives: []string{}}
	for _, o := range d.Options {
		oj := optionJSON{Name: o.Name, Values: []optionValueJSON{}}
		for _, v := range o.Values {
			oj.Values = append(oj.Values, optionValueJSON{ID: v.ID, Value: v.Value, SwatchHex: strPtr(v.SwatchHex)})
		}
		out.Options = append(out.Options, oj)
	}
	for _, v := range d.Variants {
		out.Variants = append(out.Variants, adminVariantJSON{ID: v.ID, SKU: v.SKU, Options: v.Options, ValueIDs: v.ValueIDs,
			PriceDelta: v.PriceDelta, OnHand: v.OnHand, Reserved: v.Reserved, Free: v.Free,
			RestockETA: dateString(v.RestockETA), Active: v.Active})
	}
	for _, im := range d.Images {
		out.Images = append(out.Images, imageJSON{URL: im.URL, Alt: im.Alt})
	}
	out.Alternatives = append(out.Alternatives, d.CuratedAlternativeIDs...)
	return out
}

// List lists products for the editor: status, q, limit, offset.
func (h *AdminCatalogHandler) List(c *gin.Context) {
	limit, offset, ok := paging(c)
	if !ok {
		return
	}
	status := c.DefaultQuery("status", catalog.StatusActive)
	if status != catalog.StatusActive && status != catalog.StatusDraft && status != catalog.StatusRemoved {
		writeError(c, http.StatusBadRequest, "status must be ACTIVE, DRAFT or REMOVED.")
		return
	}
	cards, total, err := h.store.SearchProducts(c.Request.Context(), repository.ProductQuery{Q: c.Query("q"), Status: status,
		Sort: "name", Limit: limit, Offset: offset})
	if err != nil {
		writeInternal(c, "admin products: list failed", err)
		return
	}
	items := make([]gin.H, 0, len(cards))
	for _, p := range cards {
		items = append(items, gin.H{"id": p.ID, "slug": p.Slug, "name": p.Name, "status": p.Status, "price": p.Price,
			"image": strPtr(p.ImageURL), "freeStock": p.MaxFree, "unitsSold30d": p.UnitsSold})
	}
	writeJSON(c, http.StatusOK, gin.H{"items": items, "total": total})
}

// Get returns one product with every variant, including inactive ones.
func (h *AdminCatalogHandler) Get(c *gin.Context) {
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	d, err := h.store.ProductByID(c.Request.Context(), id)
	if err != nil {
		writeInternal(c, "admin products: read failed", err)
		return
	}
	if d == nil {
		writeError(c, http.StatusNotFound, ErrMsgNotFound)
		return
	}
	writeJSON(c, http.StatusOK, adminProduct(d))
}

type imageBody struct {
	URL string `json:"url"`
	Alt string `json:"alt"`
}

// productBody is POST /admin/products and the field form of PATCH. Status
// and removalNote belong to the status form of PATCH only.
type productBody struct {
	Name            *string      `json:"name"`
	Slug            *string      `json:"slug"`
	Description     *string      `json:"description"`
	CategoryID      *string      `json:"categoryId"`
	Price           *int64       `json:"price"`
	RestockPolicy   *string      `json:"restockPolicy"`
	MaxPerUser      *int         `json:"maxPerUser"`
	LimitWindowDays *int         `json:"limitWindowDays"`
	Images          *[]imageBody `json:"images"`
	Alternatives    *[]string    `json:"alternatives"`
	Status          *string      `json:"status"`
	RemovalNote     *string      `json:"removalNote"`
	raw             map[string]json.RawMessage
}

func (b *productBody) fieldsSet() bool {
	return b.Name != nil || b.Slug != nil || b.Description != nil || b.CategoryID != nil || b.Price != nil ||
		b.RestockPolicy != nil || b.raw["maxPerUser"] != nil || b.raw["limitWindowDays"] != nil || b.Images != nil || b.Alternatives != nil
}

// apply copies the set fields onto in and checks the result.
func (h *AdminCatalogHandler) apply(b *productBody, in *repository.ProductInput) error {
	if b.Name != nil {
		in.Name = strings.TrimSpace(*b.Name)
	}
	if b.Slug != nil {
		in.Slug = strings.TrimSpace(*b.Slug)
	}
	if in.Slug == "" {
		in.Slug = catalog.Slugify(in.Name)
	}
	if b.Description != nil {
		in.Description = strings.TrimSpace(*b.Description)
	}
	if b.CategoryID != nil {
		in.CategoryID = strings.ToLower(*b.CategoryID)
	}
	if b.Price != nil {
		in.Price = *b.Price
	}
	if b.RestockPolicy != nil {
		in.RestockPolicy = *b.RestockPolicy
	}
	if _, ok := b.raw["maxPerUser"]; ok {
		in.MaxPerUser = b.MaxPerUser
	}
	if _, ok := b.raw["limitWindowDays"]; ok {
		in.LimitWindowDays = b.LimitWindowDays
	}
	if b.Images != nil {
		in.Images = nil
		for _, im := range *b.Images {
			in.Images = append(in.Images, repository.ImageInput{URL: strings.TrimSpace(im.URL), Alt: strings.TrimSpace(im.Alt)})
		}
	}
	if b.Alternatives != nil {
		in.Alternatives = nil
		for _, a := range *b.Alternatives {
			in.Alternatives = append(in.Alternatives, strings.ToLower(a))
		}
	}
	switch n := utf8.RuneCountInString(in.Name); {
	case n == 0 || n > 200:
		return errors.New("name must be 1 to 200 characters.")
	case !slugRe.MatchString(in.Slug) || len(in.Slug) > 120:
		return errors.New("slug must be lower-case letters, digits and dashes.")
	case utf8.RuneCountInString(in.Description) > 2000:
		return errors.New("description is at most 2,000 characters.")
	case in.CategoryID != "" && !uuidRe.MatchString(in.CategoryID):
		return errors.New("categoryId must be a UUID.")
	case in.Price < 1 || in.Price > 1_000_000:
		return errors.New("price must be between 1 and 1,000,000 coins.")
	case in.RestockPolicy != string(catalog.Restockable) && in.RestockPolicy != string(catalog.Limited):
		return errors.New("restockPolicy must be RESTOCKABLE or LIMITED.")
	case in.MaxPerUser != nil && (*in.MaxPerUser < 1 || *in.MaxPerUser > 99):
		return errors.New("maxPerUser must be between 1 and 99.")
	case in.LimitWindowDays != nil && (*in.LimitWindowDays < 1 || *in.LimitWindowDays > 3650):
		return errors.New("limitWindowDays must be between 1 and 3650.")
	case len(in.Images) > 10:
		return errors.New("at most 10 images.")
	case len(in.Alternatives) > 8:
		return errors.New("at most 8 alternatives.")
	}
	for _, im := range in.Images {
		if !catalog.ValidImageURL(im.URL, h.imageHosts) || len(im.URL) > 2048 {
			return errors.New("image URLs must be https on an allowed host.")
		}
		if utf8.RuneCountInString(im.Alt) > 300 {
			return errors.New("image alt text is at most 300 characters.")
		}
	}
	for _, a := range in.Alternatives {
		if !uuidRe.MatchString(a) {
			return errors.New("alternatives must be product UUIDs.")
		}
	}
	return nil
}

func readProductBody(c *gin.Context) (*productBody, bool) {
	var b productBody
	if !readJSON(c, &b) {
		return nil, false
	}
	// Re-read the raw keys to tell "null" from "absent" for nullable fields.
	b.raw = map[string]json.RawMessage{}
	if raw, ok := c.Get(rawBodyKey); ok {
		_ = json.NewDecoder(bytes.NewReader(raw.([]byte))).Decode(&b.raw)
	}
	return &b, true
}

// Create adds a DRAFT product with one default variant.
func (h *AdminCatalogHandler) Create(c *gin.Context) {
	b, ok := readProductBody(c)
	if !ok {
		return
	}
	if b.Status != nil || b.RemovalNote != nil {
		writeError(c, http.StatusBadRequest, "A new product is always a DRAFT.")
		return
	}
	in := repository.ProductInput{RestockPolicy: string(catalog.Restockable)}
	if err := h.apply(b, &in); err != nil {
		writeError(c, http.StatusBadRequest, err.Error())
		return
	}
	d, err := h.store.CreateProduct(c.Request.Context(), actorOf(c), in)
	if err != nil {
		h.writeStoreError(c, "admin products: create failed", err)
		return
	}
	writeJSON(c, http.StatusCreated, adminProduct(d))
}

// Patch edits fields, or changes status: DRAFT→ACTIVE publishes, ACTIVE→REMOVED
// removes, REMOVED→ACTIVE restores. The two forms cannot be mixed.
func (h *AdminCatalogHandler) Patch(c *gin.Context) {
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	b, ok := readProductBody(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if b.Status != nil {
		if b.fieldsSet() {
			writeError(c, http.StatusBadRequest, "A status change carries only status and removalNote.")
			return
		}
		to := *b.Status
		if to != catalog.StatusActive && to != catalog.StatusRemoved {
			writeError(c, http.StatusBadRequest, "status must be ACTIVE or REMOVED.")
			return
		}
		note := ""
		if b.RemovalNote != nil {
			note = strings.TrimSpace(*b.RemovalNote)
		}
		if utf8.RuneCountInString(note) > 500 {
			writeError(c, http.StatusBadRequest, "removalNote is at most 500 characters.")
			return
		}
		if err := h.store.SetProductStatus(ctx, actorOf(c), id, to, note); err != nil {
			h.writeStoreError(c, "admin products: status failed", err)
			return
		}
		h.cache.Clear()
		h.respondProduct(c, id)
		return
	}
	if b.RemovalNote != nil {
		writeError(c, http.StatusBadRequest, "removalNote goes with a status change.")
		return
	}
	cur, err := h.store.ProductByID(ctx, id)
	if err != nil {
		writeInternal(c, "admin products: read failed", err)
		return
	}
	if cur == nil {
		writeError(c, http.StatusNotFound, ErrMsgNotFound)
		return
	}
	p := cur.Product
	in := repository.ProductInput{Name: p.Name, Slug: p.Slug, Description: p.Description, CategoryID: p.CategoryID,
		Price: p.Price, RestockPolicy: p.RestockPolicy, MaxPerUser: p.MaxPerUser, LimitWindowDays: p.LimitWindowDays}
	for _, im := range cur.Images {
		in.Images = append(in.Images, repository.ImageInput{URL: im.URL, Alt: im.Alt})
	}
	in.Alternatives = append(in.Alternatives, cur.CuratedAlternativeIDs...)
	if err := h.apply(b, &in); err != nil {
		writeError(c, http.StatusBadRequest, err.Error())
		return
	}
	d, err := h.store.UpdateProduct(ctx, actorOf(c), id, in)
	if err != nil {
		h.writeStoreError(c, "admin products: update failed", err)
		return
	}
	h.cache.Clear()
	writeJSON(c, http.StatusOK, adminProduct(d))
}

func (h *AdminCatalogHandler) respondProduct(c *gin.Context, id string) {
	d, err := h.store.ProductByID(c.Request.Context(), id)
	if err != nil {
		writeInternal(c, "admin products: read failed", err)
		return
	}
	if d == nil {
		writeError(c, http.StatusNotFound, ErrMsgNotFound)
		return
	}
	writeJSON(c, http.StatusOK, adminProduct(d))
}

// Delete removes a DRAFT that was never ordered.
func (h *AdminCatalogHandler) Delete(c *gin.Context) {
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	if err := h.store.DeleteDraft(c.Request.Context(), actorOf(c), id); err != nil {
		h.writeStoreError(c, "admin products: delete failed", err)
		return
	}
	c.Status(http.StatusNoContent)
}

// RemovalImpact reports what removing the product would touch.
func (h *AdminCatalogHandler) RemovalImpact(c *gin.Context) {
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	imp, err := h.store.RemovalImpact(c.Request.Context(), id)
	if err != nil {
		h.writeStoreError(c, "admin products: impact failed", err)
		return
	}
	writeJSON(c, http.StatusOK, imp)
}

type optionsBody struct {
	Options []struct {
		Name   string `json:"name"`
		Values []struct {
			Value     string `json:"value"`
			SwatchHex string `json:"swatchHex"`
		} `json:"values"`
	} `json:"options"`
}

func (b optionsBody) validate() error {
	if len(b.Options) > 3 {
		return errors.New("at most 3 options.")
	}
	combos := 1
	names := map[string]bool{}
	for _, o := range b.Options {
		n := strings.TrimSpace(o.Name)
		if n == "" || len(n) > 60 || names[n] {
			return errors.New("each option needs a unique name of 1 to 60 characters.")
		}
		names[n] = true
		if len(o.Values) == 0 || len(o.Values) > 30 {
			return errors.New("each option needs 1 to 30 values.")
		}
		vals := map[string]bool{}
		for _, v := range o.Values {
			val := strings.TrimSpace(v.Value)
			if val == "" || len(val) > 60 || vals[val] {
				return errors.New("option values must be unique, 1 to 60 characters.")
			}
			vals[val] = true
			if v.SwatchHex != "" && !swatchRe.MatchString(v.SwatchHex) {
				return errors.New("swatchHex must look like #1A2B3C.")
			}
		}
		combos *= len(o.Values)
	}
	if combos > 200 {
		return errors.New("at most 200 variants.")
	}
	return nil
}

// SetOptions replaces the options and regenerates the variant matrix,
// keeping the SKUs of combinations that still exist.
func (h *AdminCatalogHandler) SetOptions(c *gin.Context) {
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	var b optionsBody
	if !readJSON(c, &b) {
		return
	}
	opts := make([]repository.OptionInput, 0, len(b.Options))
	for _, o := range b.Options {
		in := repository.OptionInput{Name: strings.TrimSpace(o.Name)}
		for _, v := range o.Values {
			in.Values = append(in.Values, repository.OptionValueInput{Value: strings.TrimSpace(v.Value), SwatchHex: strings.ToUpper(v.SwatchHex)})
		}
		opts = append(opts, in)
	}
	d, err := h.store.SetOptions(c.Request.Context(), actorOf(c), id, opts)
	if err != nil {
		h.writeStoreError(c, "admin products: options failed", err)
		return
	}
	h.cache.Clear()
	writeJSON(c, http.StatusOK, adminProduct(d))
}

type variantBody struct {
	SKU        *string         `json:"sku"`
	PriceDelta *int64          `json:"priceDelta"`
	Active     *bool           `json:"active"`
	RestockETA json.RawMessage `json:"restockEta"`
}

// PatchVariant edits SKU, price delta, active and restock date.
func (h *AdminCatalogHandler) PatchVariant(c *gin.Context) {
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	variantID, ok := uuidParam(c, "variantId")
	if !ok {
		return
	}
	var b variantBody
	if !readJSON(c, &b) {
		return
	}
	in := repository.VariantInput{PriceDelta: b.PriceDelta, Active: b.Active}
	if b.SKU != nil {
		sku := strings.ToUpper(strings.TrimSpace(*b.SKU))
		if !skuRe.MatchString(sku) {
			writeError(c, http.StatusBadRequest, "sku must be upper-case letters, digits and dashes.")
			return
		}
		in.SKU = &sku
	}
	if b.PriceDelta != nil && (*b.PriceDelta < -1_000_000 || *b.PriceDelta > 1_000_000) {
		writeError(c, http.StatusBadRequest, "priceDelta is out of range.")
		return
	}
	if len(b.RestockETA) > 0 {
		if string(b.RestockETA) == "null" {
			in.ClearETA = true
		} else {
			var s string
			t, err := time.Time{}, json.Unmarshal(b.RestockETA, &s)
			if err == nil {
				t, err = time.Parse(time.DateOnly, s)
			}
			if err != nil {
				writeError(c, http.StatusBadRequest, "restockEta must be a date like 2026-12-01, or null.")
				return
			}
			in.RestockETA = &t
		}
	}
	if err := h.store.UpdateVariant(c.Request.Context(), actorOf(c), id, variantID, in); err != nil {
		h.writeStoreError(c, "admin variants: update failed", err)
		return
	}
	h.cache.Clear()
	h.respondProduct(c, id)
}

type stockBody struct {
	Delta  int    `json:"delta"`
	Reason string `json:"reason"`
	Note   string `json:"note"`
}

func (b stockBody) validate() error {
	switch b.Reason {
	case repository.MoveReceive, repository.MoveWriteOff:
		if b.Delta < 1 || b.Delta > 100_000 {
			return errors.New("delta must be between 1 and 100,000 for " + b.Reason + ".")
		}
	case repository.MoveAdjust:
		if b.Delta == 0 || b.Delta < -100_000 || b.Delta > 100_000 {
			return errors.New("delta must be a non-zero number up to 100,000 either way.")
		}
	default:
		return errors.New("reason must be RECEIVE, ADJUST or WRITE_OFF.")
	}
	if utf8.RuneCountInString(b.Note) > 500 {
		return errors.New("note is at most 500 characters.")
	}
	return nil
}

// StockMovement records a receipt, adjustment or write-off.
func (h *AdminCatalogHandler) StockMovement(c *gin.Context) {
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	var b stockBody
	if !readJSON(c, &b) {
		return
	}
	res, err := h.store.RecordStockMovement(c.Request.Context(), actorOf(c), id, b.Reason, b.Delta, strings.TrimSpace(b.Note))
	if errors.Is(err, repository.ErrStateConflict) {
		writeErrorCode(c, http.StatusConflict, "Stock can't go below what open orders hold.", CodeStateConflict)
		return
	}
	if err != nil {
		h.writeStoreError(c, "admin stock: movement failed", err)
		return
	}
	h.cache.Clear()
	writeJSON(c, http.StatusOK, res)
}

// Categories lists every category.
func (h *AdminCatalogHandler) Categories(c *gin.Context) {
	cats, err := h.store.Categories(c.Request.Context())
	if err != nil {
		writeInternal(c, "admin categories: list failed", err)
		return
	}
	if cats == nil {
		cats = []repository.Category{}
	}
	writeJSON(c, http.StatusOK, gin.H{"items": cats})
}

// GetCategory returns one category.
func (h *AdminCatalogHandler) GetCategory(c *gin.Context) {
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	cat, err := h.store.Category(c.Request.Context(), id)
	if err != nil {
		h.writeStoreError(c, "admin categories: read failed", err)
		return
	}
	writeJSON(c, http.StatusOK, cat)
}

type categoryBody struct {
	Slug *string `json:"slug"`
	Name *string `json:"name"`
	Sort *int    `json:"sort"`
}

func (b categoryBody) apply(cat *repository.Category) error {
	if b.Name != nil {
		cat.Name = strings.TrimSpace(*b.Name)
	}
	if b.Slug != nil {
		cat.Slug = strings.TrimSpace(*b.Slug)
	}
	if cat.Slug == "" {
		cat.Slug = catalog.Slugify(cat.Name)
	}
	if b.Sort != nil {
		cat.Sort = *b.Sort
	}
	if n := utf8.RuneCountInString(cat.Name); n == 0 || n > 120 {
		return errors.New("name must be 1 to 120 characters.")
	}
	if !slugRe.MatchString(cat.Slug) || len(cat.Slug) > 80 {
		return errors.New("slug must be lower-case letters, digits and dashes.")
	}
	return nil
}

// CreateCategory adds a category.
func (h *AdminCatalogHandler) CreateCategory(c *gin.Context) {
	var b categoryBody
	if !readJSON(c, &b) {
		return
	}
	var cat repository.Category
	if err := b.apply(&cat); err != nil {
		writeError(c, http.StatusBadRequest, err.Error())
		return
	}
	saved, err := h.store.SaveCategory(c.Request.Context(), actorOf(c), cat)
	if err != nil {
		h.writeStoreError(c, "admin categories: create failed", err)
		return
	}
	h.cache.Clear()
	writeJSON(c, http.StatusCreated, saved)
}

// PatchCategory edits a category.
func (h *AdminCatalogHandler) PatchCategory(c *gin.Context) {
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	var b categoryBody
	if !readJSON(c, &b) {
		return
	}
	cur, err := h.store.Category(c.Request.Context(), id)
	if err != nil {
		h.writeStoreError(c, "admin categories: read failed", err)
		return
	}
	if err := b.apply(cur); err != nil {
		writeError(c, http.StatusBadRequest, err.Error())
		return
	}
	saved, err := h.store.SaveCategory(c.Request.Context(), actorOf(c), *cur)
	if err != nil {
		h.writeStoreError(c, "admin categories: update failed", err)
		return
	}
	h.cache.Clear()
	writeJSON(c, http.StatusOK, saved)
}

// DeleteCategory refuses (409) while a product points at the category.
func (h *AdminCatalogHandler) DeleteCategory(c *gin.Context) {
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	if err := h.store.DeleteCategory(c.Request.Context(), actorOf(c), id); err != nil {
		h.writeStoreError(c, "admin categories: delete failed", err)
		return
	}
	h.cache.Clear()
	c.Status(http.StatusNoContent)
}

// Demand lists pending alerts per variant.
func (h *AdminCatalogHandler) Demand(c *gin.Context) {
	rows, err := h.store.Demand(c.Request.Context())
	if err != nil {
		writeInternal(c, "admin demand: read failed", err)
		return
	}
	writeJSON(c, http.StatusOK, gin.H{"items": rows})
}
