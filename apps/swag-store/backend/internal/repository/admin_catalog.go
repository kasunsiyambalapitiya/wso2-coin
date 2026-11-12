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
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/catalog"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/notifications"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/store"
)

// ProductInput is the editable part of a product. Images and alternatives
// are replaced as a whole.
type ProductInput struct {
	Name            string
	Slug            string
	Description     string
	CategoryID      string
	Price           int64
	RestockPolicy   string
	MaxPerUser      *int
	LimitWindowDays *int
	Images          []ImageInput
	Alternatives    []string
}

// ImageInput is one gallery image.
type ImageInput struct {
	URL string
	Alt string
}

func (s *Store) writeImagesAndAlternatives(ctx context.Context, q *store.Queries, productID string, in ProductInput) error {
	if err := q.DeleteProductImages(ctx, productID); err != nil {
		return err
	}
	for i, im := range in.Images {
		if err := q.InsertImage(ctx, store.InsertImageParams{ID: newID(), ProductID: productID, Url: im.URL, Alt: im.Alt, Sort: int32(i)}); err != nil { // #nosec G115
			return err
		}
	}
	if err := q.DeleteAlternativesOf(ctx, productID); err != nil {
		return err
	}
	for i, alt := range in.Alternatives {
		if alt == productID {
			continue
		}
		if err := q.InsertAlternative(ctx, store.InsertAlternativeParams{ProductID: productID, AlternativeID: alt, Sort: int32(i)}); err != nil { // #nosec G115
			if mysqlNumber(err) == 1452 { // ER_NO_REFERENCED_ROW_2: unknown product id
				return fmt.Errorf("%w: alternative %s", ErrNotFound, alt)
			}
			return err
		}
	}
	return nil
}

// CreateProduct inserts a DRAFT product with one default variant.
func (s *Store) CreateProduct(ctx context.Context, a Actor, in ProductInput) (*ProductDetail, error) {
	id := newID()
	err := s.inTx(ctx, func(q *store.Queries) error {
		err := q.CreateProduct(ctx, store.CreateProductParams{ID: id, Slug: in.Slug, Name: in.Name, Description: in.Description,
			CategoryID: nullStr(in.CategoryID), PriceCoins: in.Price, RestockPolicy: store.RestockPolicy(in.RestockPolicy),
			MaxPerUser: nullInt(in.MaxPerUser), LimitWindowDays: nullInt(in.LimitWindowDays)})
		if isDuplicate(err) {
			return ErrConflict
		}
		if mysqlNumber(err) == 1452 {
			return fmt.Errorf("%w: category", ErrNotFound)
		}
		if err != nil {
			return err
		}
		sku, err := uniqueSKU(ctx, q, skuBase(in.Slug, nil))
		if err != nil {
			return err
		}
		if err := q.InsertVariant(ctx, store.InsertVariantParams{ID: newID(), ProductID: id, Sku: sku}); err != nil {
			return err
		}
		if err := s.writeImagesAndAlternatives(ctx, q, id, in); err != nil {
			return err
		}
		return audit(ctx, q, a, "product_created", "product", id, map[string]any{"name": in.Name})
	})
	if err != nil {
		return nil, err
	}
	return s.ProductByID(ctx, id)
}

// UpdateProduct edits the fields, images and curated alternatives. patch gets
// the product as it is under the row lock and changes it in place, so two
// edits of different fields never undo each other. An error from patch is
// returned as it is and nothing is written. patch may run again when the
// transaction is retried.
func (s *Store) UpdateProduct(ctx context.Context, a Actor, id string, patch func(*ProductInput) error) (*ProductDetail, error) {
	err := s.inTx(ctx, func(q *store.Queries) error {
		p, err := q.LockProduct(ctx, id)
		if err != nil {
			return notFound(err)
		}
		in, err := inputOf(ctx, q, p)
		if err != nil {
			return err
		}
		if err := patch(&in); err != nil {
			return err
		}
		if err := checkVariantPrices(ctx, q, id, in.Price); err != nil {
			return err
		}
		_, err = q.UpdateProduct(ctx, store.UpdateProductParams{Slug: in.Slug, Name: in.Name, Description: in.Description,
			CategoryID: nullStr(in.CategoryID), PriceCoins: in.Price, RestockPolicy: store.RestockPolicy(in.RestockPolicy),
			MaxPerUser: nullInt(in.MaxPerUser), LimitWindowDays: nullInt(in.LimitWindowDays), ID: id})
		if isDuplicate(err) {
			return ErrConflict
		}
		if mysqlNumber(err) == 1452 {
			return fmt.Errorf("%w: category", ErrNotFound)
		}
		if err != nil {
			return err
		}
		if err := s.writeImagesAndAlternatives(ctx, q, id, in); err != nil {
			return err
		}
		return audit(ctx, q, a, "product_updated", "product", id, map[string]any{"name": in.Name, "price": in.Price})
	})
	if err != nil {
		return nil, err
	}
	return s.ProductByID(ctx, id)
}

// inputOf is the editable part of a locked product row.
func inputOf(ctx context.Context, q *store.Queries, row store.Product) (ProductInput, error) {
	p := productFrom(row)
	in := ProductInput{Name: p.Name, Slug: p.Slug, Description: p.Description, CategoryID: p.CategoryID, Price: p.Price,
		RestockPolicy: p.RestockPolicy, MaxPerUser: p.MaxPerUser, LimitWindowDays: p.LimitWindowDays}
	images, err := q.ListImagesByProduct(ctx, p.ID)
	if err != nil {
		return in, err
	}
	for _, im := range images {
		in.Images = append(in.Images, ImageInput{URL: im.Url, Alt: im.Alt})
	}
	in.Alternatives, err = q.ListAlternativeIDs(ctx, p.ID)
	return in, err
}

// checkVariantPrices returns ErrPriceTooLow when a product price of price
// would make any of its variants cost less than 1 coin.
func checkVariantPrices(ctx context.Context, q *store.Queries, productID string, price int64) error {
	variants, err := q.ListVariantsByProduct(ctx, productID)
	if err != nil {
		return err
	}
	for _, v := range variants {
		if price+v.PriceDeltaCoins < 1 {
			return ErrPriceTooLow
		}
	}
	return nil
}

// SetProductStatus publishes (DRAFT→ACTIVE), removes (ACTIVE→REMOVED) or
// restores (REMOVED→ACTIVE). A removal closes the product's pending alerts,
// each with a "no longer sold" email (backend-spec §8.5). Anything else is
// ErrStateConflict.
func (s *Store) SetProductStatus(ctx context.Context, a Actor, id, to, note string) error {
	return s.inTx(ctx, func(q *store.Queries) error {
		p, err := q.LockProduct(ctx, id)
		if err != nil {
			return notFound(err)
		}
		from := string(p.Status)
		var n int64
		switch {
		case from == catalog.StatusDraft && to == catalog.StatusActive:
			n, err = q.PublishProduct(ctx, id)
		case from == catalog.StatusActive && to == catalog.StatusRemoved:
			n, err = q.RemoveProduct(ctx, store.RemoveProductParams{RemovedBy: nullStr(a.Email), RemovalNote: nullStr(note), ID: id})
		case from == catalog.StatusRemoved && to == catalog.StatusActive:
			n, err = q.RestoreProduct(ctx, id)
		default:
			return ErrStateConflict
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrStateConflict
		}
		if to == catalog.StatusRemoved {
			alerts, err := q.PendingAlertsForProduct(ctx, id)
			if err != nil {
				return err
			}
			labels, err := s.optionLabels(ctx, q, alertVariantIDs(alerts))
			if err != nil {
				return err
			}
			for _, al := range alerts {
				if err := q.MarkAlertClosed(ctx, al.ID); err != nil {
					return err
				}
				if err := enqueue(ctx, q, notifications.TemplateProductRemoved, al.UserEmail, notifications.AlertEmail{
					ProductName: p.Name, Options: labels[al.VariantID], ProductSlug: p.Slug,
				}, "removed:"+al.ID); err != nil {
					return err
				}
			}
		}
		action := map[string]string{catalog.StatusActive: "product_published", catalog.StatusRemoved: "product_removed"}[to]
		if from == catalog.StatusRemoved {
			action = "product_restored"
		}
		return audit(ctx, q, a, action, "product", id, map[string]any{"from": from, "note": note})
	})
}

func alertVariantIDs(alerts []store.StockAlert) []string {
	ids := make([]string, 0, len(alerts))
	for _, a := range alerts {
		ids = append(ids, a.VariantID)
	}
	return ids
}

// DeleteDraft deletes a DRAFT that was never ordered, with everything under it.
func (s *Store) DeleteDraft(ctx context.Context, a Actor, id string) error {
	return s.inTx(ctx, func(q *store.Queries) error {
		p, err := q.LockProduct(ctx, id)
		if err != nil {
			return notFound(err)
		}
		if string(p.Status) != catalog.StatusDraft {
			return ErrStateConflict
		}
		if n, err := q.CountOrderLinesForProduct(ctx, id); err != nil || n > 0 {
			if err == nil {
				err = ErrStateConflict
			}
			return err
		}
		steps := []func(context.Context, string) error{q.DeleteProductImages, q.DeleteProductVariantValues,
			q.DeleteProductMovements}
		for _, step := range steps {
			if err := step(ctx, id); err != nil {
				return err
			}
		}
		for _, step := range []func(context.Context, string) error{q.DeleteProductVariants, q.DeleteProductOptionValues,
			q.DeleteProductOptions, q.DeleteProductStats} {
			if err := step(ctx, id); err != nil {
				return err
			}
		}
		if err := q.DeleteProductAlternatives(ctx, store.DeleteProductAlternativesParams{ProductID: id, AlternativeID: id}); err != nil {
			return err
		}
		if _, err := q.DeleteProductRow(ctx, id); err != nil {
			if isReferenced(err) {
				return ErrInUse
			}
			return err
		}
		return audit(ctx, q, a, "product_deleted", "product", id, map[string]any{"name": p.Name})
	})
}

// OptionInput is one option with its values, in display order.
type OptionInput struct {
	Name   string
	Values []OptionValueInput
}

// OptionValueInput is one value; SwatchHex is "#RRGGBB" or empty.
type OptionValueInput struct {
	Value     string
	SwatchHex string
}

// SetOptions replaces the option list and regenerates the variant matrix.
// Options and values are matched by name, so their ids, and the SKUs and
// stock of every variant whose combination still exists, are kept. A variant
// whose combination is gone is deactivated, never deleted.
func (s *Store) SetOptions(ctx context.Context, a Actor, productID string, opts []OptionInput) (*ProductDetail, error) {
	err := s.inTx(ctx, func(q *store.Queries) error {
		p, err := q.LockProduct(ctx, productID)
		if err != nil {
			return notFound(err)
		}
		existingOpts, err := q.ListProductOptions(ctx, productID)
		if err != nil {
			return err
		}
		existingVals, err := q.ListOptionValuesByProduct(ctx, productID)
		if err != nil {
			return err
		}
		optID := map[string]string{}
		for _, o := range existingOpts {
			optID[o.Name] = o.ID
		}
		valID := map[string]string{} // optionID + "\x00" + value → id
		for _, v := range existingVals {
			valID[v.OptionID+"\x00"+v.Value] = v.ID
		}
		var matrix [][]string // value ids per option
		for i, o := range opts {
			id, ok := optID[o.Name]
			if !ok {
				id = newID()
				if err := q.InsertOption(ctx, store.InsertOptionParams{ID: id, ProductID: productID, Name: o.Name, Sort: int32(i)}); err != nil { // #nosec G115
					return err
				}
			} else if err := q.UpdateOptionSort(ctx, store.UpdateOptionSortParams{Sort: int32(i), ID: id}); err != nil { // #nosec G115
				return err
			}
			var ids []string
			for j, v := range o.Values {
				vid, ok := valID[id+"\x00"+v.Value]
				if !ok {
					vid = newID()
					if err := q.InsertOptionValue(ctx, store.InsertOptionValueParams{ID: vid, OptionID: id, Value: v.Value,
						SwatchHex: nullStr(v.SwatchHex), Sort: int32(j)}); err != nil { // #nosec G115
						return err
					}
				} else if err := q.UpdateOptionValue(ctx, store.UpdateOptionValueParams{SwatchHex: nullStr(v.SwatchHex), Sort: int32(j), ID: vid}); err != nil { // #nosec G115
					return err
				}
				ids = append(ids, vid)
			}
			matrix = append(matrix, ids)
		}
		wanted := map[string][]string{}
		for _, combo := range catalog.VariantMatrix(matrix) {
			wanted[comboKey(combo)] = combo
		}
		links, err := q.ListVariantValuesByProduct(ctx, productID)
		if err != nil {
			return err
		}
		valuesOf := map[string][]string{}
		for _, l := range links {
			valuesOf[l.VariantID] = append(valuesOf[l.VariantID], l.OptionValueID)
		}
		variants, err := q.ListVariantsByProduct(ctx, productID)
		if err != nil {
			return err
		}
		have := map[string]bool{}
		for _, v := range variants {
			key := comboKey(valuesOf[v.ID])
			_, keep := wanted[key]
			if keep && !have[key] {
				have[key] = true
				if !v.Active {
					if err := q.SetVariantActive(ctx, store.SetVariantActiveParams{Active: true, ID: v.ID}); err != nil {
						return err
					}
				}
				continue
			}
			if v.Active {
				if err := q.SetVariantActive(ctx, store.SetVariantActiveParams{Active: false, ID: v.ID}); err != nil {
					return err
				}
			}
		}
		// Value names for new SKUs, from the inputs in matrix order.
		names := map[string]string{}
		for i, o := range opts {
			for j, v := range o.Values {
				names[matrix[i][j]] = v.Value
			}
		}
		keys := make([]string, 0, len(wanted))
		for k := range wanted {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if have[k] {
				continue
			}
			combo := wanted[k]
			parts := make([]string, 0, len(combo))
			for _, id := range combo {
				parts = append(parts, names[id])
			}
			sku, err := uniqueSKU(ctx, q, skuBase(p.Slug, parts))
			if err != nil {
				return err
			}
			vid := newID()
			if err := q.InsertVariant(ctx, store.InsertVariantParams{ID: vid, ProductID: productID, Sku: sku}); err != nil {
				return err
			}
			for _, id := range combo {
				if err := q.InsertVariantValue(ctx, store.InsertVariantValueParams{VariantID: vid, OptionValueID: id}); err != nil {
					return err
				}
			}
		}
		return audit(ctx, q, a, "product_options_set", "product", productID, map[string]any{"options": len(opts), "variants": len(wanted)})
	})
	if err != nil {
		return nil, err
	}
	return s.ProductByID(ctx, productID)
}

func comboKey(ids []string) string {
	c := append([]string{}, ids...)
	sort.Strings(c)
	return strings.Join(c, "|")
}

// skuBase builds "WSO2-HOODIE-BLACK-M" from a slug and value names.
func skuBase(slug string, values []string) string {
	parts := []string{strings.ToUpper(slug)}
	for _, v := range values {
		if s := strings.ToUpper(catalog.Slugify(v)); s != "" {
			parts = append(parts, s)
		}
	}
	sku := strings.Join(parts, "-")
	if len(sku) > 56 {
		sku = sku[:56]
	}
	return sku
}

func uniqueSKU(ctx context.Context, q *store.Queries, base string) (string, error) {
	sku := base
	for i := 2; i < 100; i++ {
		n, err := q.SkuExists(ctx, sku)
		if err != nil {
			return "", err
		}
		if n == 0 {
			return sku, nil
		}
		sku = fmt.Sprintf("%s-%d", base, i)
	}
	return "", fmt.Errorf("%w: no free SKU for %s", ErrConflict, base)
}

// VariantInput is a variant edit. Nil fields are left as they are; ClearETA
// removes the restock date.
type VariantInput struct {
	SKU        *string
	PriceDelta *int64
	Active     *bool
	RestockETA *time.Time
	ClearETA   bool
}

// UpdateVariant edits one variant of a product. The product is locked first
// (the order every writer uses), so a concurrent repricing cannot take the
// variant below 1 coin.
func (s *Store) UpdateVariant(ctx context.Context, a Actor, productID, variantID string, in VariantInput) error {
	return s.inTx(ctx, func(q *store.Queries) error {
		p, err := q.LockProduct(ctx, productID)
		if err != nil {
			return notFound(err)
		}
		v, err := q.LockVariant(ctx, variantID)
		if err != nil || v.ProductID != productID {
			if err == nil {
				err = ErrNotFound
			}
			return notFound(err)
		}
		if in.SKU != nil {
			v.Sku = *in.SKU
		}
		if in.PriceDelta != nil {
			v.PriceDeltaCoins = *in.PriceDelta
		}
		if in.Active != nil {
			v.Active = *in.Active
		}
		if in.RestockETA != nil {
			v.RestockEta = nullTime(in.RestockETA)
		}
		if in.ClearETA {
			v.RestockEta = sql.NullTime{}
		}
		if p.PriceCoins+v.PriceDeltaCoins < 1 {
			return ErrPriceTooLow
		}
		_, err = q.UpdateVariant(ctx, store.UpdateVariantParams{Sku: v.Sku, PriceDeltaCoins: v.PriceDeltaCoins, Active: v.Active,
			RestockEta: v.RestockEta, ID: variantID, ProductID: productID})
		if isDuplicate(err) {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		return audit(ctx, q, a, "variant_updated", "variant", variantID, map[string]any{"sku": v.Sku, "active": v.Active})
	})
}

// Stock movement reasons an admin may record.
const (
	MoveReceive  = "RECEIVE"
	MoveAdjust   = "ADJUST"
	MoveWriteOff = "WRITE_OFF"
)

// StockResult is the variant's stock after a movement.
type StockResult struct {
	OnHand   int `json:"onHand"`
	Reserved int `json:"reserved"`
	Free     int `json:"free"`
	Notified int `json:"notified"`
}

// RecordStockMovement applies RECEIVE (+qty), ADJUST (±delta) or WRITE_OFF
// (−qty) to on-hand stock, never below reserved. When free stock goes from 0
// to above 0, every pending alert for the variant is notified in the same
// transaction (backend-spec §8.6).
func (s *Store) RecordStockMovement(ctx context.Context, a Actor, variantID, reason string, delta int, note string) (*StockResult, error) {
	var res StockResult
	err := s.inTx(ctx, func(q *store.Queries) error {
		v, err := q.LockVariant(ctx, variantID)
		if err != nil {
			return notFound(err)
		}
		var change int
		var r store.MovementReason
		switch reason {
		case MoveReceive:
			change, r = delta, store.SwagInventoryMovementsReasonRECEIVE
		case MoveAdjust:
			change, r = delta, store.SwagInventoryMovementsReasonADJUST
		case MoveWriteOff:
			change, r = -delta, store.SwagInventoryMovementsReasonWRITEOFF
		default:
			return fmt.Errorf("unknown reason %q", reason)
		}
		before := int(v.StockOnHand - v.StockReserved)
		n, err := q.AddStockOnHand(ctx, store.AddStockOnHandParams{Delta: int32(change), VariantID: variantID}) // #nosec G115
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrStateConflict // would go below reserved
		}
		if err := movement(ctx, q, variantID, change, r, note, a.Email); err != nil {
			return err
		}
		after := before + change
		res = StockResult{OnHand: int(v.StockOnHand) + change, Reserved: int(v.StockReserved), Free: after}
		if before <= 0 && after > 0 {
			sent, err := s.notifyBackInStock(ctx, q, v)
			if err != nil {
				return err
			}
			res.Notified = sent
		}
		return audit(ctx, q, a, "stock_"+strings.ToLower(reason), "variant", variantID, map[string]any{"delta": change, "note": note})
	})
	if err != nil {
		return nil, err
	}
	return &res, nil
}

func (s *Store) notifyBackInStock(ctx context.Context, q *store.Queries, v store.Variant) (int, error) {
	p, err := q.GetProduct(ctx, v.ProductID)
	if err != nil {
		return 0, err
	}
	if string(p.Status) != catalog.StatusActive || !v.Active {
		return 0, nil
	}
	alerts, err := q.PendingAlertsForVariant(ctx, v.ID)
	if err != nil {
		return 0, err
	}
	labels, err := s.optionLabels(ctx, q, []string{v.ID})
	if err != nil {
		return 0, err
	}
	for _, al := range alerts {
		if err := q.MarkAlertNotified(ctx, al.ID); err != nil {
			return 0, err
		}
		if err := enqueue(ctx, q, notifications.TemplateBackInStock, al.UserEmail, notifications.AlertEmail{
			ProductName: p.Name, Options: labels[v.ID], ProductSlug: p.Slug,
		}, "alert:"+al.ID); err != nil {
			return 0, err
		}
	}
	return len(alerts), nil
}

// SaveCategory creates (empty ID) or updates a category.
func (s *Store) SaveCategory(ctx context.Context, a Actor, c Category) (*Category, error) {
	creating := c.ID == ""
	if creating {
		c.ID = newID()
	}
	err := s.inTx(ctx, func(q *store.Queries) error {
		var err error
		if creating {
			err = q.CreateCategory(ctx, store.CreateCategoryParams{ID: c.ID, Slug: c.Slug, Name: c.Name, Sort: int32(c.Sort)}) // #nosec G115
		} else {
			if _, err := q.GetCategory(ctx, c.ID); err != nil {
				return notFound(err)
			}
			_, err = q.UpdateCategory(ctx, store.UpdateCategoryParams{Slug: c.Slug, Name: c.Name, Sort: int32(c.Sort), ID: c.ID}) // #nosec G115
		}
		if isDuplicate(err) {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		return audit(ctx, q, a, map[bool]string{true: "category_created", false: "category_updated"}[creating], "category", c.ID, map[string]any{"slug": c.Slug})
	})
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// Category returns one category, or ErrNotFound.
func (s *Store) Category(ctx context.Context, id string) (*Category, error) {
	c, err := s.q.GetCategory(ctx, id)
	if err != nil {
		return nil, notFound(err)
	}
	return &Category{ID: c.ID, Slug: c.Slug, Name: c.Name, Sort: int(c.Sort)}, nil
}

// DeleteCategory refuses while any product points at the category.
func (s *Store) DeleteCategory(ctx context.Context, a Actor, id string) error {
	return s.inTx(ctx, func(q *store.Queries) error {
		if _, err := q.GetCategory(ctx, id); err != nil {
			return notFound(err)
		}
		n, err := q.CountProductsInCategory(ctx, nullStr(id))
		if err != nil {
			return err
		}
		if n > 0 {
			return ErrInUse
		}
		if _, err := q.DeleteCategory(ctx, id); err != nil {
			if isReferenced(err) {
				return ErrInUse
			}
			return err
		}
		return audit(ctx, q, a, "category_deleted", "category", id, nil)
	})
}

// RemovalImpact is what removing a product would touch (backend-spec §8.5).
type RemovalImpact struct {
	OpenOrders    []ImpactOrder `json:"openOrders"`
	OpenUnits     int           `json:"openUnits"`
	Carts         int           `json:"carts"`
	CartUnits     int           `json:"cartUnits"`
	PendingAlerts int           `json:"pendingAlerts"`
	FreeStock     int           `json:"freeStock"`
}

// ImpactOrder is one open order holding the product.
type ImpactOrder struct {
	OrderNo string `json:"orderNo"`
	Status  string `json:"status"`
	Units   int    `json:"units"`
}

// RemovalImpact reads the impact in one read-only transaction.
func (s *Store) RemovalImpact(ctx context.Context, productID string) (*RemovalImpact, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := store.New(tx).GetProduct(ctx, productID); err != nil {
		return nil, notFound(err)
	}
	imp := &RemovalImpact{OpenOrders: []ImpactOrder{}}
	rows, err := tx.QueryContext(ctx, `SELECT o.order_no, o.status, CAST(SUM(ol.quantity) AS SIGNED)
FROM swag_order_lines ol JOIN swag_orders o ON o.id = ol.order_id
WHERE ol.product_id = ? AND o.status IN ('PENDING_PAYMENT', 'CONFIRMED', 'READY_FOR_PICKUP')
GROUP BY o.id, o.order_no, o.status ORDER BY o.placed_at`, productID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var o ImpactOrder
		if err := rows.Scan(&o.OrderNo, &o.Status, &o.Units); err != nil {
			_ = rows.Close()
			return nil, err
		}
		imp.OpenUnits += o.Units
		imp.OpenOrders = append(imp.OpenOrders, o)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	err = tx.QueryRowContext(ctx, `SELECT COUNT(DISTINCT cl.user_email), CAST(COALESCE(SUM(cl.quantity), 0) AS SIGNED)
FROM swag_cart_lines cl JOIN swag_variants v ON v.id = cl.variant_id WHERE v.product_id = ?`, productID).Scan(&imp.Carts, &imp.CartUnits)
	if err != nil {
		return nil, err
	}
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM swag_stock_alerts a JOIN swag_variants v ON v.id = a.variant_id
WHERE v.product_id = ? AND a.status = 'PENDING'`, productID).Scan(&imp.PendingAlerts)
	if err != nil {
		return nil, err
	}
	err = tx.QueryRowContext(ctx, `SELECT CAST(COALESCE(SUM(stock_on_hand - stock_reserved), 0) AS SIGNED)
FROM swag_variants WHERE product_id = ?`, productID).Scan(&imp.FreeStock)
	if err != nil {
		return nil, err
	}
	return imp, nil
}
