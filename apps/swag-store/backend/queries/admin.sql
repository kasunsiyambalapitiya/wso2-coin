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

-- Outbox, admin actions, job state, stock alerts.

-- name: EnqueueEmail :exec
INSERT INTO swag_notification_outbox (template, to_email, payload_json, dedupe_key) VALUES (?, ?, ?, ?)
ON DUPLICATE KEY UPDATE id = id;

-- name: DueEmails :many
SELECT * FROM swag_notification_outbox
WHERE status = 'PENDING' AND next_attempt_at <= ?
ORDER BY id LIMIT 20 FOR UPDATE SKIP LOCKED;

-- name: MarkEmailSent :exec
UPDATE swag_notification_outbox SET status = 'SENT', sent_at = CURRENT_TIMESTAMP(6), attempts = attempts + 1,
    last_error = NULL
WHERE id = ?;

-- name: MarkEmailRetry :exec
UPDATE swag_notification_outbox SET attempts = attempts + 1, next_attempt_at = ?, last_error = ? WHERE id = ?;

-- name: MarkEmailFailed :exec
UPDATE swag_notification_outbox SET status = 'FAILED', attempts = attempts + 1, last_error = ? WHERE id = ?;

-- name: InsertAdminAction :exec
INSERT INTO swag_admin_actions (actor_email, actor_user_id, action, entity_type, entity_id, detail_json)
VALUES (?, ?, ?, ?, ?, ?);

-- name: ListAdminActions :many
SELECT * FROM swag_admin_actions ORDER BY id DESC LIMIT ? OFFSET ?;

-- name: CountAdminActions :one
SELECT COUNT(*) FROM swag_admin_actions;

-- name: GetJobState :one
SELECT * FROM swag_job_state WHERE job = ?;

-- name: SaveJobState :exec
INSERT INTO swag_job_state (job, job_cursor, last_run_at, last_status) VALUES (?, ?, ?, ?)
ON DUPLICATE KEY UPDATE job_cursor = VALUES(job_cursor), last_run_at = VALUES(last_run_at),
    last_status = VALUES(last_status);

-- name: InsertStockAlert :exec
INSERT INTO swag_stock_alerts (id, user_email, variant_id, status) VALUES (?, ?, ?, 'PENDING');

-- name: GetPendingAlert :one
SELECT * FROM swag_stock_alerts WHERE user_email = ? AND variant_id = ? AND status = 'PENDING';

-- name: ListAlertsForUser :many
SELECT a.id, a.variant_id, a.status, a.created_at, a.notified_at, a.closed_at,
       v.sku, p.slug, p.name AS product_name, p.status AS product_status
FROM swag_stock_alerts a
JOIN swag_variants v ON v.id = a.variant_id
JOIN swag_products p ON p.id = v.product_id
WHERE a.user_email = ?
ORDER BY a.created_at DESC
LIMIT 200;

-- name: CloseAlertForUser :execrows
UPDATE swag_stock_alerts SET status = 'CLOSED', closed_at = CURRENT_TIMESTAMP(6)
WHERE id = ? AND user_email = ? AND status = 'PENDING';

-- name: PendingAlertsForVariant :many
SELECT * FROM swag_stock_alerts WHERE variant_id = ? AND status = 'PENDING' ORDER BY created_at FOR UPDATE;

-- name: PendingAlertsForProduct :many
SELECT a.* FROM swag_stock_alerts a JOIN swag_variants v ON v.id = a.variant_id
WHERE v.product_id = ? AND a.status = 'PENDING' ORDER BY a.created_at FOR UPDATE;

-- name: MarkAlertNotified :exec
UPDATE swag_stock_alerts SET status = 'NOTIFIED', notified_at = CURRENT_TIMESTAMP(6) WHERE id = ? AND status = 'PENDING';

-- name: MarkAlertClosed :exec
UPDATE swag_stock_alerts SET status = 'CLOSED', closed_at = CURRENT_TIMESTAMP(6) WHERE id = ? AND status = 'PENDING';
