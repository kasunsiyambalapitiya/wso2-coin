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

package checkout

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

type fakeTreasury struct {
	addr  string
	err   error
	calls int
}

func (f *fakeTreasury) Treasury(context.Context) (string, error) {
	f.calls++
	return f.addr, f.err
}

func TestTreasuryGuardChecksBeforeFirstPaymentThenEveryTenMinutes(t *testing.T) {
	f := &fakeTreasury{addr: "0xTreasury"}
	now := time.Unix(1000, 0)
	g := NewTreasuryGuard("0xtreasury", f)
	g.now = func() time.Time { return now }

	if g.State() != TreasuryUnknown {
		t.Fatalf("state before any check = %s", g.State())
	}
	if !g.Allow(context.Background()) || f.calls != 1 {
		t.Fatalf("first payment: allowed=%v calls=%d", g.Allow(context.Background()), f.calls)
	}
	now = now.Add(9 * time.Minute)
	g.Allow(context.Background())
	if f.calls != 1 {
		t.Fatalf("re-checked inside 10 min: %d calls", f.calls)
	}
	now = now.Add(2 * time.Minute)
	g.Allow(context.Background())
	if f.calls != 2 {
		t.Fatalf("not re-checked after 10 min: %d calls", f.calls)
	}
	if g.State() != TreasuryOK {
		t.Fatalf("state = %s", g.State())
	}
}

func TestTreasuryGuardMismatchPauses(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	f := &fakeTreasury{addr: "0xsomeone-else"}
	now := time.Unix(1000, 0)
	g := NewTreasuryGuard("0xtreasury", f)
	g.now = func() time.Time { return now }

	if g.Allow(context.Background()) {
		t.Fatal("mismatched treasury allowed a payment")
	}
	if g.State() != TreasuryMismatch {
		t.Fatalf("state = %s", g.State())
	}
	if !strings.Contains(buf.String(), "treasury_mismatch") {
		t.Fatalf("no treasury_mismatch log: %s", buf.String())
	}
	// Stays paused without hammering transaction-service until the next check.
	g.Allow(context.Background())
	if f.calls != 1 {
		t.Fatalf("mismatch re-checked at once: %d calls", f.calls)
	}
	f.addr = "0xtreasury"
	now = now.Add(11 * time.Minute)
	if !g.Allow(context.Background()) {
		t.Fatal("fixed mapping not picked up at the next check")
	}
}

func TestTreasuryGuardFailedCheckPausesAndRetries(t *testing.T) {
	f := &fakeTreasury{err: errors.New("down")}
	now := time.Unix(1000, 0)
	g := NewTreasuryGuard("0xtreasury", f)
	g.now = func() time.Time { return now }

	if g.Allow(context.Background()) {
		t.Fatal("allowed before any successful check")
	}
	if g.State() != TreasuryError {
		t.Fatalf("state = %s", g.State())
	}
	g.Allow(context.Background())
	if f.calls != 1 {
		t.Fatalf("retried at once: %d", f.calls)
	}
	f.err, f.addr = nil, "0xtreasury"
	now = now.Add(31 * time.Second)
	if !g.Allow(context.Background()) || f.calls != 2 {
		t.Fatalf("no retry after 30 s: calls=%d", f.calls)
	}
}

func TestTreasuryGuardFailedRefreshAfterOKPauses(t *testing.T) {
	f := &fakeTreasury{addr: "0xtreasury"}
	now := time.Unix(1000, 0)
	g := NewTreasuryGuard("0xtreasury", f)
	g.now = func() time.Time { return now }
	g.Allow(context.Background())
	f.err = errors.New("down")
	now = now.Add(11 * time.Minute)
	if g.Allow(context.Background()) {
		t.Fatal("a stale check that then failed still allowed payments")
	}
}
