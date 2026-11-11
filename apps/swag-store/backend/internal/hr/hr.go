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

// Package hr reads the HR Entity GraphQL service. The store asks for two
// things only: the employee status (who may buy) and the location (default
// pickup office). Results are cached per email.
package hr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
	"golang.org/x/sync/singleflight"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/apierror"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/reqctx"
)

var tokenFetchTimeout = 10 * time.Second

// Config holds the GraphQL endpoint and its OAuth2 app.
type Config struct {
	BaseURL      string // the GraphQL endpoint itself
	TokenURL     string
	ClientID     string
	ClientSecret string
	Scopes       []string
	Timeout      time.Duration
}

// Employee is the part of the HR record the store uses.
type Employee struct {
	WorkEmail      string `json:"workEmail"`
	FirstName      string `json:"firstName"`
	LastName       string `json:"lastName"`
	EmployeeStatus string `json:"employeeStatus"`
	Location       string `json:"location"`
}

// CanBuy reports whether the HR status allows placing an order.
func CanBuy(e *Employee) bool {
	if e == nil {
		return false
	}
	s := strings.TrimSpace(e.EmployeeStatus)
	return strings.EqualFold(s, "Active") || strings.EqualFold(s, "Marked leaver")
}

// Client calls the HR Entity GraphQL service.
type Client struct {
	http    *http.Client
	baseURL string
}

// NewClient builds the client. It never contacts anything when built.
func NewClient(cfg Config) *Client {
	cc := clientcredentials.Config{ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, TokenURL: cfg.TokenURL, Scopes: cfg.Scopes}
	tokenCtx := context.WithValue(context.Background(), oauth2.HTTPClient, &http.Client{Timeout: tokenFetchTimeout})
	hc := cc.Client(tokenCtx)
	hc.Timeout = cfg.Timeout
	if hc.Timeout == 0 {
		hc.Timeout = 25 * time.Second
	}
	return &Client{http: hc, baseURL: cfg.BaseURL}
}

const employeeQuery = `query employee($email: String!) {
  employee(email: $email) { workEmail firstName lastName employeeStatus location } }`

// GetEmployee returns the employee with this work email, or (nil, nil) when
// none matches.
func (c *Client) GetEmployee(ctx context.Context, email string) (*Employee, error) {
	data, err := doGraphQL[struct {
		Employee *Employee `json:"employee"`
	}](ctx, c.http, c.baseURL, employeeQuery, map[string]any{"email": email}, "hr: employee")
	if err != nil {
		return nil, err
	}
	return data.Employee, nil
}

// Health sends a trivial query. It is used by /health/dependencies only.
func (c *Client) Health(ctx context.Context) error {
	_, err := doGraphQL[map[string]any](ctx, c.http, c.baseURL, `query { __typename }`, nil, "hr: health")
	return err
}

type graphqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

type graphqlEnvelope[T any] struct {
	Data   T `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// doGraphQL posts one query. A non-2xx, or a 200 that carries errors[],
// becomes an *apierror.Error (502 for errors[]).
func doGraphQL[T any](ctx context.Context, hc *http.Client, endpoint, query string, vars map[string]any, label string) (T, error) {
	var zero T
	body, err := json.Marshal(graphqlRequest{Query: query, Variables: vars})
	if err != nil {
		return zero, fmt.Errorf("%s: encode request: %w", label, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return zero, fmt.Errorf("%s: build request: %w", label, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if id := reqctx.CorrelationID(ctx); id != "" {
		req.Header.Set("X-Correlation-ID", id)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return zero, fmt.Errorf("%s: %w", label, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return zero, fmt.Errorf("%s: read response: %w", label, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return zero, apierror.New(resp.StatusCode, raw)
	}
	var env graphqlEnvelope[T]
	if err := json.Unmarshal(raw, &env); err != nil {
		return zero, fmt.Errorf("%s: decode response: %w", label, err)
	}
	if len(env.Errors) > 0 {
		return zero, &apierror.Error{StatusCode: http.StatusBadGateway, Body: env.Errors[0].Message}
	}
	return env.Data, nil
}

// Fetcher is what Cache wraps (the Client, or a fake in tests).
type Fetcher interface {
	GetEmployee(ctx context.Context, email string) (*Employee, error)
}

// Cache keeps HR answers for ttl, keyed by lower-cased email. Concurrent
// misses for one email share one upstream call. Errors are not cached.
type Cache struct {
	fetch Fetcher
	ttl   time.Duration
	now   func() time.Time
	sf    singleflight.Group

	mu      sync.Mutex
	entries map[string]cacheEntry
}

type cacheEntry struct {
	emp     *Employee
	expires time.Time
}

// NewCache wraps f with a ttl cache.
func NewCache(f Fetcher, ttl time.Duration) *Cache {
	return &Cache{fetch: f, ttl: ttl, now: time.Now, entries: map[string]cacheEntry{}}
}

// Lookup returns the employee for email, or (nil, nil) when HR has none.
func (c *Cache) Lookup(ctx context.Context, email string) (*Employee, error) {
	key := strings.ToLower(strings.TrimSpace(email))
	c.mu.Lock()
	if e, ok := c.entries[key]; ok && c.now().Before(e.expires) {
		c.mu.Unlock()
		return e.emp, nil
	}
	c.mu.Unlock()

	v, err, _ := c.sf.Do(key, func() (any, error) {
		emp, err := c.fetch.GetEmployee(ctx, key)
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		c.entries[key] = cacheEntry{emp: emp, expires: c.now().Add(c.ttl)}
		if len(c.entries) > 10000 {
			for k, e := range c.entries {
				if !c.now().Before(e.expires) {
					delete(c.entries, k)
				}
			}
		}
		c.mu.Unlock()
		return emp, nil
	})
	if err != nil {
		return nil, err
	}
	emp, _ := v.(*Employee)
	return emp, nil
}
