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

package handler

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// Dependency states reported by /health/dependencies.
const (
	StatusOK            = "ok"
	StatusError         = "error"
	StatusNotConfigured = "not_configured"
)

// probeTimeout bounds each probe; healthCacheTTL is how long a result is reused.
var (
	probeTimeout   = 5 * time.Second
	healthCacheTTL = 10 * time.Second
)

// Probe is one dependency check. Check returns a state string.
type Probe struct {
	Name  string
	Check func(ctx context.Context) string
}

// HealthHandler serves GET /health and GET /health/dependencies. Both skip auth.
type HealthHandler struct {
	probes []Probe
	now    func() time.Time

	mu       sync.Mutex
	cached   map[string]string
	cachedAt time.Time
}

// NewHealthHandler builds the handler. The probes never run on a request
// path other than /health/dependencies.
func NewHealthHandler(probes []Probe) *HealthHandler {
	return &HealthHandler{probes: probes, now: time.Now}
}

// Live answers an empty 200.
func (h *HealthHandler) Live(c *gin.Context) { c.Status(http.StatusOK) }

// Dependencies runs every probe at once (5 s each) and caches the result for
// 10 s. It answers 503 when any dependency is neither ok nor not_configured.
func (h *HealthHandler) Dependencies(c *gin.Context) {
	result := h.result(c.Request.Context())
	status := http.StatusOK
	for _, v := range result {
		if v != StatusOK && v != StatusNotConfigured {
			status = http.StatusServiceUnavailable
		}
	}
	c.Header("Cache-Control", "no-store")
	writeJSON(c, status, result)
}

func (h *HealthHandler) result(ctx context.Context) map[string]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cached != nil && h.now().Sub(h.cachedAt) < healthCacheTTL {
		return h.cached
	}
	out := make(map[string]string, len(h.probes))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, p := range h.probes {
		wg.Add(1)
		go func(p Probe) {
			defer wg.Done()
			pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), probeTimeout)
			defer cancel()
			state := p.Check(pctx)
			mu.Lock()
			out[p.Name] = state
			mu.Unlock()
		}(p)
	}
	wg.Wait()
	h.cached, h.cachedAt = out, h.now()
	return out
}

// ErrProbe wraps an error-returning health call as a probe.
func ErrProbe(name string, f func(context.Context) error) Probe {
	return Probe{Name: name, Check: func(ctx context.Context) string {
		if f(ctx) != nil {
			return StatusError
		}
		return StatusOK
	}}
}
