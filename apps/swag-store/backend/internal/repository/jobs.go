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
	"time"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/notifications"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/store"
)

// outboxLease is how long a claimed email stays invisible to the next run.
const outboxLease = 5 * time.Minute

// OutboxEmail is one queued email.
type OutboxEmail struct {
	ID       int64
	Template string
	To       string
	Payload  []byte
	Attempts int
}

// ClaimDueEmails takes up to 20 due rows with SKIP LOCKED and leases them for
// five minutes, so sending happens outside the transaction.
func (s *Store) ClaimDueEmails(ctx context.Context) ([]OutboxEmail, error) {
	var out []OutboxEmail
	now := s.now().UTC()
	err := s.inTx(ctx, func(q *store.Queries) error {
		out = nil // a deadlock retry starts the claim again
		rows, err := q.DueEmails(ctx, now)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if _, err := s.leaseEmail(ctx, q, r.ID, now.Add(outboxLease)); err != nil {
				return err
			}
			out = append(out, OutboxEmail{ID: r.ID, Template: r.Template, To: r.ToEmail, Payload: r.PayloadJson, Attempts: int(r.Attempts)})
		}
		return nil
	})
	return out, err
}

func (s *Store) leaseEmail(ctx context.Context, q *store.Queries, id int64, until time.Time) (bool, error) {
	err := q.MarkEmailRetry(ctx, store.MarkEmailRetryParams{NextAttemptAt: until, LastError: sql.NullString{}, ID: id})
	return err == nil, err
}

// EmailSent marks a row sent.
func (s *Store) EmailSent(ctx context.Context, id int64) error { return s.q.MarkEmailSent(ctx, id) }

// EmailRetry schedules the next attempt. The lease already counted one attempt.
func (s *Store) EmailRetry(ctx context.Context, id int64, next time.Time, reason string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE swag_notification_outbox SET next_attempt_at = ?, last_error = ? WHERE id = ?`,
		next.UTC(), truncate(reason, 256), id)
	return err
}

// EmailFailed gives up on a row.
func (s *Store) EmailFailed(ctx context.Context, id int64, reason string) error {
	return s.q.MarkEmailFailed(ctx, store.MarkEmailFailedParams{LastError: nullStr(truncate(reason, 256)), ID: id})
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// QueueEmail adds one outbox row outside any other change (jobs).
func (s *Store) QueueEmail(ctx context.Context, template, to string, payload any, dedupe string) error {
	return enqueue(ctx, s.q, template, to, payload, dedupe)
}

// RemindPickups queues one reminder per READY_FOR_PICKUP order ready before
// cutoff that has had none, and returns how many it queued.
func (s *Store) RemindPickups(ctx context.Context, cutoff time.Time) (int, error) {
	due, err := s.q.OrdersNeedingReminder(ctx, sql.NullTime{Time: cutoff, Valid: true})
	if err != nil {
		return 0, err
	}
	sent := 0
	for _, o := range due {
		err := s.inTx(ctx, func(q *store.Queries) error {
			n, err := q.MarkReminderSent(ctx, o.ID)
			if err != nil || n == 0 {
				return err
			}
			lines, err := q.ListOrderLines(ctx, o.ID)
			if err != nil {
				return err
			}
			sent++
			return enqueueOrderEmail(ctx, q, notifications.TemplatePickupReminder, o, lines, "reminder:"+o.ID)
		})
		if err != nil {
			return sent, err
		}
	}
	return sent, nil
}

// FlagUncollected sets attention 'uncollected' on orders ready before cutoff.
func (s *Store) FlagUncollected(ctx context.Context, cutoff time.Time) (int64, error) {
	return s.q.FlagUncollected(ctx, sql.NullTime{Time: cutoff, Valid: true})
}

// RecomputeStats rebuilds units_sold_30d from paid order lines since since.
func (s *Store) RecomputeStats(ctx context.Context, since time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO swag_product_stats (product_id, units_sold_30d, updated_at)
SELECT p.id, CAST(COALESCE(SUM(CASE WHEN o.id IS NOT NULL THEN ol.quantity END), 0) AS SIGNED), CURRENT_TIMESTAMP(6)
FROM swag_products p
LEFT JOIN swag_order_lines ol ON ol.product_id = p.id
LEFT JOIN swag_orders o ON o.id = ol.order_id AND o.status IN ('CONFIRMED', 'READY_FOR_PICKUP', 'COLLECTED')
    AND o.placed_at >= ?
GROUP BY p.id
ON DUPLICATE KEY UPDATE units_sold_30d = VALUES(units_sold_30d), updated_at = VALUES(updated_at)`, since.UTC())
	return err
}

// LowStock lists active variants of ACTIVE products at or below threshold.
func (s *Store) LowStock(ctx context.Context, threshold int) ([]notifications.LowStockItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT v.id, v.sku, p.name, v.stock_on_hand - v.stock_reserved
FROM swag_variants v JOIN swag_products p ON p.id = v.product_id
WHERE p.status = 'ACTIVE' AND v.active = TRUE AND v.stock_on_hand - v.stock_reserved <= ?
ORDER BY v.stock_on_hand - v.stock_reserved, p.name, v.sku LIMIT 500`, threshold)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []notifications.LowStockItem
	var ids []string
	for rows.Next() {
		var id string
		var it notifications.LowStockItem
		if err := rows.Scan(&id, &it.SKU, &it.Name, &it.Free); err != nil {
			return nil, err
		}
		items = append(items, it)
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	labels, err := s.optionLabels(ctx, s.q, ids)
	if err != nil {
		return nil, err
	}
	for i := range items {
		items[i].Options = labels[ids[i]]
	}
	return items, nil
}

// SystemAction writes an admin action row for a job finding.
func (s *Store) SystemAction(ctx context.Context, action, entityType, entityID string, detail any) error {
	return audit(ctx, s.q, SystemActor, action, entityType, entityID, detail)
}

// JobState is a job's last run and cursor.
type JobState struct {
	Cursor    string
	LastRunAt *time.Time
	Status    string
}

// GetJobState reads a job's state; a job that never ran has a zero state.
func (s *Store) GetJobState(ctx context.Context, job string) (JobState, error) {
	r, err := s.q.GetJobState(ctx, job)
	if errors.Is(err, sql.ErrNoRows) {
		return JobState{}, nil
	}
	if err != nil {
		return JobState{}, err
	}
	return JobState{Cursor: r.JobCursor.String, LastRunAt: timePtr(r.LastRunAt), Status: r.LastStatus.String}, nil
}

// SaveJobState records a run.
func (s *Store) SaveJobState(ctx context.Context, job string, st JobState) error {
	return s.q.SaveJobState(ctx, store.SaveJobStateParams{Job: job, JobCursor: nullStr(st.Cursor),
		LastRunAt: nullTime(st.LastRunAt), LastStatus: nullStr(st.Status)})
}

// WithJobLock runs fn only if this replica gets GET_LOCK('swag:job:<name>', 0)
// on a dedicated connection; otherwise it returns (false, nil).
func (s *Store) WithJobLock(ctx context.Context, name string, fn func(ctx context.Context) error) (bool, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	var got sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, 0)", "swag:job:"+name).Scan(&got); err != nil {
		return false, err
	}
	if !got.Valid || got.Int64 != 1 {
		return false, nil
	}
	defer func() {
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), "SELECT RELEASE_LOCK(?)", "swag:job:"+name)
	}()
	return true, fn(ctx)
}
