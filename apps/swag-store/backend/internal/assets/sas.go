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

package assets

import (
	"context"
	"log/slog"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/sas"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/catalog"
)

const (
	day = 24 * time.Hour
	// Each photo gets one read SAS per UTC day, for that blob only, from 15
	// minutes before that day's midnight to 48 hours after it: a URL handed out
	// at any time works for at least 24 h, and stays the same all day.
	sasLead     = 15 * time.Minute
	sasLifetime = 48 * time.Hour
	// Delegation keys: Azure allows 7 days. The key starts before today's
	// midnight so it covers today's SAS, and is renewed with 3 days left.
	keyLead       = 30 * time.Minute
	keyLifetime   = 7*day - 5*time.Minute
	keyRenewBelow = 72 * time.Hour
	keyCheckEvery = time.Hour
	keyErrLogGap  = time.Minute
	// After a failed fetch, try again soon: 1 min, doubling up to the hourly check.
	keyRetryFirst = time.Minute
)

// ReadURL is the signed, read-only URL of a photo, or nil when photos are off,
// no key is held yet, or path isn't a photo name.
func (c *Client) ReadURL(path string) *string {
	if c.disabled {
		return nil
	}
	if _, _, ok := catalog.ParsePhotoPath(path); !ok {
		slog.Warn("assets_bad_path", "path", path)
		return nil
	}
	q := c.daySAS(path)
	if q == "" {
		return nil
	}
	u := c.accountURL + "/" + c.container + "/" + path + "?" + q
	return &u
}

// daySAS is today's read SAS for one blob, signed once per photo per UTC day.
func (c *Client) daySAS(path string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clock().UTC()
	today := now.Truncate(day)
	if !c.sasDay.Equal(today) || c.sasQuery == nil {
		c.sasDay, c.sasQuery = today, map[string]string{}
	}
	if q, ok := c.sasQuery[path]; ok {
		return q
	}
	expiry := today.Add(sasLifetime)
	if c.sign == nil || (!c.signUntil.IsZero() && c.signUntil.Before(expiry)) {
		return ""
	}
	q, err := c.sign(sas.BlobSignatureValues{
		Protocol:      c.protocol,
		StartTime:     today.Add(-sasLead),
		ExpiryTime:    expiry,
		Permissions:   (&sas.BlobPermissions{Read: true}).String(),
		ContainerName: c.container,
		BlobName:      path,
	})
	if err != nil {
		slog.Error("assets_sign_failed", "err", err)
		return ""
	}
	c.sasQuery[path] = q.Encode()
	return c.sasQuery[path]
}

// Start fetches the first delegation key and keeps it fresh until ctx ends.
// It does nothing with a shared key or when photos are off.
func (c *Client) Start(ctx context.Context) {
	if c.disabled || c.fetchKey == nil {
		return
	}
	go func() {
		t := time.NewTimer(c.refreshKey(ctx))
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				t.Reset(c.refreshKey(ctx))
			}
		}
	}()
}

// refreshKey gets a new delegation key when there is none or less than three
// days are left on it, and says how long to wait before checking again: an
// hour normally, less after a failure so photos come back soon.
func (c *Client) refreshKey(ctx context.Context) time.Duration {
	now := c.clock().UTC()
	c.mu.Lock()
	fresh := c.sign != nil && c.signUntil.Sub(now) >= keyRenewBelow
	c.mu.Unlock()
	if fresh {
		return keyCheckEvery
	}
	start, expiry := now.Truncate(day).Add(-keyLead), now.Add(keyLifetime)
	fetchCtx, cancel := context.WithTimeout(ctx, c.fetchTimeout())
	sign, err := c.fetchKey(fetchCtx, start, expiry)
	cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		if now.Sub(c.keyErrLog) >= keyErrLogGap {
			slog.Error("assets_key_failed", "err", err)
			c.keyErrLog = now
		}
		c.keyRetry = min(max(2*c.keyRetry, keyRetryFirst), keyCheckEvery)
		return c.keyRetry
	}
	c.sign, c.signUntil, c.keyRetry = sign, expiry, 0
	c.sasQuery = nil // sign today's URLs again with the new key
	return keyCheckEvery
}

func (c *Client) fetchTimeout() time.Duration {
	if c.timeout > 0 {
		return c.timeout
	}
	return 25 * time.Second
}

// keyUsable: today's URLs can be signed (they need the key until 48 h after
// today's midnight). The caller holds c.mu.
func (c *Client) keyUsable(now time.Time) bool {
	return c.sign != nil && (c.signUntil.IsZero() || !c.signUntil.Before(now.UTC().Truncate(day).Add(sasLifetime)))
}
