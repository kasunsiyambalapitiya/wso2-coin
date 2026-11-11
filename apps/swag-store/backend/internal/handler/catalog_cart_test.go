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

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/repository"
)

type fakeCatalog struct {
	cards    []repository.ProductCard
	detail   *repository.ProductDetail
	searches int
}

func (f *fakeCatalog) ActiveCategories(context.Context) ([]repository.Category, error) {
	return []repository.Category{{ID: "c1", Slug: "apparel", Name: "Apparel"}}, nil
}

func (f *fakeCatalog) SearchProducts(context.Context, repository.ProductQuery) ([]repository.ProductCard, int, error) {
	f.searches++
	return f.cards, len(f.cards), nil
}

func (f *fakeCatalog) ProductBySlug(_ context.Context, slug string) (*repository.ProductDetail, error) {
	if f.detail == nil || f.detail.Product.Slug != slug {
		return nil, nil
	}
	return f.detail, nil
}

func TestProductListCachedAndCleared(t *testing.T) {
	f := &fakeCatalog{cards: []repository.ProductCard{{ID: "p1", Slug: "hoodie", Name: "WSO2 Hoodie", Price: 90, MaxFree: 3, RestockPolicy: "RESTOCKABLE"}}}
	cache := NewListCache()
	now := time.Unix(0, 0)
	cache.now = func() time.Time { return now }
	h := NewCatalogHandler(f, cache, 5)
	r := testRouter(shopper, http.MethodGet, "/products", h.Products)

	rec := do(r, http.MethodGet, "/products?sort=price_asc", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"availability":"LOW_STOCK"`) {
		t.Fatalf("list = %d %s", rec.Code, rec.Body)
	}
	do(r, http.MethodGet, "/products?sort=price_asc", "")
	if f.searches != 1 {
		t.Fatalf("not cached: %d searches", f.searches)
	}
	cache.Clear()
	do(r, http.MethodGet, "/products?sort=price_asc", "")
	if f.searches != 2 {
		t.Fatal("Clear did not drop the cached list")
	}
	now = now.Add(61 * time.Second)
	do(r, http.MethodGet, "/products?sort=price_asc", "")
	if f.searches != 3 {
		t.Fatal("cache never expires")
	}
	if rec := do(r, http.MethodGet, "/products?sort=cheapest", ""); rec.Code != 400 {
		t.Fatalf("bad sort = %d", rec.Code)
	}
	if rec := do(r, http.MethodGet, "/products?limit=500", ""); rec.Code != 400 {
		t.Fatalf("limit over 100 = %d", rec.Code)
	}
}

func TestRemovedProductShowsAlternativesOnly(t *testing.T) {
	f := &fakeCatalog{detail: &repository.ProductDetail{
		Product:      repository.Product{ID: "p1", Slug: "hoodie", Name: "WSO2 Hoodie", Status: "REMOVED", Price: 90},
		Variants:     []repository.Variant{{ID: "v1", Active: true, Free: 3}},
		Alternatives: []repository.ProductCard{{ID: "p2", Slug: "tumbler", Name: "Tumbler", MaxFree: 10}},
	}}
	h := NewCatalogHandler(f, NewListCache(), 5)
	r := testRouter(shopper, http.MethodGet, "/products/:slug", h.Product)
	rec := do(r, http.MethodGet, "/products/hoodie", "")
	body := decode[map[string]any](t, rec)
	if rec.Code != 200 || body["status"] != "REMOVED" || len(body["variants"].([]any)) != 0 || len(body["alternatives"].([]any)) != 1 {
		t.Fatalf("removed = %d %v", rec.Code, body)
	}
	f.detail.Product.Status = "DRAFT"
	if rec := do(r, http.MethodGet, "/products/hoodie", ""); rec.Code != 404 {
		t.Fatalf("draft = %d", rec.Code)
	}
	if rec := do(r, http.MethodGet, "/products/missing", ""); rec.Code != 404 {
		t.Fatalf("missing = %d", rec.Code)
	}
}

func TestProductDetailHidesInactiveVariants(t *testing.T) {
	f := &fakeCatalog{detail: &repository.ProductDetail{
		Product: repository.Product{ID: "p1", Slug: "hoodie", Name: "WSO2 Hoodie", Status: "ACTIVE", Price: 90, RestockPolicy: "RESTOCKABLE"},
		Options: []repository.Option{{Name: "Size", Values: []repository.OptionValue{{ID: "s", Value: "S"}, {ID: "m", Value: "M"}}}},
		Variants: []repository.Variant{
			{ID: "v1", SKU: "H-S", Active: true, Free: 0, ValueIDs: []string{"s"}},
			{ID: "v2", SKU: "H-M", Active: false, Free: 9, ValueIDs: []string{"m"}},
		},
	}}
	h := NewCatalogHandler(f, NewListCache(), 5)
	rec := do(testRouter(shopper, http.MethodGet, "/products/:slug", h.Product), http.MethodGet, "/products/hoodie", "")
	s := rec.Body.String()
	if rec.Code != 200 || strings.Contains(s, "H-M") || strings.Contains(s, `"value":"M"`) || !strings.Contains(s, `"availability":"OUT_OF_STOCK"`) {
		t.Fatalf("detail = %d %s", rec.Code, s)
	}
}

type fakeCart struct {
	items  []repository.CartItem
	addErr error
	ownErr error
}

func (f *fakeCart) CartForQuote(context.Context, string) ([]repository.CartItem, error) {
	return f.items, nil
}
func (f *fakeCart) CartItemCount(context.Context, string) (int, error) { return 3, nil }
func (f *fakeCart) AddToCart(context.Context, string, string, int) (string, error) {
	return "line-1", f.addErr
}
func (f *fakeCart) SetCartQuantity(context.Context, string, string, int) error { return f.ownErr }
func (f *fakeCart) RemoveCartLine(context.Context, string, string) error       { return f.ownErr }

func TestCartTotalsOnlyOrderableLines(t *testing.T) {
	f := &fakeCart{items: []repository.CartItem{
		{LineID: "l1", Name: "Hoodie", UnitPrice: 90, Quantity: 2, Free: 5, ProductStatus: "ACTIVE", VariantActive: true},
		{LineID: "l2", Name: "Umbrella", UnitPrice: 40, Quantity: 1, Free: 5, ProductStatus: "REMOVED", VariantActive: true},
	}}
	h := NewCartHandler(f, 5)
	rec := do(testRouter(shopper, http.MethodGet, "/cart", h.Get), http.MethodGet, "/cart", "")
	body := decode[map[string]any](t, rec)
	if rec.Code != 200 || body["total"] != float64(180) || body["itemCount"] != float64(3) {
		t.Fatalf("cart = %d %v", rec.Code, body)
	}
	if l := body["lines"].([]any)[1].(map[string]any); l["state"] != "REMOVED" || l["reason"] == nil {
		t.Fatalf("removed line = %v", l)
	}
}

func TestCartWrites(t *testing.T) {
	const line = "/cart/lines/0192f3a4-0000-7000-8000-0000000000cc"
	h := NewCartHandler(&fakeCart{addErr: repository.ErrNotAvailable}, 5)
	rec := do(testRouter(shopper, http.MethodPost, "/cart/lines", h.AddLine), http.MethodPost, "/cart/lines",
		`{"variantId":"0192f3a4-0000-7000-8000-0000000000cc","quantity":1}`)
	if rec.Code != 409 || decode[errorBody](t, rec).ErrorCode != CodeNotAvailable {
		t.Fatalf("inactive add = %d %s", rec.Code, rec.Body)
	}
	for _, body := range []string{`{"variantId":"x","quantity":1}`, `{"variantId":"0192f3a4-0000-7000-8000-0000000000cc","quantity":0}`} {
		if rec := do(testRouter(shopper, http.MethodPost, "/cart/lines", h.AddLine), http.MethodPost, "/cart/lines", body); rec.Code != 400 {
			t.Errorf("bad add %s = %d", body, rec.Code)
		}
	}
	h = NewCartHandler(&fakeCart{ownErr: repository.ErrNotFound}, 5)
	if rec := do(testRouter(shopper, http.MethodDelete, "/cart/lines/:id", h.DeleteLine), http.MethodDelete, line, ""); rec.Code != 404 {
		t.Fatalf("another user's line = %d", rec.Code)
	}
	if rec := do(testRouter(shopper, http.MethodPatch, "/cart/lines/:id", h.UpdateLine), http.MethodPatch, line, `{"quantity":2}`); rec.Code != 404 {
		t.Fatalf("another user's line = %d", rec.Code)
	}
}
