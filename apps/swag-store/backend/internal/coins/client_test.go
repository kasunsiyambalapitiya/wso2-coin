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

package coins

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/apierror"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/reqctx"
)

type recorded struct {
	method, path, auth, user, correlation string
	body                                  string
}

type fakeTS struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []recorded
	// handle answers non-token requests.
	handle func(w http.ResponseWriter, r *http.Request, body string)
}

func newFakeTS(t *testing.T, handle func(w http.ResponseWriter, r *http.Request, body string)) *fakeTS {
	t.Helper()
	f := &fakeTS{handle: handle}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"svc-token","token_type":"Bearer","expires_in":3600}`)
			return
		}
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.reqs = append(f.reqs, recorded{r.Method, r.URL.EscapedPath(), r.Header.Get("Authorization"),
			r.Header.Get("X-User-Assertion"), r.Header.Get("X-Correlation-ID"), string(b)})
		f.mu.Unlock()
		f.handle(w, r, string(b))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTS) client() *Client {
	return NewClient(Config{BaseURL: f.srv.URL + "/", TokenURL: f.srv.URL + "/token", ClientID: "c", ClientSecret: "s", Timeout: 5 * time.Second})
}

func (f *fakeTS) last() recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reqs[len(f.reqs)-1]
}

func userCtx() context.Context {
	ctx := reqctx.WithUserAssertion(context.Background(), "user-jwt")
	return reqctx.WithCorrelationID(ctx, "cid-1")
}

func TestWalletsForwardsAssertionAndSortsDefaultFirst(t *testing.T) {
	f := newFakeTS(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		_, _ = io.WriteString(w, `[{"walletAddress":"0xb","balance":"12.5","defaultWallet":false},{"walletAddress":"0xa","balance":"202.50","defaultWallet":true}]`)
	})
	ws, err := f.client().Wallets(userCtx())
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 2 || ws[0].Address != "0xa" || !ws[0].Default || ws[0].BalanceText != "202.50" {
		t.Fatalf("wallets = %+v", ws)
	}
	if ws[0].Balance.Cmp(mustRat("202.5")) != 0 {
		t.Fatalf("balance = %v", ws[0].Balance)
	}
	got := f.last()
	if got.method != http.MethodGet || got.path != "/wallets/me" || got.auth != "Bearer svc-token" ||
		got.user != "user-jwt" || got.correlation != "cid-1" {
		t.Fatalf("request = %+v", got)
	}
}

func TestUserCallsNeedAnAssertion(t *testing.T) {
	f := newFakeTS(t, func(w http.ResponseWriter, r *http.Request, _ string) { t.Error("upstream called") })
	if _, err := f.client().Wallets(context.Background()); err == nil {
		t.Fatal("no error without an assertion")
	}
	if _, err := f.client().Pay(context.Background(), PayRequest{}); err == nil {
		t.Fatal("no error without an assertion")
	}
}

func TestPayCreatedAndReplay(t *testing.T) {
	status := http.StatusCreated
	f := newFakeTS(t, func(w http.ResponseWriter, r *http.Request, body string) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"reference":"swag:ord:1","fromAddress":"0xu","toAddress":"0xt","amount":"180"}`)
	})
	req := PayRequest{FromAddress: "0xu", ToAddress: "0xt", Amount: "180", Reference: "swag:ord:1", Source: "SWAG_STORE"}
	p, err := f.client().Pay(userCtx(), req)
	if err != nil {
		t.Fatal(err)
	}
	if p.Replayed || p.ToAddress != "0xt" || p.Amount != "180" || p.Reference != "swag:ord:1" {
		t.Fatalf("payment = %+v", p)
	}
	var sent map[string]string
	if err := json.Unmarshal([]byte(f.last().body), &sent); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"fromAddress": "0xu", "toAddress": "0xt", "amount": "180", "reference": "swag:ord:1", "source": "SWAG_STORE"}
	for k, v := range want {
		if sent[k] != v {
			t.Fatalf("body %s = %q, want %q (%s)", k, sent[k], v, f.last().body)
		}
	}
	if len(sent) != len(want) || f.last().user != "user-jwt" || f.last().path != "/payments" {
		t.Fatalf("request = %+v", f.last())
	}

	status = http.StatusOK
	p, err = f.client().Pay(userCtx(), req)
	if err != nil || !p.Replayed {
		t.Fatalf("replay = %+v, %v", p, err)
	}
}

func TestPayErrorsKeepStatus(t *testing.T) {
	f := newFakeTS(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"message":"Forbidden."}`)
	})
	_, err := f.client().Pay(userCtx(), PayRequest{})
	if apierror.Status(err) != http.StatusForbidden {
		t.Fatalf("err = %v", err)
	}
}

func TestInsufficientFundsMatcher(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{&apierror.Error{StatusCode: 400, Body: `{"message":"Insufficient funds."}`}, true},
		{&apierror.Error{StatusCode: 400, Body: `{"message":"insufficient FUNDS"}`}, true},
		{&apierror.Error{StatusCode: 400, Body: `{"message":"Invalid amount."}`}, false},
		{&apierror.Error{StatusCode: 409, Body: `{"message":"Insufficient funds."}`}, false},
		{&apierror.Error{StatusCode: 400, Body: `not json insufficient funds`}, false},
		{errors.New("insufficient funds"), false},
		{nil, false},
	}
	for _, tc := range cases {
		if got := IsInsufficientFunds(tc.err); got != tc.want {
			t.Errorf("IsInsufficientFunds(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestTransactionEscapesAndHandles404(t *testing.T) {
	f := newFakeTS(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		if strings.Contains(r.URL.EscapedPath(), "missing") {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"Not found."}`)
			return
		}
		_, _ = io.WriteString(w, `{"reference":"swag:ord:a/b","fromAddress":"0xu","toAddress":"0xt","amount":"90","timestamp":"2026-11-11T05:00:00Z"}`)
	})
	tx, err := f.client().Transaction(context.Background(), "swag:ord:a/b")
	if err != nil || tx == nil || tx.Amount != "90" {
		t.Fatalf("tx = %+v, %v", tx, err)
	}
	if got := f.last().path; got != "/transactions/swag:ord:a%2Fb" {
		t.Fatalf("path = %s", got)
	}
	if f.last().user != "" {
		t.Fatal("service call forwarded a user assertion")
	}
	tx, err = f.client().Transaction(context.Background(), "missing")
	if err != nil || tx != nil {
		t.Fatalf("404 = %+v, %v", tx, err)
	}
}

func TestSearchAndTreasury(t *testing.T) {
	f := newFakeTS(t, func(w http.ResponseWriter, r *http.Request, body string) {
		switch r.URL.Path {
		case "/transactions/search":
			_, _ = io.WriteString(w, `{"transactions":[{"reference":"swag:ord:1","toAddress":"0xt","amount":"40"}],"total":1,"limit":100,"offset":0}`)
		case "/wallets/master/balance":
			_, _ = io.WriteString(w, `{"walletAddress":"0xt","balance":"1000"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	c := f.client()
	page, err := c.Search(context.Background(), SearchFilter{ToAddresses: []string{"0xt"}, DateFrom: "2026-11-10T00:00:00Z", Limit: 100})
	if err != nil || page.Total != 1 || page.Transactions[0].Reference != "swag:ord:1" {
		t.Fatalf("page = %+v, %v", page, err)
	}
	var sent map[string]any
	_ = json.Unmarshal([]byte(f.last().body), &sent)
	if sent["toAddresses"].([]any)[0] != "0xt" || sent["dateFrom"] != "2026-11-10T00:00:00Z" {
		t.Fatalf("search body = %s", f.last().body)
	}
	addr, err := c.Treasury(context.Background())
	if err != nil || addr != "0xt" {
		t.Fatalf("treasury = %q, %v", addr, err)
	}
}

func TestPayHasItsOwnDeadline(t *testing.T) {
	old := PayTimeout
	PayTimeout = 50 * time.Millisecond
	defer func() { PayTimeout = old }()
	f := newFakeTS(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	})
	start := time.Now()
	_, err := f.client().Pay(userCtx(), PayRequest{})
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("pay did not time out quickly: %v after %v", err, time.Since(start))
	}
	if apierror.Status(err) != 0 {
		t.Fatalf("a timeout must not look like an upstream status: %v", err)
	}
}
