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

-- Cart lines and pickup offices.

-- name: CartLinesForUser :many
SELECT cl.id, cl.variant_id, cl.quantity, cl.added_at,
       v.sku, v.price_delta_coins, v.stock_on_hand, v.stock_reserved, v.active AS variant_active, v.restock_eta,
       p.id AS product_id, p.slug, p.name, p.price_coins, p.status AS product_status,
       p.max_per_user, p.limit_window_days, p.restock_policy
FROM swag_cart_lines cl
JOIN swag_variants v ON v.id = cl.variant_id
JOIN swag_products p ON p.id = v.product_id
WHERE cl.user_email = ?
ORDER BY cl.added_at, cl.id;

-- name: LockCartLines :many
SELECT id FROM swag_cart_lines WHERE user_email = ? FOR UPDATE;

-- name: CountCartItems :one
SELECT CAST(COALESCE(SUM(quantity), 0) AS SIGNED) FROM swag_cart_lines WHERE user_email = ?;

-- name: UpsertCartLine :exec
INSERT INTO swag_cart_lines (id, user_email, variant_id, quantity) VALUES (?, ?, ?, ?)
ON DUPLICATE KEY UPDATE quantity = LEAST(quantity + VALUES(quantity), 99);

-- name: GetCartLineByVariant :one
SELECT * FROM swag_cart_lines WHERE user_email = ? AND variant_id = ?;

-- name: SetCartLineQuantity :execrows
UPDATE swag_cart_lines SET quantity = ? WHERE id = ? AND user_email = ?;

-- name: DeleteCartLine :execrows
DELETE FROM swag_cart_lines WHERE id = ? AND user_email = ?;

-- name: DeleteCartLinesForVariants :exec
DELETE FROM swag_cart_lines WHERE user_email = sqlc.arg(user_email) AND variant_id IN (sqlc.slice(variant_ids));

-- name: PaidUnitsForProduct :one
SELECT CAST(COALESCE(SUM(ol.quantity), 0) AS SIGNED)
FROM swag_order_lines ol JOIN swag_orders o ON o.id = ol.order_id
WHERE o.user_email = sqlc.arg(user_email) AND ol.product_id = sqlc.arg(product_id)
  AND o.status IN ('PENDING_PAYMENT', 'CONFIRMED', 'READY_FOR_PICKUP', 'COLLECTED')
  AND o.placed_at >= sqlc.arg(since);

-- name: ListActiveOffices :many
SELECT * FROM swag_pickup_offices WHERE active = TRUE ORDER BY sort, name;

-- name: ListOffices :many
SELECT * FROM swag_pickup_offices ORDER BY sort, name;

-- name: GetOffice :one
SELECT * FROM swag_pickup_offices WHERE id = ?;

-- name: CreateOffice :exec
INSERT INTO swag_pickup_offices (id, name, address, country, collection_point, hours, lead_time_days,
    hr_locations_json, active, sort)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: UpdateOffice :execrows
UPDATE swag_pickup_offices SET name = ?, address = ?, country = ?, collection_point = ?, hours = ?,
    lead_time_days = ?, hr_locations_json = ?, active = ?, sort = ?
WHERE id = ?;

-- name: LastOrderOffice :one
SELECT pickup_office_id FROM swag_orders WHERE user_email = ? ORDER BY placed_at DESC LIMIT 1;
