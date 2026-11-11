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

// Package jobs runs the in-process background jobs. Each job runs on its own
// ticker, and a run happens only on the replica that gets the job's MySQL
// GET_LOCK, so each job runs once across replicas.
package jobs

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/checkout"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/coins"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/notifications"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/orders"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/repository"
)

// Daily is a once-a-day schedule in a time zone.
type Daily struct {
	Hour, Minute int
	Loc          *time.Location
}

// Last is the most recent scheduled time at or before now.
func (d Daily) Last(now time.Time) time.Time {
	n := now.In(d.Loc)
	t := time.Date(n.Year(), n.Month(), n.Day(), d.Hour, d.Minute, 0, 0, d.Loc)
	if t.After(n) {
		t = t.AddDate(0, 0, -1)
	}
	return t
}

// Job is one background job. Every is the tick; a Daily job checks each tick
// whether today's run is still due. Run gets the saved state and returns the
// cursor to save.
type Job struct {
	Name  string
	Every time.Duration
	Daily *Daily
	Run   func(ctx context.Context, st repository.JobState) (string, error)
}

// LockStore is the job lock and state (repository.Store).
type LockStore interface {
	WithJobLock(ctx context.Context, name string, fn func(context.Context) error) (bool, error)
	GetJobState(ctx context.Context, job string) (repository.JobState, error)
	SaveJobState(ctx context.Context, job string, st repository.JobState) error
}

// Runner starts and ticks the jobs.
type Runner struct {
	store LockStore
	now   func() time.Time
}

// NewRunner builds a runner.
func NewRunner(s LockStore) *Runner { return &Runner{store: s, now: time.Now} }

// Start runs every job on its own goroutine until ctx is cancelled.
func (r *Runner) Start(ctx context.Context, jobs []Job) {
	for _, j := range jobs {
		every := j.Every
		if j.Daily != nil && every == 0 {
			every = time.Minute
		}
		go func(j Job) {
			t := time.NewTicker(every)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					r.tick(ctx, j)
				}
			}
		}(j)
	}
}

func (r *Runner) tick(ctx context.Context, j Job) {
	defer func() {
		if p := recover(); p != nil {
			slog.ErrorContext(ctx, "job_run", "job", j.Name, "status", "failed", "panic", p)
		}
	}()
	ran, err := r.store.WithJobLock(ctx, j.Name, func(ctx context.Context) error {
		st, err := r.store.GetJobState(ctx, j.Name)
		if err != nil {
			return err
		}
		now := r.now()
		if j.Daily != nil && st.LastRunAt != nil && !st.LastRunAt.Before(j.Daily.Last(now)) {
			return errSkip
		}
		cursor, runErr := j.Run(ctx, st)
		next := repository.JobState{Cursor: st.Cursor, LastRunAt: &now, Status: "ok"}
		if runErr != nil {
			// Keep the last good run, so a daily job tries again on the next tick.
			next.LastRunAt = st.LastRunAt
			next.Status = "failed"
			slog.ErrorContext(ctx, "job_run", "job", j.Name, "status", "failed", "err", runErr)
		} else if cursor != "" {
			next.Cursor = cursor
		}
		return r.store.SaveJobState(ctx, j.Name, next)
	})
	if errors.Is(err, errSkip) || !ran {
		return
	}
	if err != nil {
		slog.ErrorContext(ctx, "job_run", "job", j.Name, "status", "failed", "err", err)
	}
}

var errSkip = errors.New("not due")

// --- outbox ---

// retryBackoff is the wait after attempt 1..5; a sixth failure is final.
var retryBackoff = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 12 * time.Hour}

// OutboxStore is the outbox persistence.
type OutboxStore interface {
	ClaimDueEmails(ctx context.Context) ([]repository.OutboxEmail, error)
	EmailSent(ctx context.Context, id int64) error
	EmailRetry(ctx context.Context, id int64, next time.Time, reason string) error
	EmailFailed(ctx context.Context, id int64, reason string) error
}

// Outbox renders and sends queued emails.
type Outbox struct {
	store   OutboxStore
	sender  notifications.Sender // nil: email not configured, rows wait
	common  notifications.Common
	replyTo string
	now     func() time.Time
}

// NewOutbox builds the job. A nil sender leaves every row pending.
func NewOutbox(s OutboxStore, sender notifications.Sender, common notifications.Common, replyTo string) *Outbox {
	return &Outbox{store: s, sender: sender, common: common, replyTo: replyTo, now: time.Now}
}

// Run sends one batch.
func (o *Outbox) Run(ctx context.Context) error {
	if o.sender == nil {
		return nil
	}
	emails, err := o.store.ClaimDueEmails(ctx)
	if err != nil {
		return err
	}
	var replyTo []string
	if o.replyTo != "" {
		replyTo = []string{o.replyTo}
	}
	for _, e := range emails {
		r, err := notifications.Render(e.Template, e.Payload, o.common)
		if err != nil {
			slog.ErrorContext(ctx, "outbox_failed", "id", e.ID, "template", e.Template, "err", err)
			if err := o.store.EmailFailed(ctx, e.ID, "render: "+err.Error()); err != nil {
				return err
			}
			continue
		}
		if err := o.sender.SendEmail(ctx, []string{e.To}, nil, nil, replyTo, r.Subject, r.HTML, nil); err != nil {
			attempt := e.Attempts + 1
			if attempt > len(retryBackoff) {
				slog.ErrorContext(ctx, "outbox_failed", "id", e.ID, "template", e.Template, "attempts", attempt, "err", err)
				if err := o.store.EmailFailed(ctx, e.ID, err.Error()); err != nil {
					return err
				}
				continue
			}
			slog.WarnContext(ctx, "outbox send failed; will retry", "id", e.ID, "attempt", attempt, "err", err)
			if err := o.store.EmailRetry(ctx, e.ID, o.now().Add(retryBackoff[attempt-1]), err.Error()); err != nil {
				return err
			}
			continue
		}
		slog.InfoContext(ctx, "outbox_sent", "id", e.ID, "template", e.Template)
		if err := o.store.EmailSent(ctx, e.ID); err != nil {
			return err
		}
	}
	return nil
}

// --- payment reconciler ---

// paymentDue reports whether a pending order is due for its next ledger look:
// at +1, +5 and +15 minutes.
func paymentDue(o repository.Order, now time.Time) bool {
	age := now.Sub(o.PlacedAt)
	switch {
	case o.PaymentChecks == 0:
		return age >= time.Minute
	case o.PaymentChecks == 1:
		return age >= 5*time.Minute
	default:
		return age >= 15*time.Minute
	}
}

// PendingStore lists pending orders.
type PendingStore interface {
	PendingOrdersOlderThan(ctx context.Context, cutoff time.Time) ([]repository.Order, error)
}

// PendingReconciler settles one pending order (checkout.Service).
type PendingReconciler interface {
	ReconcilePending(ctx context.Context, o *repository.Order) (string, error)
}

// PaymentReconciler is the 30 s job.
func PaymentReconciler(store PendingStore, svc PendingReconciler, now func() time.Time) func(context.Context) error {
	return func(ctx context.Context) error {
		t := now()
		list, err := store.PendingOrdersOlderThan(ctx, t.Add(-time.Minute))
		if err != nil {
			return err
		}
		for i := range list {
			if !paymentDue(list[i], t) {
				continue
			}
			res, err := svc.ReconcilePending(ctx, &list[i])
			if err != nil {
				slog.WarnContext(ctx, "reconcile_result", "orderNo", list[i].OrderNo, "result", "error", "err", err)
				continue
			}
			slog.InfoContext(ctx, "reconcile_result", "orderNo", list[i].OrderNo, "result", res)
		}
		return nil
	}
}

// --- treasury reconcile ---

// Ledger searches transaction-service (coins.Client).
type Ledger interface {
	Search(ctx context.Context, f coins.SearchFilter) (*coins.TransactionPage, error)
}

// TreasuryStore is what the nightly reconciliation changes.
type TreasuryStore interface {
	OrderByID(ctx context.Context, id string) (*repository.Order, error)
	ConfirmLatePayment(ctx context.Context, orderID string) error
	Flag(ctx context.Context, orderID, reason string) error
	SystemAction(ctx context.Context, action, entityType, entityID string, detail any) error
}

// TreasuryReconcile matches every swag:ord: inflow into the pinned treasury
// to its order (backend-spec §9). It searches the pin only, so a payment that
// went anywhere else can never confirm an order.
type TreasuryReconcile struct {
	ledger  Ledger
	store   TreasuryStore
	pending PendingReconciler
	pin     string
}

// NewTreasuryReconcile builds the job.
func NewTreasuryReconcile(l Ledger, s TreasuryStore, p PendingReconciler, pin string) *TreasuryReconcile {
	return &TreasuryReconcile{ledger: l, store: s, pending: p, pin: pin}
}

const (
	reconcilePage     = 100
	reconcileLookback = 48 * time.Hour
)

// Run reads inflows since the cursor and returns the newest timestamp seen.
func (t *TreasuryReconcile) Run(ctx context.Context, st repository.JobState) (string, error) {
	newest := st.Cursor
	// The cursor is a ledger timestamp, but a payment can commit after a later
	// one was read. Look back 48 h every night; matching is idempotent.
	cursor := st.Cursor
	if c, err := time.Parse(time.RFC3339, st.Cursor); err == nil {
		cursor = c.Add(-reconcileLookback).UTC().Format(time.RFC3339)
	}
	for offset, pages := 0, 0; pages < 50; pages++ {
		page, err := t.ledger.Search(ctx, coins.SearchFilter{ToAddresses: []string{t.pin}, DateFrom: cursor, Limit: reconcilePage, Offset: offset})
		if err != nil {
			return newest, err
		}
		for _, tx := range page.Transactions {
			if err := t.match(ctx, tx); err != nil {
				return newest, err
			}
			if tx.Timestamp > newest {
				newest = tx.Timestamp
			}
		}
		offset += len(page.Transactions)
		if len(page.Transactions) == 0 || offset >= page.Total {
			break
		}
	}
	return newest, nil
}

func (t *TreasuryReconcile) match(ctx context.Context, tx coins.Transaction) error {
	id, ok := orders.OrderIDFromReference(tx.Reference)
	if !ok {
		return nil // not a store payment
	}
	o, err := t.store.OrderByID(ctx, id)
	if err != nil {
		return err
	}
	if o == nil {
		slog.ErrorContext(ctx, "reconcile_result", "result", "unknown_order", "reference", tx.Reference)
		return t.store.SystemAction(ctx, "unknown_inflow", "transaction", tx.Reference, map[string]string{"amount": tx.Amount})
	}
	if !checkout.AmountEquals(tx.Amount, o.Total) {
		slog.ErrorContext(ctx, "reconcile_result", "result", "amount_mismatch", "orderNo", o.OrderNo)
		return t.store.Flag(ctx, o.ID, checkout.AttentionPaymentMismatch)
	}
	switch o.Status {
	case orders.Failed:
		err := t.store.ConfirmLatePayment(ctx, o.ID)
		if errors.Is(err, repository.ErrNoStockForLatePayment) || errors.Is(err, repository.ErrStateConflict) {
			slog.WarnContext(ctx, "reconcile_result", "result", "late_payment_no_stock", "orderNo", o.OrderNo)
			return nil
		}
		if err == nil {
			slog.InfoContext(ctx, "reconcile_result", "result", "late_payment_confirmed", "orderNo", o.OrderNo)
		}
		return err
	case orders.PendingPayment:
		_, err := t.pending.ReconcilePending(ctx, o)
		return err
	}
	return nil
}
