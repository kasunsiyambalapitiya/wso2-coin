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
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/store"
)

// MySQL error numbers the repository handles.
const (
	errDuplicateEntry = 1062
	errDeadlock       = 1213
	errLockWait       = 1205
	errRowIsReferred  = 1451
)

// txRetries is how often a transaction is retried after a deadlock.
const txRetries = 3

// Store is the repository over one connection pool.
type Store struct {
	db  *sql.DB
	q   *store.Queries
	now func() time.Time
}

// New wraps db.
func New(db *sql.DB) *Store {
	return &Store{db: db, q: store.New(db), now: time.Now}
}

// DB exposes the pool (health probe, job locks).
func (s *Store) DB() *sql.DB { return s.db }

// inTx runs fn in a READ COMMITTED transaction and retries it up to three
// times on a deadlock or lock-wait timeout.
func (s *Store) inTx(ctx context.Context, fn func(q *store.Queries) error) error {
	var err error
	for attempt := 0; attempt <= txRetries; attempt++ {
		err = s.tryTx(ctx, fn)
		if !isRetryable(err) {
			return err
		}
		slog.WarnContext(ctx, "transaction retried after deadlock", "attempt", attempt+1)
		time.Sleep(time.Duration(attempt+1) * 20 * time.Millisecond)
	}
	return err
}

func (s *Store) tryTx(ctx context.Context, fn func(q *store.Queries) error) (err error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if err = fn(s.q.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit()
}

func mysqlNumber(err error) uint16 {
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		return me.Number
	}
	return 0
}

func isRetryable(err error) bool {
	n := mysqlNumber(err)
	return n == errDeadlock || n == errLockWait
}

func isDuplicate(err error) bool { return mysqlNumber(err) == errDuplicateEntry }

func isReferenced(err error) bool { return mysqlNumber(err) == errRowIsReferred }

// notFound turns sql.ErrNoRows into ErrNotFound.
func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func newID() string { return uuid.Must(uuid.NewV7()).String() }

func nullStr(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

func nullInt(p *int) sql.NullInt32 {
	if p == nil {
		return sql.NullInt32{}
	}
	return sql.NullInt32{Int32: int32(*p), Valid: true} // #nosec G115 -- bounded by request validation
}

func intPtr(n sql.NullInt32) *int {
	if !n.Valid {
		return nil
	}
	v := int(n.Int32)
	return &v
}

func timePtr(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}

func nullTime(t *time.Time) sql.NullTime {
	if t == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: *t, Valid: true}
}

// placeholders returns "?, ?, ?" for n values.
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

// enqueue adds one outbox row in the caller's transaction. A dedupe key that
// already exists is ignored, so a retried step never sends twice.
func enqueue(ctx context.Context, q *store.Queries, template, to string, payload any, dedupe string) error {
	if to == "" {
		return nil
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode %s payload: %w", template, err)
	}
	return q.EnqueueEmail(ctx, store.EnqueueEmailParams{Template: template, ToEmail: to, PayloadJson: b, DedupeKey: dedupe})
}

// audit writes one swag_admin_actions row in the caller's transaction.
func audit(ctx context.Context, q *store.Queries, a Actor, action, entityType, entityID string, detail any) error {
	var raw json.RawMessage
	if detail != nil {
		b, err := json.Marshal(detail)
		if err != nil {
			return err
		}
		raw = b
	}
	return q.InsertAdminAction(ctx, store.InsertAdminActionParams{
		ActorEmail: a.Email, ActorUserID: a.UserID, Action: action,
		EntityType: entityType, EntityID: entityID, DetailJson: raw,
	})
}

// movement records one stock change. delta is the change of the quantity the
// reason names: RECEIVE/ADJUST/WRITE_OFF/COMMIT change on hand; RESERVE and
// RELEASE change reserved.
func movement(ctx context.Context, q *store.Queries, variantID string, delta int, reason store.MovementReason, ref, actor string) error {
	return q.InsertMovement(ctx, store.InsertMovementParams{
		VariantID: variantID, Delta: int32(delta), Reason: reason, Ref: ref, ActorEmail: nullStr(actor), // #nosec G115
	})
}
