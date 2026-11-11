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

package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/reqctx"
)

// limiterIdle is how long an unused bucket is kept before it is dropped.
const limiterIdle = 10 * time.Minute

// Limiter keeps one token bucket per (limit name, user id).
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	now     func() time.Time
	sweptAt time.Time
}

type bucket struct {
	lim  *rate.Limiter
	used time.Time
}

// NewLimiter returns an empty Limiter.
func NewLimiter() *Limiter {
	return &Limiter{buckets: map[string]*bucket{}, now: time.Now}
}

// Limit allows perMinute requests a minute for each user, with a burst of
// perMinute. Over the limit → 429 rate_limited. A request without a user is
// passed on; the access guard has already refused it.
func (l *Limiter) Limit(name string, perMinute int) gin.HandlerFunc {
	every := rate.Every(time.Minute / time.Duration(perMinute))
	return func(c *gin.Context) {
		u := reqctx.UserFrom(c.Request.Context())
		if u == nil {
			c.Next()
			return
		}
		if !l.allow(name+"\x00"+u.UserID, every, perMinute) {
			abortError(c, http.StatusTooManyRequests, "Too many requests. Please wait a moment and try again.", "rate_limited")
			return
		}
		c.Next()
	}
}

func (l *Limiter) allow(key string, every rate.Limit, burst int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if now.Sub(l.sweptAt) > limiterIdle {
		for k, b := range l.buckets {
			if now.Sub(b.used) > limiterIdle {
				delete(l.buckets, k)
			}
		}
		l.sweptAt = now
	}
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{lim: rate.NewLimiter(every, burst)}
		l.buckets[key] = b
	}
	b.used = now
	return b.lim.AllowN(now, 1)
}
