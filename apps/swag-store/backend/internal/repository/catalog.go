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

package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/catalog"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/store"
)

// Category is a shop category.
type Category struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
	Sort int    `json:"sort"`
}

// ActiveCategories lists categories that hold at least one ACTIVE product.
func (s *Store) ActiveCategories(ctx context.Context) ([]Category, error) {
	rows, err := s.q.ListActiveCategories(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Category, 0, len(rows))
	for _, r := range rows {
		out = append(out, Category{ID: r.ID, Slug: r.Slug, Name: r.Name, Sort: int(r.Sort)})
	}
	return out, nil
}

// Categories lists every category (admin).
func (s *Store) Categories(ctx context.Context) ([]Category, error) {
	rows, err := s.q.ListCategories(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Category, 0, len(rows))
	for _, r := range rows {
		out = append(out, Category{ID: r.ID, Slug: r.Slug, Name: r.Name, Sort: int(r.Sort)})
	}
	return out, nil
}

// ProductQuery filters a product list. Status empty means ACTIVE (the shop).
type ProductQuery struct {
	Q            string
	CategorySlug string
	Sort         string // popular | price_asc | price_desc | newest
	InStock      bool
	Status       string
	Limit        int
	Offset       int
}

// ProductCard is one product in a list.
type ProductCard struct {
	ID            string
	Slug          string
	Name          string
	Status        string
	RestockPolicy string
	CategoryID    string
	Price         int64 // lowest price over active variants
	MaxFree       int   // most free stock of any active variant
	UnitsSold     int
	ImageURL      string
	PublishedAt   *time.Time
	CreatedAt     time.Time
}

var productSorts = map[string]string{
	"popular":    "units_sold DESC, p.published_at DESC, p.id",
	"price_asc":  "price ASC, p.name, p.id",
	"price_desc": "price DESC, p.name, p.id",
	"newest":     "p.published_at DESC, p.created_at DESC, p.id",
	"name":       "p.name, p.id",
}

// likePattern escapes LIKE wildcards in a user's search text.
func likePattern(q string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(q) + "%"
}

const cardSelect = `SELECT p.id, p.slug, p.name, p.status, p.restock_policy, COALESCE(p.category_id, ''),
    p.price_coins + COALESCE(MIN(v.price_delta_coins), 0) AS price,
    COALESCE(MAX(v.stock_on_hand - v.stock_reserved), 0) AS max_free,
    COALESCE(st.units_sold_30d, 0) AS units_sold, p.published_at, p.created_at
FROM swag_products p
LEFT JOIN swag_variants v ON v.product_id = p.id AND v.active = TRUE
LEFT JOIN swag_product_stats st ON st.product_id = p.id
LEFT JOIN swag_categories c ON c.id = p.category_id`

const cardGroup = ` GROUP BY p.id, st.units_sold_30d`

// SearchProducts lists products with filters, sort and paging, and the total.
// Every value is a parameter; the sort is chosen from a fixed list.
func (s *Store) SearchProducts(ctx context.Context, pq ProductQuery) ([]ProductCard, int, error) {
	status := pq.Status
	if status == "" {
		status = catalog.StatusActive
	}
	where := []string{"p.status = ?"}
	args := []any{status}
	if pq.CategorySlug != "" {
		where = append(where, "c.slug = ?")
		args = append(args, pq.CategorySlug)
	}
	if q := strings.TrimSpace(pq.Q); q != "" {
		where = append(where, "(p.name LIKE ? OR p.description LIKE ?)")
		args = append(args, likePattern(q), likePattern(q))
	}
	having := ""
	if pq.InStock {
		having = " HAVING max_free > 0"
	}
	base := cardSelect + " WHERE " + strings.Join(where, " AND ") + cardGroup + having
	var total int
	// #nosec G202 -- the SQL is built from fixed fragments; every value is a parameter.
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM ("+base+") t", args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	order, ok := productSorts[pq.Sort]
	if !ok {
		order = productSorts["popular"]
	}
	cards, err := s.queryCards(ctx, base+" ORDER BY "+order+" LIMIT ? OFFSET ?", append(args, pq.Limit, pq.Offset)...)
	return cards, total, err
}

func (s *Store) queryCards(ctx context.Context, query string, args ...any) ([]ProductCard, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProductCard
	var ids []string
	for rows.Next() {
		var c ProductCard
		var published sql.NullTime
		if err := rows.Scan(&c.ID, &c.Slug, &c.Name, &c.Status, &c.RestockPolicy, &c.CategoryID, &c.Price,
			&c.MaxFree, &c.UnitsSold, &published, &c.CreatedAt); err != nil {
			return nil, err
		}
		c.PublishedAt = timePtr(published)
		out = append(out, c)
		ids = append(ids, c.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	images, err := s.firstImages(ctx, s.q, ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].ImageURL = images[out[i].ID]
	}
	return out, nil
}

// Option is a product option with its values.
type Option struct {
	ID     string
	Name   string
	Sort   int
	Values []OptionValue
}

// OptionValue is one value of an option.
type OptionValue struct {
	ID        string
	Value     string
	SwatchHex string
	Sort      int
}

// Variant is one sellable combination.
type Variant struct {
	ID         string
	SKU        string
	PriceDelta int64
	OnHand     int
	Reserved   int
	Free       int
	RestockETA *time.Time
	Active     bool
	ValueIDs   []string
	Options    string
}

// Image is a product image.
type Image struct {
	ID            string
	URL           string
	Alt           string
	OptionValueID string
	Sort          int
}

// Product is the product row as handlers use it.
type Product struct {
	ID              string
	Slug            string
	Name            string
	Description     string
	CategoryID      string
	Price           int64
	RestockPolicy   string
	MaxPerUser      *int
	LimitWindowDays *int
	Status          string
	PublishedAt     *time.Time
	RemovedAt       *time.Time
	RemovedBy       string
	RemovalNote     string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// ProductDetail is a product with options, variants, images and alternatives.
type ProductDetail struct {
	Product      Product
	Options      []Option
	Variants     []Variant
	Images       []Image
	Alternatives []ProductCard // what the shop shows: curated ACTIVE ones, else the category fallback
	// CuratedAlternativeIDs is the stored list, in order, whatever each product's status.
	CuratedAlternativeIDs []string
}

func productFrom(p store.Product) Product {
	return Product{ID: p.ID, Slug: p.Slug, Name: p.Name, Description: p.Description, CategoryID: p.CategoryID.String,
		Price: p.PriceCoins, RestockPolicy: string(p.RestockPolicy), MaxPerUser: intPtr(p.MaxPerUser),
		LimitWindowDays: intPtr(p.LimitWindowDays), Status: string(p.Status), PublishedAt: timePtr(p.PublishedAt),
		RemovedAt: timePtr(p.RemovedAt), RemovedBy: p.RemovedBy.String, RemovalNote: p.RemovalNote.String,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}
}

// ProductBySlug returns a product's detail, or nil.
func (s *Store) ProductBySlug(ctx context.Context, slug string) (*ProductDetail, error) {
	p, err := s.q.GetProductBySlug(ctx, slug)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.detail(ctx, s.q, p)
}

// ProductByID returns a product's detail, or nil.
func (s *Store) ProductByID(ctx context.Context, id string) (*ProductDetail, error) {
	p, err := s.q.GetProduct(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.detail(ctx, s.q, p)
}

func (s *Store) detail(ctx context.Context, q *store.Queries, p store.Product) (*ProductDetail, error) {
	d := &ProductDetail{Product: productFrom(p)}
	opts, err := q.ListProductOptions(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	vals, err := q.ListOptionValuesByProduct(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	byOption := map[string][]OptionValue{}
	valueName := map[string]string{}
	for _, v := range vals {
		byOption[v.OptionID] = append(byOption[v.OptionID], OptionValue{ID: v.ID, Value: v.Value, SwatchHex: v.SwatchHex.String, Sort: int(v.Sort)})
		valueName[v.ID] = v.Value
	}
	optionOf := map[string]int{}
	for i, o := range opts {
		d.Options = append(d.Options, Option{ID: o.ID, Name: o.Name, Sort: int(o.Sort), Values: byOption[o.ID]})
		for _, v := range byOption[o.ID] {
			optionOf[v.ID] = i
		}
	}
	links, err := q.ListVariantValuesByProduct(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	valuesOf := map[string][]string{}
	for _, l := range links {
		valuesOf[l.VariantID] = append(valuesOf[l.VariantID], l.OptionValueID)
	}
	variants, err := q.ListVariantsByProduct(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	for _, v := range variants {
		ids := valuesOf[v.ID]
		sortByOption(ids, optionOf)
		names := make([]string, 0, len(ids))
		for _, id := range ids {
			names = append(names, valueName[id])
		}
		d.Variants = append(d.Variants, Variant{ID: v.ID, SKU: v.Sku, PriceDelta: v.PriceDeltaCoins, OnHand: int(v.StockOnHand),
			Reserved: int(v.StockReserved), Free: int(v.StockOnHand - v.StockReserved), RestockETA: timePtr(v.RestockEta),
			Active: v.Active, ValueIDs: ids, Options: catalog.OptionsLabel(names)})
	}
	images, err := q.ListImagesByProduct(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	for _, im := range images {
		d.Images = append(d.Images, Image{ID: im.ID, URL: im.Url, Alt: im.Alt, OptionValueID: im.OptionValueID.String, Sort: int(im.Sort)})
	}
	if d.Alternatives, err = s.alternatives(ctx, q, p); err != nil {
		return nil, err
	}
	if d.CuratedAlternativeIDs, err = q.ListAlternativeIDs(ctx, p.ID); err != nil {
		return nil, err
	}
	return d, nil
}

func sortByOption(ids []string, optionOf map[string]int) {
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && optionOf[ids[j]] < optionOf[ids[j-1]]; j-- {
			ids[j], ids[j-1] = ids[j-1], ids[j]
		}
	}
}

// alternatives are the curated ACTIVE alternatives, else up to four ACTIVE
// products from the same category with stock, most popular first.
func (s *Store) alternatives(ctx context.Context, q *store.Queries, p store.Product) ([]ProductCard, error) {
	ids, err := q.ListAlternativeIDs(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	if len(ids) > 0 {
		args := []any{}
		for _, id := range ids {
			args = append(args, id)
		}
		// #nosec G202 -- placeholders only.
		cards, err := s.queryCards(ctx, cardSelect+" WHERE p.status = 'ACTIVE' AND p.id IN ("+placeholders(len(ids))+")"+cardGroup, args...)
		if err != nil {
			return nil, err
		}
		order := map[string]int{}
		for i, id := range ids {
			order[id] = i
		}
		for i := 1; i < len(cards); i++ {
			for j := i; j > 0 && order[cards[j].ID] < order[cards[j-1].ID]; j-- {
				cards[j], cards[j-1] = cards[j-1], cards[j]
			}
		}
		if len(cards) > 0 {
			return cards, nil
		}
	}
	if !p.CategoryID.Valid {
		return []ProductCard{}, nil
	}
	cards, err := s.queryCards(ctx, cardSelect+" WHERE p.status = 'ACTIVE' AND p.category_id = ? AND p.id <> ?"+cardGroup+
		" HAVING max_free > 0 ORDER BY units_sold DESC, p.id LIMIT 4", p.CategoryID.String, p.ID)
	if cards == nil {
		cards = []ProductCard{}
	}
	return cards, err
}
