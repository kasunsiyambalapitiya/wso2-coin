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

package jobs

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/coins"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/notifications"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/orders"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/repository"
)

func TestLastScheduled(t *testing.T) {
	colombo, _ := time.LoadLocation("Asia/Colombo")
	d := Daily{Hour: 8, Minute: 0, Loc: colombo}
	now := time.Date(2026, 11, 11, 9, 30, 0, 0, colombo)
	if got := d.Last(now); !got.Equal(time.Date(2026, 11, 11, 8, 0, 0, 0, colombo)) {
		t.Fatalf("after 08:00 = %v", got)
	}
	now = time.Date(2026, 11, 11, 7, 59, 0, 0, colombo)
	if got := d.Last(now); !got.Equal(time.Date(2026, 11, 10, 8, 0, 0, 0, colombo)) {
		t.Fatalf("before 08:00 = %v", got)
	}
}

type fakeLocks struct {
	locked bool
	state  repository.JobState
	saved  []repository.JobState
}

func (f *fakeLocks) WithJobLock(ctx context.Context, _ string, fn func(context.Context) error) (bool, error) {
	if f.locked {
		return false, nil
	}
	return true, fn(ctx)
}
func (f *fakeLocks) GetJobState(context.Context, string) (repository.JobState, error) {
	return f.state, nil
}
func (f *fakeLocks) SaveJobState(_ context.Context, _ string, st repository.JobState) error {
	f.saved = append(f.saved, st)
	f.state = st
	return nil
}

func TestRunnerTickLockAndDaily(t *testing.T) {
	locks := &fakeLocks{}
	now := time.Date(2026, 11, 11, 2, 30, 0, 0, time.UTC)
	r := NewRunner(locks)
	r.now = func() time.Time { return now }
	runs := 0
	job := Job{Name: "treasury-reconcile", Daily: &Daily{Hour: 2, Loc: time.UTC},
		Run: func(_ context.Context, st repository.JobState) (string, error) { runs++; return "cursor-1", nil }}

	r.tick(context.Background(), job)
	r.tick(context.Background(), job)
	if runs != 1 || locks.state.Cursor != "cursor-1" || locks.state.Status != "ok" {
		t.Fatalf("daily job ran %d times, state %+v", runs, locks.state)
	}
	now = now.Add(24 * time.Hour)
	r.tick(context.Background(), job)
	if runs != 2 {
		t.Fatalf("next day not run: %d", runs)
	}

	locks.locked = true
	every := Job{Name: "outbox", Every: time.Second, Run: func(context.Context, repository.JobState) (string, error) { runs++; return "", nil }}
	r.tick(context.Background(), every)
	if runs != 2 {
		t.Fatal("ran without the lock")
	}
	locks.locked = false
	failing := Job{Name: "stats", Every: time.Second, Run: func(context.Context, repository.JobState) (string, error) {
		return "", errors.New("boom")
	}}
	r.tick(context.Background(), failing)
	if locks.state.Status != "failed" {
		t.Fatalf("failed run recorded as %q", locks.state.Status)
	}
}

type fakeOutbox struct {
	emails []repository.OutboxEmail
	sent   []int64
	retry  map[int64]time.Time
	failed []int64
}

func (f *fakeOutbox) ClaimDueEmails(context.Context) ([]repository.OutboxEmail, error) {
	return f.emails, nil
}
func (f *fakeOutbox) EmailSent(_ context.Context, id int64) error {
	f.sent = append(f.sent, id)
	return nil
}
func (f *fakeOutbox) EmailRetry(_ context.Context, id int64, next time.Time, _ string) error {
	f.retry[id] = next
	return nil
}
func (f *fakeOutbox) EmailFailed(_ context.Context, id int64, _ string) error {
	f.failed = append(f.failed, id)
	return nil
}

type fakeSender struct {
	err  error
	sent []string
	to   [][]string
	rto  [][]string
}

func (f *fakeSender) SendEmail(_ context.Context, to, _, _, replyTo []string, subject, _ string, _ []notifications.EmailAttachment) error {
	f.sent = append(f.sent, subject)
	f.to = append(f.to, to)
	f.rto = append(f.rto, replyTo)
	return f.err
}

func orderPayload() []byte {
	return []byte(`{"orderNo":"SS-10001","firstName":"Your","officeName":"Colombo office","total":90,"lines":[{"name":"Hoodie","quantity":1,"unitPrice":90}],"placedAt":"2026-11-11T05:00:00Z"}`)
}

func TestOutboxSendsAndBacksOff(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	store := &fakeOutbox{retry: map[int64]time.Time{}, emails: []repository.OutboxEmail{
		{ID: 1, Template: notifications.TemplateOrderConfirmed, To: "you@wso2.com", Payload: orderPayload(), Attempts: 0},
	}}
	sender := &fakeSender{}
	o := NewOutbox(store, sender, notifications.Common{StoreURL: "https://one.example", ContactEmail: "swag@example.com"}, "swag@example.com")
	o.now = func() time.Time { return now }
	if err := o.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.sent) != 1 || !strings.Contains(sender.sent[0], "SS-10001") || sender.to[0][0] != "you@wso2.com" || sender.rto[0][0] != "swag@example.com" {
		t.Fatalf("sent = %v %v %v", store.sent, sender.sent, sender.to)
	}

	sender.err = errors.New("smtp down")
	want := []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 12 * time.Hour}
	for attempts, wait := range want {
		store.emails[0].Attempts = attempts
		store.retry = map[int64]time.Time{}
		_ = o.Run(context.Background())
		if got := store.retry[1]; !got.Equal(now.Add(wait)) {
			t.Fatalf("after attempt %d: next %v, want +%v", attempts+1, got.Sub(now), wait)
		}
	}
	store.emails[0].Attempts = 5
	store.retry = map[int64]time.Time{}
	_ = o.Run(context.Background())
	if len(store.failed) != 1 || len(store.retry) != 0 {
		t.Fatalf("sixth failure: failed=%v retry=%v", store.failed, store.retry)
	}
}

func TestOutboxBadPayloadFailsAtOnceAndUnconfiguredWaits(t *testing.T) {
	store := &fakeOutbox{retry: map[int64]time.Time{}, emails: []repository.OutboxEmail{{ID: 7, Template: "nope", To: "x@wso2.com", Payload: []byte(`{}`)}}}
	o := NewOutbox(store, &fakeSender{}, notifications.Common{}, "")
	_ = o.Run(context.Background())
	if len(store.failed) != 1 {
		t.Fatalf("unknown template not failed: %v", store.failed)
	}
	store = &fakeOutbox{retry: map[int64]time.Time{}, emails: []repository.OutboxEmail{{ID: 8}}}
	o = NewOutbox(store, nil, notifications.Common{}, "")
	_ = o.Run(context.Background())
	if len(store.sent)+len(store.failed)+len(store.retry) != 0 {
		t.Fatal("unconfigured email touched the outbox")
	}
}

func TestPaymentDue(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	cases := []struct {
		age    time.Duration
		checks int
		want   bool
	}{
		{30 * time.Second, 0, false}, {61 * time.Second, 0, true},
		{3 * time.Minute, 1, false}, {5 * time.Minute, 1, true},
		{10 * time.Minute, 2, false}, {15 * time.Minute, 2, true}, {40 * time.Minute, 7, true},
	}
	for _, tc := range cases {
		o := repository.Order{PlacedAt: now.Add(-tc.age), PaymentChecks: tc.checks}
		if got := paymentDue(o, now); got != tc.want {
			t.Errorf("age %v checks %d: %v", tc.age, tc.checks, got)
		}
	}
}

// --- treasury reconcile ---

type fakeLedger struct {
	pages   []*coins.TransactionPage
	filters []coins.SearchFilter
}

func (f *fakeLedger) Search(_ context.Context, flt coins.SearchFilter) (*coins.TransactionPage, error) {
	f.filters = append(f.filters, flt)
	if len(f.pages) == 0 {
		return &coins.TransactionPage{}, nil
	}
	p := f.pages[0]
	f.pages = f.pages[1:]
	return p, nil
}

type fakeTreasuryStore struct {
	orders  map[string]*repository.Order
	late    []string
	flags   map[string]string
	actions []string
}

func (f *fakeTreasuryStore) OrderByID(_ context.Context, id string) (*repository.Order, error) {
	return f.orders[id], nil
}
func (f *fakeTreasuryStore) ConfirmLatePayment(_ context.Context, id string) error {
	f.late = append(f.late, id)
	return nil
}
func (f *fakeTreasuryStore) Flag(_ context.Context, id, reason string) error {
	f.flags[id] = reason
	return nil
}
func (f *fakeTreasuryStore) SystemAction(_ context.Context, action, _, id string, _ any) error {
	f.actions = append(f.actions, action+":"+id)
	return nil
}

type fakePendingReconciler struct{ seen []string }

func (f *fakePendingReconciler) ReconcilePending(_ context.Context, o *repository.Order) (string, error) {
	f.seen = append(f.seen, o.ID)
	return "confirmed", nil
}

func TestTreasuryReconcile(t *testing.T) {
	const (
		failedID  = "0192f3a4-0000-7000-8000-000000000001"
		pendingID = "0192f3a4-0000-7000-8000-000000000002"
		okID      = "0192f3a4-0000-7000-8000-000000000003"
		shortID   = "0192f3a4-0000-7000-8000-000000000004"
		ghostID   = "0192f3a4-0000-7000-8000-000000000005"
	)
	st := &fakeTreasuryStore{flags: map[string]string{}, orders: map[string]*repository.Order{
		failedID:  {ID: failedID, Status: orders.Failed, Total: 90},
		pendingID: {ID: pendingID, Status: orders.PendingPayment, Total: 40},
		okID:      {ID: okID, Status: orders.Confirmed, Total: 50},
		shortID:   {ID: shortID, Status: orders.Confirmed, Total: 70},
	}}
	ledger := &fakeLedger{pages: []*coins.TransactionPage{{Total: 6, Transactions: []coins.Transaction{
		{Reference: orders.PaymentReference(failedID), Amount: "90", Timestamp: "2026-11-11T01:00:00Z"},
		{Reference: orders.PaymentReference(pendingID), Amount: "40", Timestamp: "2026-11-11T01:10:00Z"},
		{Reference: orders.PaymentReference(okID), Amount: "50", Timestamp: "2026-11-11T01:20:00Z"},
		{Reference: orders.PaymentReference(shortID), Amount: "7", Timestamp: "2026-11-11T01:30:00Z"},
		{Reference: orders.PaymentReference(ghostID), Amount: "10", Timestamp: "2026-11-11T01:40:00Z"},
		{Reference: "0xabc", Amount: "10", Timestamp: "2026-11-11T01:50:00Z"},
	}}}}
	pend := &fakePendingReconciler{}
	tr := NewTreasuryReconcile(ledger, st, pend, "0xtreasury")
	cursor, err := tr.Run(context.Background(), repository.JobState{Cursor: "2026-11-10T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	if f := ledger.filters[0]; len(f.ToAddresses) != 1 || f.ToAddresses[0] != "0xtreasury" || f.DateFrom != "2026-11-08T00:00:00Z" {
		t.Fatalf("search = %+v", f)
	}
	if len(st.late) != 1 || st.late[0] != failedID {
		t.Fatalf("late = %v", st.late)
	}
	if len(pend.seen) != 1 || pend.seen[0] != pendingID {
		t.Fatalf("pending = %v", pend.seen)
	}
	if st.flags[shortID] != "payment_mismatch" || st.flags[okID] != "" {
		t.Fatalf("flags = %v", st.flags)
	}
	if len(st.actions) != 1 || !strings.HasPrefix(st.actions[0], "unknown_inflow:") {
		t.Fatalf("actions = %v", st.actions)
	}
	if cursor != "2026-11-11T01:50:00Z" {
		t.Fatalf("cursor = %q", cursor)
	}
}

func TestFailedDailyRunRetriesNextTick(t *testing.T) {
	locks := &fakeLocks{}
	now := time.Date(2026, 11, 11, 2, 30, 0, 0, time.UTC)
	r := NewRunner(locks)
	r.now = func() time.Time { return now }
	runs := 0
	job := Job{Name: "treasury-reconcile", Daily: &Daily{Hour: 2, Loc: time.UTC},
		Run: func(context.Context, repository.JobState) (string, error) { runs++; return "", errors.New("ts down") }}
	r.tick(context.Background(), job)
	now = now.Add(time.Minute)
	r.tick(context.Background(), job)
	if runs != 2 {
		t.Fatalf("a failed daily run was not retried: %d runs", runs)
	}
}

func TestTreasuryReconcileLooksBack48Hours(t *testing.T) {
	ledger := &fakeLedger{}
	tr := NewTreasuryReconcile(ledger, &fakeTreasuryStore{flags: map[string]string{}}, &fakePendingReconciler{}, "0xtreasury")
	cursor, err := tr.Run(context.Background(), repository.JobState{Cursor: "2026-11-10T02:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	if got := ledger.filters[0].DateFrom; got != "2026-11-08T02:00:00Z" {
		t.Fatalf("dateFrom = %s, want the cursor minus 48 h", got)
	}
	if cursor != "2026-11-10T02:00:00Z" {
		t.Fatalf("an empty run moved the cursor: %s", cursor)
	}
}
