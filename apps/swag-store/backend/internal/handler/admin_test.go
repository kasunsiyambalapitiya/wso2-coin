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

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/hr"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/orders"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/repository"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/reqctx"
)

const pid = "0192f3a4-0000-7000-8000-0000000000d1"

var catalogAdmin = &reqctx.User{Email: "cm@wso2.com", UserID: "u-cm", Groups: []string{"swag-catalog"}}
var opsAdmin = &reqctx.User{Email: "ops@wso2.com", UserID: "u-ops", Groups: []string{"swag-ops"}}

// --- offices ---

type fakeOffices struct {
	offices  []repository.Office
	location string
	saved    *repository.Office
}

func (f *fakeOffices) Offices(context.Context, bool) ([]repository.Office, error) {
	return f.offices, nil
}
func (f *fakeOffices) Office(_ context.Context, id string) (*repository.Office, error) {
	for i := range f.offices {
		if f.offices[i].ID == id {
			return &f.offices[i], nil
		}
	}
	return nil, nil
}
func (f *fakeOffices) DefaultOfficeID(_ context.Context, _, location string) (string, error) {
	f.location = location
	return "o1", nil
}
func (f *fakeOffices) SaveOffice(_ context.Context, _ repository.Actor, o repository.Office) (*repository.Office, error) {
	f.saved = &o
	return &o, nil
}

func TestPickupOfficesUseHRLocationAndSurviveHRDown(t *testing.T) {
	f := &fakeOffices{offices: []repository.Office{{ID: "o1", Name: "Colombo office", Active: true, HRLocations: []string{"Colombo"}}}}
	h := NewOfficesHandler(f, &fakeEmployees{emp: &hr.Employee{Location: "Colombo"}})
	rec := do(testRouter(shopper, http.MethodGet, "/pickup-offices", h.List), http.MethodGet, "/pickup-offices", "")
	body := decode[map[string]any](t, rec)
	if rec.Code != 200 || body["defaultOfficeId"] != "o1" || f.location != "Colombo" {
		t.Fatalf("offices = %d %v loc=%q", rec.Code, body, f.location)
	}
	if strings.Contains(rec.Body.String(), "hrLocations") {
		t.Fatal("HR mapping leaked to shoppers")
	}
	h = NewOfficesHandler(f, &fakeEmployees{err: errDown})
	if rec := do(testRouter(shopper, http.MethodGet, "/pickup-offices", h.List), http.MethodGet, "/pickup-offices", ""); rec.Code != 200 {
		t.Fatalf("HR down broke the office list: %d", rec.Code)
	}
}

func TestAdminOfficeValidation(t *testing.T) {
	f := &fakeOffices{}
	h := NewOfficesHandler(f, &fakeEmployees{})
	r := testRouter(opsAdmin, http.MethodPost, "/admin/pickup-offices", h.Create)
	if rec := do(r, http.MethodPost, "/admin/pickup-offices", `{"name":""}`); rec.Code != 400 {
		t.Fatalf("empty name = %d", rec.Code)
	}
	rec := do(r, http.MethodPost, "/admin/pickup-offices", `{"name":"Colombo office","leadTimeDays":2,"hrLocations":["Colombo"],"active":true}`)
	if rec.Code != 201 || f.saved == nil || f.saved.Name != "Colombo office" || !f.saved.Active {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
}

// --- alerts ---

type fakeAlerts struct {
	err     error
	created string
}

func (f *fakeAlerts) Alerts(context.Context, string) ([]repository.Alert, error) { return nil, nil }
func (f *fakeAlerts) CreateAlert(_ context.Context, _, variantID string) (string, error) {
	f.created = variantID
	return "a1", f.err
}
func (f *fakeAlerts) CancelAlert(context.Context, string, string) error { return f.err }

func TestStockAlerts(t *testing.T) {
	h := NewAlertsHandler(&fakeAlerts{err: repository.ErrNotAvailable})
	rec := do(testRouter(shopper, http.MethodPost, "/stock-alerts", h.Create), http.MethodPost, "/stock-alerts", `{"variantId":"`+pid+`"}`)
	if rec.Code != 409 || decode[errorBody](t, rec).ErrorCode != CodeNotAvailable {
		t.Fatalf("limited product alert = %d %s", rec.Code, rec.Body)
	}
	h = NewAlertsHandler(&fakeAlerts{err: repository.ErrNotFound})
	if rec := do(testRouter(shopper, http.MethodDelete, "/stock-alerts/:id", h.Delete), http.MethodDelete, "/stock-alerts/"+pid, ""); rec.Code != 404 {
		t.Fatalf("another user's alert = %d", rec.Code)
	}
}

// --- admin catalog ---

type fakeAdminCatalog struct {
	detail    *repository.ProductDetail
	statusTo  string
	statusErr error
	updated   *repository.ProductInput
	created   *repository.ProductInput
	moveErr   error
	updateErr error // UpdateProduct and UpdateVariant
	uploads   []repository.PhotoUploadDetail
}

func (f *fakeAdminCatalog) RecordPhotoUpload(_ context.Context, _ repository.Actor, _ string, d repository.PhotoUploadDetail) error {
	f.uploads = append(f.uploads, d)
	return nil
}

func (f *fakeAdminCatalog) SearchProducts(context.Context, repository.ProductQuery) ([]repository.ProductCard, int, error) {
	return nil, 0, nil
}
func (f *fakeAdminCatalog) ProductByID(context.Context, string) (*repository.ProductDetail, error) {
	return f.detail, nil
}
func (f *fakeAdminCatalog) CreateProduct(_ context.Context, _ repository.Actor, in repository.ProductInput) (*repository.ProductDetail, error) {
	f.created = &in
	return &repository.ProductDetail{Product: repository.Product{ID: pid, Name: in.Name, Slug: in.Slug, Status: "DRAFT"}}, nil
}

// UpdateProduct patches the stored detail the way the repository does under
// its row lock: every field, the images and the curated alternatives.
func (f *fakeAdminCatalog) UpdateProduct(_ context.Context, _ repository.Actor, _ string, patch func(*repository.ProductInput) error) (*repository.ProductDetail, error) {
	if f.detail == nil {
		return nil, repository.ErrNotFound
	}
	p := f.detail.Product
	in := repository.ProductInput{Name: p.Name, Slug: p.Slug, Description: p.Description, CategoryID: p.CategoryID,
		Price: p.Price, RestockPolicy: p.RestockPolicy, MaxPerUser: p.MaxPerUser, LimitWindowDays: p.LimitWindowDays,
		Alternatives: append([]string(nil), f.detail.CuratedAlternativeIDs...)}
	for _, im := range f.detail.Images {
		in.Images = append(in.Images, repository.ImageInput{View: im.View, Path: im.Path, Alt: im.Alt})
	}
	if err := patch(&in); err != nil {
		return nil, err
	}
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	f.updated = &in
	return f.detail, nil
}
func (f *fakeAdminCatalog) SetProductStatus(_ context.Context, _ repository.Actor, _, to, _ string) error {
	f.statusTo = to
	return f.statusErr
}
func (f *fakeAdminCatalog) DeleteDraft(context.Context, repository.Actor, string) error { return nil }
func (f *fakeAdminCatalog) RemovalImpact(context.Context, string) (*repository.RemovalImpact, error) {
	return &repository.RemovalImpact{OpenUnits: 3}, nil
}
func (f *fakeAdminCatalog) SetOptions(context.Context, repository.Actor, string, []repository.OptionInput) (*repository.ProductDetail, error) {
	return f.detail, nil
}
func (f *fakeAdminCatalog) UpdateVariant(context.Context, repository.Actor, string, string, repository.VariantInput) error {
	return f.updateErr
}
func (f *fakeAdminCatalog) RecordStockMovement(context.Context, repository.Actor, string, string, int, string) (*repository.StockResult, error) {
	return &repository.StockResult{Free: 3}, f.moveErr
}
func (f *fakeAdminCatalog) Categories(context.Context) ([]repository.Category, error) {
	return nil, nil
}
func (f *fakeAdminCatalog) Category(context.Context, string) (*repository.Category, error) {
	return nil, repository.ErrNotFound
}
func (f *fakeAdminCatalog) SaveCategory(_ context.Context, _ repository.Actor, c repository.Category) (*repository.Category, error) {
	return &c, nil
}
func (f *fakeAdminCatalog) DeleteCategory(context.Context, repository.Actor, string) error {
	return repository.ErrInUse
}
func (f *fakeAdminCatalog) Demand(context.Context) ([]repository.DemandRow, error) { return nil, nil }

func adminCatalog(f *fakeAdminCatalog, cache *ListCache) *AdminCatalogHandler {
	return NewAdminCatalogHandler(f, cache, &fakePhotos{}, 5<<20)
}

func activeDetail() *repository.ProductDetail {
	return &repository.ProductDetail{Product: repository.Product{ID: pid, Slug: "hoodie", Name: "WSO2 Hoodie", Price: 90,
		Status: "ACTIVE", RestockPolicy: "RESTOCKABLE"}, Images: []repository.Image{{View: "front", Path: "products/" + pid + "/front-3f9a2c1d.jpg", Alt: "Hoodie"}}}
}

func TestAdminProductStatusPatch(t *testing.T) {
	f := &fakeAdminCatalog{detail: activeDetail()}
	cache := NewListCache()
	cache.put("k", "cached")
	h := adminCatalog(f, cache)
	r := testRouter(catalogAdmin, http.MethodPatch, "/admin/products/:id", h.Patch)

	rec := do(r, http.MethodPatch, "/admin/products/"+pid, `{"status":"REMOVED","removalNote":"Supplier stopped"}`)
	if rec.Code != 200 || f.statusTo != "REMOVED" {
		t.Fatalf("remove = %d %s", rec.Code, rec.Body)
	}
	if _, ok := cache.get("k"); ok {
		t.Fatal("removal did not clear the product-list cache")
	}
	if rec := do(r, http.MethodPatch, "/admin/products/"+pid, `{"status":"REMOVED","name":"x"}`); rec.Code != 400 {
		t.Fatalf("status mixed with fields = %d", rec.Code)
	}
	if rec := do(r, http.MethodPatch, "/admin/products/"+pid, `{"status":"SHIPPED"}`); rec.Code != 400 {
		t.Fatalf("unknown status = %d", rec.Code)
	}
	f.statusErr = repository.ErrStateConflict
	if rec := do(r, http.MethodPatch, "/admin/products/"+pid, `{"status":"ACTIVE"}`); rec.Code != 409 {
		t.Fatalf("stale status = %d", rec.Code)
	}
}

func TestAdminProductFieldPatchKeepsOtherFields(t *testing.T) {
	f := &fakeAdminCatalog{detail: activeDetail()}
	h := adminCatalog(f, NewListCache())
	r := testRouter(catalogAdmin, http.MethodPatch, "/admin/products/:id", h.Patch)
	rec := do(r, http.MethodPatch, "/admin/products/"+pid, `{"price":95}`)
	if rec.Code != 200 || f.updated == nil || f.updated.Price != 95 || f.updated.Name != "WSO2 Hoodie" || len(f.updated.Images) != 1 {
		t.Fatalf("patch = %d %+v", rec.Code, f.updated)
	}
}

func TestAdminProductValidation(t *testing.T) {
	f := &fakeAdminCatalog{}
	h := adminCatalog(f, NewListCache())
	r := testRouter(catalogAdmin, http.MethodPost, "/admin/products", h.Create)
	bad := []string{
		`{"name":"","price":90}`,
		`{"name":"Hoodie","price":0}`,
		`{"name":"Hoodie","price":90,"images":[{"view":"front","path":"products/` + pid + `/front-3f9a2c1d.jpg","alt":""}]}`,
		`{"name":"Hoodie","price":90,"description":"` + strings.Repeat("x", 2001) + `"}`,
		`{"name":"Hoodie","price":90,"restockPolicy":"SOMETIMES"}`,
		`{"name":"Hoodie","price":90,"slug":"Bad Slug"}`,
		`{"name":"Hoodie","price":90,"maxPerUser":0}`,
	}
	for _, b := range bad {
		if rec := do(r, http.MethodPost, "/admin/products", b); rec.Code != 400 {
			t.Errorf("%.60s accepted: %d", b, rec.Code)
		}
	}
	rec := do(r, http.MethodPost, "/admin/products", `{"name":"WSO2 Hoodie","price":90,"images":[]}`)
	if rec.Code != 201 || f.created.Slug != "wso2-hoodie" || f.created.RestockPolicy != "RESTOCKABLE" {
		t.Fatalf("create = %d %+v", rec.Code, f.created)
	}
}

func TestProductImagesValidation(t *testing.T) {
	photo := func(view, path, alt string) string {
		return `{"view":"` + view + `","path":"` + path + `","alt":"` + alt + `"}`
	}
	own := func(view string) string { return "products/" + pid + "/" + view + "-3f9a2c1d.jpg" }
	front := photo("front", own("front"), "")
	eleven := make([]string, 0, 11)
	for range 11 {
		eleven = append(eleven, front)
	}
	cases := map[string]struct {
		body string
		want int
	}{
		"ok, url ignored": {`{"images":[{"view":"front","path":"` + own("front") + `","url":"https://x","alt":""}]}`, 200},
		"another product": {`{"images":[` + photo("front", "products/0192f3c4-7d1e-7a40-9b2c-000000000000/front-3f9a2c1d.jpg", "") + `]}`, 400},
		"view mismatch":   {`{"images":[` + photo("back", own("front"), "") + `]}`, 400},
		"repeated view":   {`{"images":[` + front + `,` + front + `]}`, 400},
		"unknown view":    {`{"images":[` + photo("side", own("side"), "") + `]}`, 400},
		"alt over 300":    {`{"images":[` + photo("front", own("front"), strings.Repeat("a", 301)) + `]}`, 400},
		"eleven photos":   {`{"images":[` + strings.Join(eleven, ",") + `]}`, 400},
	}
	for name, tc := range cases {
		f := &fakeAdminCatalog{detail: activeDetail()}
		r := testRouter(catalogAdmin, http.MethodPatch, "/admin/products/:id", adminCatalog(f, NewListCache()).Patch)
		if rec := do(r, http.MethodPatch, "/admin/products/"+pid, tc.body); rec.Code != tc.want {
			t.Errorf("%s = %d %s", name, rec.Code, rec.Body)
		}
	}

	// Saved photos come back in view order whatever order they were sent in.
	f := &fakeAdminCatalog{detail: activeDetail()}
	r := testRouter(catalogAdmin, http.MethodPatch, "/admin/products/:id", adminCatalog(f, NewListCache()).Patch)
	rec := do(r, http.MethodPatch, "/admin/products/"+pid, `{"images":[`+photo("back", own("back"), " Logo ")+`,`+front+`]}`)
	if rec.Code != 200 || len(f.updated.Images) != 2 || f.updated.Images[0].View != "front" || f.updated.Images[1].Alt != "Logo" {
		t.Fatalf("ordered = %d %+v", rec.Code, f.updated)
	}

	c := testRouter(catalogAdmin, http.MethodPost, "/admin/products", adminCatalog(&fakeAdminCatalog{}, NewListCache()).Create)
	rec = do(c, http.MethodPost, "/admin/products", `{"name":"Hoodie","price":90,"images":[`+front+`]}`)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "Save the product before adding photos.") {
		t.Fatalf("create with photos = %d %s", rec.Code, rec.Body)
	}
}

func TestAdminProductPhotoURLs(t *testing.T) {
	d := activeDetail()
	d.Images = []repository.Image{
		{View: "back", Path: "products/" + pid + "/back-0a1b2c3d.png"},
		{View: "front", Path: "products/" + pid + "/front-3f9a2c1d.jpg", Alt: "Hoodie"},
	}
	h := NewAdminCatalogHandler(&fakeAdminCatalog{detail: d}, NewListCache(), &fakePhotos{}, 5<<20)
	rec := do(testRouter(catalogAdmin, http.MethodGet, "/admin/products/:id", h.Get), http.MethodGet, "/admin/products/"+pid, "")
	body := decode[struct {
		Images []struct {
			View, Path, Alt string
			URL             *string
		} `json:"images"`
	}](t, rec)
	if rec.Code != 200 || len(body.Images) != 2 || body.Images[0].View != "front" || body.Images[0].URL == nil ||
		*body.Images[0].URL != "https://acc.blob.core.windows.net/c/products/"+pid+"/front-3f9a2c1d.jpg?sig=x" {
		t.Fatalf("admin photos = %d %s", rec.Code, rec.Body)
	}

	off := NewAdminCatalogHandler(&fakeAdminCatalog{detail: d}, NewListCache(), &fakePhotos{off: true}, 5<<20)
	rec = do(testRouter(catalogAdmin, http.MethodGet, "/admin/products/:id", off.Get), http.MethodGet, "/admin/products/"+pid, "")
	if !strings.Contains(rec.Body.String(), `"url":null`) || !strings.Contains(rec.Body.String(), `"path":"products/`+pid+`/front-3f9a2c1d.jpg"`) {
		t.Fatalf("photos off = %s", rec.Body)
	}
}

func TestStockMovementRules(t *testing.T) {
	f := &fakeAdminCatalog{}
	h := adminCatalog(f, NewListCache())
	r := testRouter(catalogAdmin, http.MethodPost, "/admin/variants/:id/stock-movements", h.StockMovement)
	path := "/admin/variants/" + pid + "/stock-movements"
	for _, b := range []string{`{"delta":-1,"reason":"RECEIVE"}`, `{"delta":0,"reason":"ADJUST"}`, `{"delta":5,"reason":"RESERVE"}`} {
		if rec := do(r, http.MethodPost, path, b); rec.Code != 400 {
			t.Errorf("%s = %d", b, rec.Code)
		}
	}
	if rec := do(r, http.MethodPost, path, `{"delta":5,"reason":"RECEIVE","note":"Delivery"}`); rec.Code != 200 {
		t.Fatalf("receive = %d %s", rec.Code, rec.Body)
	}
	f.moveErr = repository.ErrStateConflict
	if rec := do(r, http.MethodPost, path, `{"delta":-50,"reason":"ADJUST"}`); rec.Code != 409 {
		t.Fatalf("below reserved = %d", rec.Code)
	}
}

func TestCategoryDeleteInUse(t *testing.T) {
	h := adminCatalog(&fakeAdminCatalog{}, NewListCache())
	rec := do(testRouter(catalogAdmin, http.MethodDelete, "/admin/categories/:id", h.DeleteCategory), http.MethodDelete, "/admin/categories/"+pid, "")
	if rec.Code != 409 || decode[errorBody](t, rec).ErrorCode != CodeStateConflict {
		t.Fatalf("in-use delete = %d %s", rec.Code, rec.Body)
	}
}

// --- admin orders ---

type fakeAdminOrders struct {
	list       []repository.Order
	transition orders.Status
	err        error
}

func (f *fakeAdminOrders) SearchOrders(context.Context, repository.OrderFilter) ([]repository.Order, int, error) {
	return f.list, len(f.list), nil
}
func (f *fakeAdminOrders) OrderDetail(_ context.Context, no, email string) (*repository.OrderDetail, error) {
	if email != "" {
		panic("admin read scoped to a user")
	}
	return &repository.OrderDetail{Order: repository.Order{OrderNo: no, Status: orders.Confirmed}}, nil
}
func (f *fakeAdminOrders) TransitionOrder(_ context.Context, no string, to orders.Status, _ repository.Actor, _ string) (*repository.Order, error) {
	f.transition = to
	return &repository.Order{OrderNo: no, Status: to}, f.err
}
func (f *fakeAdminOrders) AddOrderNote(context.Context, string, repository.Actor, string) error {
	return nil
}

func TestCollectedNeedsIDCheck(t *testing.T) {
	f := &fakeAdminOrders{}
	h := NewAdminOrdersHandler(f)
	r := testRouter(opsAdmin, http.MethodPatch, "/admin/orders/:orderNo", h.Patch)
	if rec := do(r, http.MethodPatch, "/admin/orders/SS-10001", `{"status":"COLLECTED"}`); rec.Code != 400 || f.transition != "" {
		t.Fatalf("collected without id check = %d", rec.Code)
	}
	if rec := do(r, http.MethodPatch, "/admin/orders/SS-10001", `{"status":"COLLECTED","idChecked":true}`); rec.Code != 200 || f.transition != orders.Collected {
		t.Fatalf("collected = %d", rec.Code)
	}
	if rec := do(r, http.MethodPatch, "/admin/orders/SS-10001", `{"status":"CONFIRMED"}`); rec.Code != 400 {
		t.Fatalf("admin may only set ready/collected: %d", rec.Code)
	}
	f.err = repository.ErrStateConflict
	if rec := do(r, http.MethodPatch, "/admin/orders/SS-10001", `{"status":"READY_FOR_PICKUP"}`); rec.Code != 409 {
		t.Fatalf("stale CAS = %d", rec.Code)
	}
}

func TestOrderSearchCSVEscapesFormulas(t *testing.T) {
	f := &fakeAdminOrders{list: []repository.Order{{OrderNo: "SS-10001", Status: orders.Confirmed, UserEmail: "you@wso2.com",
		UserName: "=HYPERLINK(\"x\")", OfficeName: "Colombo", Total: 90, PlacedAt: time.Unix(1_800_000_000, 0)}}}
	h := NewAdminOrdersHandler(f)
	r := testRouter(opsAdmin, http.MethodPost, "/admin/orders/search", h.Search)
	rec := do(r, http.MethodPost, "/admin/orders/search", `{"filters":{"status":"CONFIRMED"},"pagination":{"limit":10}}`, "Accept", "text/csv")
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/csv") {
		t.Fatalf("csv = %d %v", rec.Code, rec.Header())
	}
	if !strings.Contains(rec.Body.String(), `'=HYPERLINK`) || !strings.Contains(rec.Body.String(), "SS-10001") {
		t.Fatalf("csv body = %s", rec.Body)
	}
	rec = do(r, http.MethodPost, "/admin/orders/search", `{"filters":{"status":"SHIPPED"}}`)
	if rec.Code != 400 {
		t.Fatalf("bad status filter = %d", rec.Code)
	}
	rec = do(r, http.MethodPost, "/admin/orders/search", `{"filters":{},"pagination":{"limit":10}}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"userEmail":"you@wso2.com"`) {
		t.Fatalf("json search = %d %s", rec.Code, rec.Body)
	}
}

// --- dashboard ---

type fakeDashboard struct{ catalog, ops bool }

func (f *fakeDashboard) Dashboard(_ context.Context, c, o bool, _ int, _ time.Time) (*repository.Dashboard, error) {
	f.catalog, f.ops = c, o
	d := &repository.Dashboard{}
	if c {
		d.Catalog = &repository.CatalogCounts{Active: 7}
	}
	if o {
		d.Orders = &repository.OrderCounts{ToPrepare: 2}
	}
	return d, nil
}
func (f *fakeDashboard) AuditEvents(context.Context, int, int) ([]repository.AuditEvent, int, error) {
	return nil, 0, nil
}

func TestDashboardFilteredByRole(t *testing.T) {
	f := &fakeDashboard{}
	h := NewAdminHandler(NewAccessGuard(testAccess()), f, 5, time.UTC)
	rec := do(testRouter(opsAdmin, http.MethodGet, "/admin/dashboard", h.Dashboard), http.MethodGet, "/admin/dashboard", "")
	if rec.Code != 200 || f.catalog || !f.ops || strings.Contains(rec.Body.String(), `"catalog"`) {
		t.Fatalf("ops dashboard = %d %s (catalog=%v ops=%v)", rec.Code, rec.Body, f.catalog, f.ops)
	}
}

func TestFieldPatchKeepsCuratedAlternativesOnly(t *testing.T) {
	d := activeDetail()
	d.Alternatives = []repository.ProductCard{{ID: "0192f3a4-0000-7000-8000-0000000000e1"}} // same-category fallback
	d.CuratedAlternativeIDs = []string{"0192f3a4-0000-7000-8000-0000000000e2"}              // curated, maybe not ACTIVE
	f := &fakeAdminCatalog{detail: d}
	h := adminCatalog(f, NewListCache())
	rec := do(testRouter(catalogAdmin, http.MethodPatch, "/admin/products/:id", h.Patch), http.MethodPatch, "/admin/products/"+pid, `{"price":95}`)
	if rec.Code != 200 || len(f.updated.Alternatives) != 1 || f.updated.Alternatives[0] != "0192f3a4-0000-7000-8000-0000000000e2" {
		t.Fatalf("alternatives = %v", f.updated.Alternatives)
	}
}

func TestFieldPatchNullCategoryClearsIt(t *testing.T) {
	d := activeDetail()
	d.Product.CategoryID = "0192f3a4-0000-7000-8000-0000000000c1"
	f := &fakeAdminCatalog{detail: d}
	r := testRouter(catalogAdmin, http.MethodPatch, "/admin/products/:id", adminCatalog(f, NewListCache()).Patch)
	if rec := do(r, http.MethodPatch, "/admin/products/"+pid, `{"price":95}`); rec.Code != 200 || f.updated.CategoryID != d.Product.CategoryID {
		t.Fatalf("absent categoryId = %d, category %q, want it kept", rec.Code, f.updated.CategoryID)
	}
	if rec := do(r, http.MethodPatch, "/admin/products/"+pid, `{"categoryId":null}`); rec.Code != 200 || f.updated.CategoryID != "" {
		t.Fatalf("categoryId null = %d, category %q, want it cleared", rec.Code, f.updated.CategoryID)
	}
	if rec := do(r, http.MethodPatch, "/admin/products/"+pid, `{"status":"ACTIVE","categoryId":null}`); rec.Code != 400 {
		t.Fatalf("status with categoryId null = %d, want 400", rec.Code)
	}
}

func TestPriceTooLowIsABadRequest(t *testing.T) {
	f := &fakeAdminCatalog{detail: activeDetail(), updateErr: repository.ErrPriceTooLow}
	h := adminCatalog(f, NewListCache())
	if rec := do(testRouter(catalogAdmin, http.MethodPatch, "/admin/products/:id", h.Patch), http.MethodPatch, "/admin/products/"+pid, `{"price":1}`); rec.Code != 400 {
		t.Fatalf("product patch = %d %s, want 400", rec.Code, rec.Body)
	}
	vid := "0192f3a4-0000-7000-8000-0000000000d1"
	rec := do(testRouter(catalogAdmin, http.MethodPatch, "/admin/products/:id/variants/:variantId", h.PatchVariant),
		http.MethodPatch, "/admin/products/"+pid+"/variants/"+vid, `{"priceDelta":-90}`)
	if rec.Code != 400 {
		t.Fatalf("variant patch = %d %s, want 400", rec.Code, rec.Body)
	}
}

func TestFieldPatchOfUnknownProductIs404(t *testing.T) {
	f := &fakeAdminCatalog{}
	rec := do(testRouter(catalogAdmin, http.MethodPatch, "/admin/products/:id", adminCatalog(f, NewListCache()).Patch), http.MethodPatch, "/admin/products/"+pid, `{"price":95}`)
	if rec.Code != 404 {
		t.Fatalf("= %d, want 404", rec.Code)
	}
}
