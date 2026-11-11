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
	"time"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/catalog"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/store"
)

// maxCartQuantity is the most of one variant a cart line holds.
const maxCartQuantity = 99

// CartForQuote returns the caller's cart lines with live prices and stock.
func (s *Store) CartForQuote(ctx context.Context, email string) ([]CartItem, error) {
	return s.cartItems(ctx, s.q, email)
}

func (s *Store) cartItems(ctx context.Context, q *store.Queries, email string) ([]CartItem, error) {
	rows, err := q.CartLinesForUser(ctx, email)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	variantIDs := make([]string, 0, len(rows))
	productIDs := make([]string, 0, len(rows))
	for _, r := range rows {
		variantIDs = append(variantIDs, r.VariantID)
		productIDs = append(productIDs, r.ProductID)
	}
	labels, err := s.optionLabels(ctx, q, variantIDs)
	if err != nil {
		return nil, err
	}
	images, err := s.firstImages(ctx, q, productIDs)
	if err != nil {
		return nil, err
	}
	paid := map[string]int{}
	items := make([]CartItem, 0, len(rows))
	for _, r := range rows {
		maxPer := intPtr(r.MaxPerUser)
		if maxPer != nil {
			if _, ok := paid[r.ProductID]; !ok {
				n, err := q.PaidUnitsForProduct(ctx, store.PaidUnitsForProductParams{UserEmail: email, ProductID: r.ProductID, Since: limitSince(s.now(), intPtr(r.LimitWindowDays))})
				if err != nil {
					return nil, err
				}
				paid[r.ProductID] = int(n)
			}
		}
		items = append(items, CartItem{
			LineID: r.ID, VariantID: r.VariantID, ProductID: r.ProductID, Slug: r.Slug, Name: r.Name, SKU: r.Sku,
			Options: labels[r.VariantID], Quantity: int(r.Quantity), UnitPrice: r.PriceCoins + r.PriceDeltaCoins,
			Free: int(r.StockOnHand - r.StockReserved), ProductStatus: string(r.ProductStatus), VariantActive: r.VariantActive,
			RestockPolicy: string(r.RestockPolicy), MaxPerUser: maxPer, PaidUnits: paid[r.ProductID],
			RestockETA: timePtr(r.RestockEta), ImageURL: images[r.ProductID], AddedAt: r.AddedAt,
		})
	}
	return items, nil
}

// limitSince is the start of a max_per_user window. With no window it is the
// Unix epoch, never Go's zero time: the driver sends that as 0000-00-00, which
// strict mode rejects.
func limitSince(now time.Time, windowDays *int) time.Time {
	if windowDays == nil {
		return time.Unix(0, 0).UTC()
	}
	return now.AddDate(0, 0, -*windowDays)
}

// optionLabels returns "Black · M" per variant, in option order.
func (s *Store) optionLabels(ctx context.Context, q *store.Queries, variantIDs []string) (map[string]string, error) {
	out := map[string]string{}
	if len(variantIDs) == 0 {
		return out, nil
	}
	rows, err := q.OptionLabelsForVariants(ctx, variantIDs)
	if err != nil {
		return nil, err
	}
	vals := map[string][]string{}
	for _, r := range rows {
		vals[r.VariantID] = append(vals[r.VariantID], r.Value)
	}
	for id, v := range vals {
		out[id] = catalog.OptionsLabel(v)
	}
	return out, nil
}

func (s *Store) firstImages(ctx context.Context, q *store.Queries, productIDs []string) (map[string]string, error) {
	out := map[string]string{}
	if len(productIDs) == 0 {
		return out, nil
	}
	rows, err := q.FirstImagesForProducts(ctx, productIDs)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if _, ok := out[r.ProductID]; !ok {
			out[r.ProductID] = r.Url
		}
	}
	return out, nil
}

// CartItemCount is the number of units in the cart (the top-bar badge).
func (s *Store) CartItemCount(ctx context.Context, email string) (int, error) {
	n, err := s.q.CountCartItems(ctx, email)
	return int(n), err
}

// AddToCart merges quantity into the line for variantID, capped at 99. Only an
// active variant of an ACTIVE product can be added.
func (s *Store) AddToCart(ctx context.Context, email, variantID string, quantity int) (string, error) {
	var lineID string
	err := s.inTx(ctx, func(q *store.Queries) error {
		v, err := q.GetVariant(ctx, variantID)
		if err != nil {
			return notFound(err)
		}
		p, err := q.GetProduct(ctx, v.ProductID)
		if err != nil {
			return err
		}
		if !v.Active || string(p.Status) != catalog.StatusActive {
			return ErrNotAvailable
		}
		if err := q.UpsertCartLine(ctx, store.UpsertCartLineParams{
			ID: newID(), UserEmail: email, VariantID: variantID, Quantity: int32(min(quantity, maxCartQuantity)), // #nosec G115
		}); err != nil {
			return err
		}
		line, err := q.GetCartLineByVariant(ctx, store.GetCartLineByVariantParams{UserEmail: email, VariantID: variantID})
		lineID = line.ID
		return err
	})
	return lineID, err
}

// SetCartQuantity changes one of the caller's lines. Another user's line is ErrNotFound.
func (s *Store) SetCartQuantity(ctx context.Context, email, lineID string, quantity int) error {
	n, err := s.q.SetCartLineQuantity(ctx, store.SetCartLineQuantityParams{
		Quantity: int32(min(quantity, maxCartQuantity)), ID: lineID, UserEmail: email, // #nosec G115
	})
	if err != nil {
		return err
	}
	if n == 0 {
		// Zero rows also means "same quantity"; tell the two apart.
		items, err := s.q.CartLinesForUser(ctx, email)
		if err != nil {
			return err
		}
		for _, it := range items {
			if it.ID == lineID {
				return nil
			}
		}
		return ErrNotFound
	}
	return nil
}

// RemoveCartLine deletes one of the caller's lines.
func (s *Store) RemoveCartLine(ctx context.Context, email, lineID string) error {
	n, err := s.q.DeleteCartLine(ctx, store.DeleteCartLineParams{ID: lineID, UserEmail: email})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
