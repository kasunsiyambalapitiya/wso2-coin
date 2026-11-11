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
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Treasury check timing (backend-spec §8.7).
const (
	treasuryRefreshEvery = 10 * time.Minute
	treasuryRetryAfter   = 30 * time.Second
	treasuryCheckTimeout = 10 * time.Second
)

// TreasuryState is the result of the last treasury cross-check.
type TreasuryState string

const (
	TreasuryUnknown  TreasuryState = "unknown"  // never checked
	TreasuryOK       TreasuryState = "ok"       // pin matches /wallets/master/balance
	TreasuryMismatch TreasuryState = "mismatch" // pin differs: payments pause
	TreasuryError    TreasuryState = "error"    // the last check failed: payments pause
)

// TreasuryLookup returns the wallet transaction-service maps to the store's
// client id (coins.Client.Treasury).
type TreasuryLookup interface {
	Treasury(ctx context.Context) (string, error)
}

// TreasuryGuard holds the pinned treasury address and cross-checks it against
// transaction-service before the first payment and then every 10 minutes.
// Payments are allowed only while the last check succeeded and matched.
type TreasuryGuard struct {
	pin    string
	lookup TreasuryLookup
	now    func() time.Time

	mu          sync.Mutex
	state       TreasuryState
	lastAttempt time.Time
}

// NewTreasuryGuard pins STORE_TREASURY_WALLET_ADDRESS. It contacts nothing.
func NewTreasuryGuard(pin string, lookup TreasuryLookup) *TreasuryGuard {
	return &TreasuryGuard{pin: strings.TrimSpace(pin), lookup: lookup, now: time.Now, state: TreasuryUnknown}
}

// Pin is the only address a payment may name as toAddress.
func (g *TreasuryGuard) Pin() string { return g.pin }

// IsPin reports whether addr is the pinned treasury (hex is case-insensitive).
func (g *TreasuryGuard) IsPin(addr string) bool {
	return strings.EqualFold(strings.TrimSpace(addr), g.pin)
}

// State reports the last result, for /health/dependencies.
func (g *TreasuryGuard) State() TreasuryState {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.state
}

// Allow runs the check when it is due and reports whether payments may go
// ahead. A due check runs while the lock is held, so concurrent callers wait
// for one result instead of each calling transaction-service.
func (g *TreasuryGuard) Allow(ctx context.Context) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.due() {
		g.check(ctx)
	}
	return g.state == TreasuryOK
}

// Refresh forces a check now (used by the periodic job).
func (g *TreasuryGuard) Refresh(ctx context.Context) TreasuryState {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.check(ctx)
	return g.state
}

func (g *TreasuryGuard) due() bool {
	since := g.now().Sub(g.lastAttempt)
	switch g.state {
	case TreasuryUnknown:
		return true
	case TreasuryError:
		return since >= treasuryRetryAfter
	default:
		return since >= treasuryRefreshEvery
	}
}

func (g *TreasuryGuard) check(ctx context.Context) {
	g.lastAttempt = g.now()
	// The check is shared by every caller, so one caller's cancel or short
	// deadline (a health probe) must not decide it.
	lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), treasuryCheckTimeout)
	defer cancel()
	addr, err := g.lookup.Treasury(lctx)
	switch {
	case err != nil:
		g.state = TreasuryError
		slog.ErrorContext(ctx, "treasury check failed; payments paused", "err", err)
	case !g.IsPin(addr):
		g.state = TreasuryMismatch
		slog.ErrorContext(ctx, "treasury_mismatch",
			"detail", "STORE_TREASURY_WALLET_ADDRESS differs from /wallets/master/balance; payments paused",
			"pinned", g.pin, "reported", addr)
	default:
		g.state = TreasuryOK
	}
}
