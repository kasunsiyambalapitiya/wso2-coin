-- Copyright (c) 2026 WSO2 LLC. (https://www.wso2.com).
--
-- WSO2 LLC. licenses this file to you under the Apache License,
-- Version 2.0 (the "License"); you may not use this file except
-- in compliance with the License.
-- You may obtain a copy of the License at
--
-- http://www.apache.org/licenses/LICENSE-2.0
--
-- Unless required by applicable law or agreed to in writing,
-- software distributed under the License is distributed on an
-- "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
-- KIND, either express or implied.  See the License for the
-- specific language governing permissions and limitations
-- under the License.

-- Catalog: categories, products, options, variants, images, stock.

-- name: ListActiveCategories :many
SELECT c.id, c.slug, c.name, c.sort FROM swag_categories c
WHERE EXISTS (SELECT 1 FROM swag_products p WHERE p.category_id = c.id AND p.status = 'ACTIVE')
ORDER BY c.sort, c.name;

-- name: ListCategories :many
SELECT * FROM swag_categories ORDER BY sort, name;

-- name: GetCategory :one
SELECT * FROM swag_categories WHERE id = ?;

-- name: CreateCategory :exec
INSERT INTO swag_categories (id, slug, name, sort) VALUES (?, ?, ?, ?);

-- name: UpdateCategory :execrows
UPDATE swag_categories SET slug = ?, name = ?, sort = ? WHERE id = ?;

-- name: CountProductsInCategory :one
SELECT COUNT(*) FROM swag_products WHERE category_id = ?;

-- name: DeleteCategory :execrows
DELETE FROM swag_categories WHERE id = ?;

-- name: GetProduct :one
SELECT * FROM swag_products WHERE id = ?;

-- name: GetProductBySlug :one
SELECT * FROM swag_products WHERE slug = ?;

-- name: LockProduct :one
SELECT * FROM swag_products WHERE id = ? FOR UPDATE;

-- name: CreateProduct :exec
INSERT INTO swag_products (id, slug, name, description, category_id, price_coins, restock_policy,
    max_per_user, limit_window_days, status)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'DRAFT');

-- name: UpdateProduct :execrows
UPDATE swag_products SET slug = ?, name = ?, description = ?, category_id = ?, price_coins = ?,
    restock_policy = ?, max_per_user = ?, limit_window_days = ?
WHERE id = ?;

-- name: PublishProduct :execrows
UPDATE swag_products SET status = 'ACTIVE', published_at = COALESCE(published_at, CURRENT_TIMESTAMP(6))
WHERE id = ? AND status = 'DRAFT';

-- name: RemoveProduct :execrows
UPDATE swag_products SET status = 'REMOVED', removed_at = CURRENT_TIMESTAMP(6), removed_by = ?, removal_note = ?
WHERE id = ? AND status = 'ACTIVE';

-- name: RestoreProduct :execrows
UPDATE swag_products SET status = 'ACTIVE', removed_at = NULL, removed_by = NULL, removal_note = NULL
WHERE id = ? AND status = 'REMOVED';

-- name: CountOrderLinesForProduct :one
SELECT COUNT(*) FROM swag_order_lines WHERE product_id = ?;

-- name: DeleteProductImages :exec
DELETE FROM swag_product_images WHERE product_id = ?;

-- name: DeleteProductVariantValues :exec
DELETE vov FROM swag_variant_option_values vov JOIN swag_variants v ON v.id = vov.variant_id WHERE v.product_id = ?;

-- name: DeleteProductMovements :exec
DELETE m FROM swag_inventory_movements m JOIN swag_variants v ON v.id = m.variant_id WHERE v.product_id = ?;

-- name: DeleteProductVariants :exec
DELETE FROM swag_variants WHERE product_id = ?;

-- name: DeleteProductOptionValues :exec
DELETE ov FROM swag_option_values ov JOIN swag_product_options o ON o.id = ov.option_id WHERE o.product_id = ?;

-- name: DeleteProductOptions :exec
DELETE FROM swag_product_options WHERE product_id = ?;

-- name: DeleteProductAlternatives :exec
DELETE FROM swag_product_alternatives WHERE product_id = ? OR alternative_id = ?;

-- name: DeleteProductStats :exec
DELETE FROM swag_product_stats WHERE product_id = ?;

-- name: DeleteProductRow :execrows
DELETE FROM swag_products WHERE id = ? AND status = 'DRAFT';

-- name: ListProductOptions :many
SELECT * FROM swag_product_options WHERE product_id = ? ORDER BY sort, name;

-- name: ListOptionValuesByProduct :many
SELECT ov.id, ov.option_id, ov.value, ov.swatch_hex, ov.sort
FROM swag_option_values ov JOIN swag_product_options o ON o.id = ov.option_id
WHERE o.product_id = ? ORDER BY o.sort, ov.sort, ov.value;

-- name: InsertOption :exec
INSERT INTO swag_product_options (id, product_id, name, sort) VALUES (?, ?, ?, ?);

-- name: UpdateOptionSort :exec
UPDATE swag_product_options SET sort = ? WHERE id = ?;

-- name: InsertOptionValue :exec
INSERT INTO swag_option_values (id, option_id, value, swatch_hex, sort) VALUES (?, ?, ?, ?, ?);

-- name: UpdateOptionValue :exec
UPDATE swag_option_values SET swatch_hex = ?, sort = ? WHERE id = ?;

-- name: ListVariantsByProduct :many
SELECT * FROM swag_variants WHERE product_id = ? ORDER BY sku;

-- name: ListVariantValuesByProduct :many
SELECT vov.variant_id, vov.option_value_id
FROM swag_variant_option_values vov JOIN swag_variants v ON v.id = vov.variant_id
WHERE v.product_id = ?;

-- name: GetVariant :one
SELECT * FROM swag_variants WHERE id = ?;

-- name: LockVariant :one
SELECT * FROM swag_variants WHERE id = ? FOR UPDATE;

-- name: InsertVariant :exec
INSERT INTO swag_variants (id, product_id, sku, price_delta_coins, active) VALUES (?, ?, ?, ?, TRUE);

-- name: InsertVariantValue :exec
INSERT INTO swag_variant_option_values (variant_id, option_value_id) VALUES (?, ?);

-- name: SetVariantActive :exec
UPDATE swag_variants SET active = ? WHERE id = ?;

-- name: UpdateVariant :execrows
UPDATE swag_variants SET sku = ?, price_delta_coins = ?, active = ?, restock_eta = ?
WHERE id = ? AND product_id = ?;

-- name: SkuExists :one
SELECT COUNT(*) FROM swag_variants WHERE sku = ?;

-- name: ListImagesByProduct :many
SELECT * FROM swag_product_images WHERE product_id = ? ORDER BY sort, id;

-- name: InsertImage :exec
INSERT INTO swag_product_images (id, product_id, photo_view, path, alt, option_value_id, sort) VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: ListAlternativeIDs :many
SELECT alternative_id FROM swag_product_alternatives WHERE product_id = ? ORDER BY sort;

-- name: InsertAlternative :exec
INSERT INTO swag_product_alternatives (product_id, alternative_id, sort) VALUES (?, ?, ?);

-- name: DeleteAlternativesOf :exec
DELETE FROM swag_product_alternatives WHERE product_id = ?;

-- Stock. Every change also writes one swag_inventory_movements row.

-- name: ReserveStock :execrows
UPDATE swag_variants v JOIN swag_products p ON p.id = v.product_id
SET v.stock_reserved = v.stock_reserved + sqlc.arg(qty)
WHERE v.id = sqlc.arg(variant_id) AND v.active = TRUE AND p.status = 'ACTIVE'
  AND v.stock_on_hand - v.stock_reserved >= sqlc.arg(qty);

-- name: ReserveStockLate :execrows
UPDATE swag_variants SET stock_reserved = stock_reserved + sqlc.arg(qty)
WHERE id = sqlc.arg(variant_id) AND stock_on_hand - stock_reserved >= sqlc.arg(qty);

-- name: CommitStock :execrows
UPDATE swag_variants SET stock_on_hand = stock_on_hand - sqlc.arg(qty), stock_reserved = stock_reserved - sqlc.arg(qty)
WHERE id = sqlc.arg(variant_id) AND stock_reserved >= sqlc.arg(qty);

-- name: ReleaseStock :execrows
UPDATE swag_variants SET stock_reserved = stock_reserved - sqlc.arg(qty)
WHERE id = sqlc.arg(variant_id) AND stock_reserved >= sqlc.arg(qty);

-- name: AddStockOnHand :execrows
UPDATE swag_variants SET stock_on_hand = stock_on_hand + sqlc.arg(delta)
WHERE id = sqlc.arg(variant_id) AND stock_on_hand + sqlc.arg(delta) >= stock_reserved;

-- name: InsertMovement :exec
INSERT INTO swag_inventory_movements (variant_id, delta, reason, ref, actor_email) VALUES (?, ?, ?, ?, ?);

-- name: OptionLabelsForVariants :many
SELECT vov.variant_id, ov.value
FROM swag_variant_option_values vov
JOIN swag_option_values ov ON ov.id = vov.option_value_id
JOIN swag_product_options o ON o.id = ov.option_id
WHERE vov.variant_id IN (sqlc.slice(variant_ids))
ORDER BY vov.variant_id, o.sort, o.name;

-- name: FirstImagesForProducts :many
SELECT product_id, path FROM swag_product_images
WHERE product_id IN (sqlc.slice(product_ids))
ORDER BY product_id, sort, id;

-- name: ListMovementsForVariant :many
SELECT * FROM swag_inventory_movements WHERE variant_id = ? ORDER BY id DESC LIMIT 50;

-- name: LockProductsShared :many
SELECT id FROM swag_products WHERE id IN (sqlc.slice(ids)) ORDER BY id FOR SHARE;
