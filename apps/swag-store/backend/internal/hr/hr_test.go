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

package hr

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/apierror"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/reqctx"
)

func fakeHR(t *testing.T, answer func(email string) string) (*Client, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"hr-token","token_type":"Bearer","expires_in":3600}`)
			return
		}
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer hr-token" || r.Header.Get("X-Correlation-ID") != "cid" {
			t.Errorf("headers = %v", r.Header)
		}
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		for _, f := range []string{"employeeStatus", "location", "workEmail"} {
			if !strings.Contains(req.Query, f) {
				t.Errorf("query lacks %s: %s", f, req.Query)
			}
		}
		_, _ = io.WriteString(w, answer(req.Variables["email"].(string)))
	}))
	t.Cleanup(srv.Close)
	return NewClient(Config{BaseURL: srv.URL + "/graphql", TokenURL: srv.URL + "/token", ClientID: "c", ClientSecret: "s"}), &calls
}

func ctx() context.Context { return reqctx.WithCorrelationID(context.Background(), "cid") }

func TestGetEmployee(t *testing.T) {
	c, _ := fakeHR(t, func(email string) string {
		switch email {
		case "you@wso2.com":
			return `{"data":{"employee":{"workEmail":"you@wso2.com","firstName":"Your","lastName":"Name","employeeStatus":"Active","location":"Colombo"}}}`
		case "gone@wso2.com":
			return `{"data":{"employee":null}}`
		default:
			return `{"data":null,"errors":[{"message":"internal detail"}]}`
		}
	})
	e, err := c.GetEmployee(ctx(), "you@wso2.com")
	if err != nil || e == nil || e.EmployeeStatus != "Active" || e.Location != "Colombo" || e.FirstName != "Your" {
		t.Fatalf("employee = %+v, %v", e, err)
	}
	e, err = c.GetEmployee(ctx(), "gone@wso2.com")
	if err != nil || e != nil {
		t.Fatalf("no match = %+v, %v", e, err)
	}
	_, err = c.GetEmployee(ctx(), "boom@wso2.com")
	if apierror.Status(err) != http.StatusBadGateway {
		t.Fatalf("errors[] = %v, want a 502 apierror", err)
	}
}

type countingFetcher struct {
	mu    sync.Mutex
	calls int
	delay time.Duration
	err   error
	emp   *Employee
}

func (f *countingFetcher) GetEmployee(_ context.Context, email string) (*Employee, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	time.Sleep(f.delay)
	return f.emp, f.err
}

func TestCacheTTLAndSingleflight(t *testing.T) {
	f := &countingFetcher{delay: 20 * time.Millisecond, emp: &Employee{EmployeeStatus: "Active"}}
	now := time.Unix(0, 0)
	cache := NewCache(f, time.Hour)
	cache.now = func() time.Time { return now }

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e, err := cache.Lookup(context.Background(), "You@wso2.com"); err != nil || e.EmployeeStatus != "Active" {
				t.Errorf("lookup = %+v, %v", e, err)
			}
		}()
	}
	wg.Wait()
	if f.calls != 1 {
		t.Fatalf("concurrent lookups made %d calls", f.calls)
	}
	_, _ = cache.Lookup(context.Background(), "you@wso2.com")
	if f.calls != 1 {
		t.Fatal("cached value not used")
	}
	now = now.Add(time.Hour + time.Second)
	_, _ = cache.Lookup(context.Background(), "you@wso2.com")
	if f.calls != 2 {
		t.Fatalf("expired value reused: %d calls", f.calls)
	}
}

func TestCacheDoesNotKeepErrors(t *testing.T) {
	f := &countingFetcher{err: errors.New("down")}
	cache := NewCache(f, time.Hour)
	for i := 0; i < 2; i++ {
		if _, err := cache.Lookup(context.Background(), "you@wso2.com"); err == nil {
			t.Fatal("error swallowed")
		}
	}
	if f.calls != 2 {
		t.Fatalf("error was cached: %d calls", f.calls)
	}
}

func TestCanBuy(t *testing.T) {
	cases := map[string]bool{"Active": true, "Marked leaver": true, "active": true, "Left": false, "": false, "Inactive": false}
	for status, want := range cases {
		if got := CanBuy(&Employee{EmployeeStatus: status}); got != want {
			t.Errorf("CanBuy(%q) = %v", status, got)
		}
	}
	if CanBuy(nil) {
		t.Error("nil employee may buy")
	}
}
