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

package main

import (
	"context"
	"fmt"
	"time"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/checkout"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/coins"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/jobs"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/notifications"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/repository"
)

// simple adapts a job that keeps no cursor.
func simple(f func(ctx context.Context) error) func(context.Context, repository.JobState) (string, error) {
	return func(ctx context.Context, _ repository.JobState) (string, error) { return "", f(ctx) }
}

// treasuryRefresher forces a treasury cross-check (checkout.TreasuryGuard).
type treasuryRefresher interface {
	Refresh(ctx context.Context) checkout.TreasuryState
}

// treasuryCheck runs the check and fails the job unless the pin matched, so
// the saved job status agrees with the payment guard.
func treasuryCheck(g treasuryRefresher) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		if state := g.Refresh(ctx); state != checkout.TreasuryOK {
			return fmt.Errorf("treasury check: %s; payments paused", state)
		}
		return nil
	}
}

// backgroundJobs is the job table (backend-spec §9).
func backgroundJobs(cfg *Config, repo *repository.Store, svc *checkout.Service, ledger *coins.Client,
	sender notifications.Sender, common notifications.Common) []jobs.Job {
	outbox := jobs.NewOutbox(repo, sender, common, cfg.ContactEmail)
	reconcile := jobs.NewTreasuryReconcile(ledger, repo, svc, cfg.TreasuryWallet)
	days := func(n int) time.Duration { return time.Duration(n) * 24 * time.Hour }
	return []jobs.Job{
		{Name: "outbox", Every: 15 * time.Second, Run: simple(outbox.Run)},
		{Name: "payment-reconciler", Every: 30 * time.Second, Run: simple(jobs.PaymentReconciler(repo, svc, time.Now))},
		{Name: "treasury-check", Every: 10 * time.Minute, Run: simple(treasuryCheck(svc.Treasury()))},
		{Name: "treasury-reconcile", Daily: &jobs.Daily{Hour: 2, Loc: time.UTC}, Run: reconcile.Run},
		{Name: "pickup-reminder", Every: time.Hour, Run: simple(func(ctx context.Context) error {
			_, err := repo.RemindPickups(ctx, time.Now().Add(-days(cfg.PickupReminderDays)))
			return err
		})},
		{Name: "uncollected-flag", Daily: &jobs.Daily{Hour: 3, Loc: time.UTC}, Run: simple(func(ctx context.Context) error {
			_, err := repo.FlagUncollected(ctx, time.Now().Add(-days(cfg.UncollectedFlagDays)))
			return err
		})},
		{Name: "stats", Every: time.Hour, Run: simple(func(ctx context.Context) error {
			return repo.RecomputeStats(ctx, time.Now().Add(-days(30)))
		})},
		{Name: "low-stock-digest", Daily: &jobs.Daily{Hour: 8, Loc: cfg.Timezone}, Run: simple(func(ctx context.Context) error {
			items, err := repo.LowStock(ctx, cfg.LowStockThreshold)
			if err != nil || len(items) == 0 {
				return err
			}
			day := time.Now().In(cfg.Timezone).Format(time.DateOnly)
			return repo.QueueEmail(ctx, notifications.TemplateLowStockDigest, cfg.ContactEmail,
				notifications.LowStockDigest{Threshold: cfg.LowStockThreshold, Items: items}, "digest:"+day)
		})},
	}
}
