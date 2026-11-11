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

-- Orders, events, notes.

-- name: NextOrderNumber :execlastid
INSERT INTO swag_order_numbers (n) VALUES (NULL);

-- name: InsertOrder :exec
INSERT INTO swag_orders (id, order_no, user_email, user_id, user_name, status, pickup_office_id, office_name,
    wallet_address, total_coins, payment_reference, idempotency_key)
VALUES (?, ?, ?, ?, ?, 'PENDING_PAYMENT', ?, ?, ?, ?, ?, ?);

-- name: InsertOrderLine :exec
INSERT INTO swag_order_lines (id, order_id, product_id, variant_id, sku, name, options_label, unit_price_coins, quantity)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: InsertOrderEvent :exec
INSERT INTO swag_order_events (order_id, from_status, to_status, note, actor_email) VALUES (?, ?, ?, ?, ?);

-- name: GetOrder :one
SELECT * FROM swag_orders WHERE id = ?;

-- name: LockOrder :one
SELECT * FROM swag_orders WHERE id = ? FOR UPDATE;

-- name: GetOrderByKey :one
SELECT * FROM swag_orders WHERE user_email = ? AND idempotency_key = ?;

-- name: GetOrderByNo :one
SELECT * FROM swag_orders WHERE order_no = ?;

-- name: GetOrderByNoForUser :one
SELECT * FROM swag_orders WHERE order_no = ? AND user_email = ?;

-- name: ListOrderLines :many
SELECT * FROM swag_order_lines WHERE order_id = ? ORDER BY name, id;

-- name: ListOrderEvents :many
SELECT * FROM swag_order_events WHERE order_id = ? ORDER BY id;

-- name: ListOrderNotes :many
SELECT * FROM swag_order_notes WHERE order_id = ? ORDER BY id;

-- name: InsertOrderNote :exec
INSERT INTO swag_order_notes (order_id, note, actor_email) VALUES (?, ?, ?);

-- name: ListOrdersForUser :many
SELECT * FROM swag_orders WHERE user_email = ? ORDER BY placed_at DESC, id DESC LIMIT ? OFFSET ?;

-- name: CountOrdersForUser :one
SELECT COUNT(*) FROM swag_orders WHERE user_email = ?;

-- name: ConfirmFromPending :execrows
UPDATE swag_orders SET status = 'CONFIRMED', confirmed_at = CURRENT_TIMESTAMP(6)
WHERE id = ? AND status = 'PENDING_PAYMENT';

-- name: ConfirmFromFailed :execrows
UPDATE swag_orders SET status = 'CONFIRMED', confirmed_at = CURRENT_TIMESTAMP(6), failure_code = NULL,
    attention_reason = NULL
WHERE id = ? AND status = 'FAILED';

-- name: FailFromPending :execrows
UPDATE swag_orders SET status = 'FAILED', failed_at = CURRENT_TIMESTAMP(6), failure_code = ?, attention_reason = ?
WHERE id = ? AND status = 'PENDING_PAYMENT';

-- name: SetAttention :exec
UPDATE swag_orders SET attention_reason = ? WHERE id = ?;

-- name: MarkReady :execrows
UPDATE swag_orders SET status = 'READY_FOR_PICKUP', ready_at = CURRENT_TIMESTAMP(6)
WHERE id = ? AND status = 'CONFIRMED';

-- name: MarkCollected :execrows
UPDATE swag_orders SET status = 'COLLECTED', collected_at = CURRENT_TIMESTAMP(6), collected_by_email = ?,
    attention_reason = IF(attention_reason = 'uncollected', NULL, attention_reason)
WHERE id = ? AND status = 'READY_FOR_PICKUP';

-- name: PendingPaymentOrders :many
SELECT * FROM swag_orders WHERE status = 'PENDING_PAYMENT' AND placed_at <= ? ORDER BY placed_at LIMIT 50;

-- name: BumpPaymentChecks :exec
UPDATE swag_orders SET payment_checks = payment_checks + 1 WHERE id = ?;

-- name: OrdersNeedingReminder :many
SELECT * FROM swag_orders
WHERE status = 'READY_FOR_PICKUP' AND ready_at <= ? AND reminder_sent_at IS NULL
ORDER BY ready_at LIMIT 100;

-- name: MarkReminderSent :execrows
UPDATE swag_orders SET reminder_sent_at = CURRENT_TIMESTAMP(6) WHERE id = ? AND reminder_sent_at IS NULL;

-- name: FlagUncollected :execrows
UPDATE swag_orders SET attention_reason = 'uncollected'
WHERE status = 'READY_FOR_PICKUP' AND ready_at <= ? AND attention_reason IS NULL;
