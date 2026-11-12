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
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"
)

type walletJSON struct {
	WalletAddress string `json:"walletAddress"`
	Balance       string `json:"balance"`
	DefaultWallet bool   `json:"defaultWallet"`
}

type paymentJSON struct {
	Reference   string `json:"reference"`
	FromAddress string `json:"fromAddress"`
	ToAddress   string `json:"toAddress"`
	Amount      string `json:"amount"`
}

type txJSON struct {
	Reference   string `json:"reference"`
	FromAddress string `json:"fromAddress"`
	ToAddress   string `json:"toAddress"`
	Amount      string `json:"amount"`
	Timestamp   string `json:"timestamp"`
}

type pageJSON struct {
	Transactions []txJSON `json:"transactions"`
	Total        int      `json:"total"`
	Limit        int      `json:"limit"`
	Offset       int      `json:"offset"`
}

func wallets(t *testing.T, srv *httptest.Server, email string) []walletJSON {
	t.Helper()
	status, body := call(t, srv, http.MethodGet, "/wallets/me", nil, email)
	if status != http.StatusOK {
		t.Fatalf("GET /wallets/me = %d %s", status, body)
	}
	return decode[[]walletJSON](t, body)
}

func pay(from, to, amount, ref string) map[string]string {
	return map[string]string{"fromAddress": from, "toAddress": to, "amount": amount, "reference": ref, "source": "SWAG_STORE"}
}

var addressRE = regexp.MustCompile(`^0x[0-9a-f]{40}$`)

func TestWalletsMe(t *testing.T) {
	srv := newTestServer(t)
	if status, _ := call(t, srv, http.MethodGet, "/wallets/me", nil, ""); status != http.StatusUnauthorized {
		t.Errorf("no assertion: status %d, want 401", status)
	}
	ws := wallets(t, srv, "Nimal@wso2.com")
	if len(ws) != 2 || !ws[0].DefaultWallet || ws[1].DefaultWallet || ws[0].Balance != "500" || ws[1].Balance != "25" {
		t.Fatalf("wallets = %+v, want default 500 then 25", ws)
	}
	for _, w := range ws {
		if !addressRE.MatchString(w.WalletAddress) {
			t.Errorf("address %q is not 0x + 40 hex", w.WalletAddress)
		}
	}
	if ws[0].WalletAddress == ws[1].WalletAddress {
		t.Error("the two wallets share an address")
	}
	again := wallets(t, srv, "nimal@wso2.com")
	if again[0].WalletAddress != ws[0].WalletAddress {
		t.Error("addresses must be stable for an email")
	}
	if other := wallets(t, srv, "kamal@wso2.com"); other[0].WalletAddress == ws[0].WalletAddress {
		t.Error("two emails share an address")
	}
	// Another server derives the same address from the same email.
	if w := wallets(t, newTestServer(t), "nimal@wso2.com"); w[0].WalletAddress != ws[0].WalletAddress {
		t.Error("address is not derived from the email")
	}
}

func TestPaymentsIdempotent(t *testing.T) {
	srv := newTestServer(t)
	from := wallets(t, srv, "nimal@wso2.com")[0].WalletAddress
	req := pay(from, testTreasury, "180", "swag:ord:SS-1")

	status, body := call(t, srv, http.MethodPost, "/payments", req, "nimal@wso2.com")
	if status != http.StatusCreated {
		t.Fatalf("first = %d %s, want 201", status, body)
	}
	first := decode[paymentJSON](t, body)
	if first != (paymentJSON{"swag:ord:SS-1", from, testTreasury, "180"}) {
		t.Errorf("payment = %+v", first)
	}

	status, body = call(t, srv, http.MethodPost, "/payments", req, "nimal@wso2.com")
	if status != http.StatusOK || decode[paymentJSON](t, body) != first {
		t.Fatalf("replay = %d %s, want 200 with the original", status, body)
	}

	status, body = call(t, srv, http.MethodPost, "/payments", pay(from, testTreasury, "181", "swag:ord:SS-1"), "nimal@wso2.com")
	if status != http.StatusConflict {
		t.Fatalf("different details = %d %s, want 409", status, body)
	}

	if got := wallets(t, srv, "nimal@wso2.com")[0].Balance; got != "320" {
		t.Errorf("balance after one payment = %s, want 320", got)
	}
	status, body = call(t, srv, http.MethodGet, "/wallets/master/balance", nil, "")
	if b := decode[walletJSON](t, body); status != http.StatusOK || b.Balance != "180" {
		t.Errorf("treasury = %d %s, want 180", status, body)
	}
}

func TestPaymentsInsufficientFunds(t *testing.T) {
	srv := newTestServer(t)
	from := wallets(t, srv, "nimal@wso2.com")[1].WalletAddress // 25
	status, body := call(t, srv, http.MethodPost, "/payments", pay(from, testTreasury, "25.5", "swag:ord:SS-2"), "nimal@wso2.com")
	if status != http.StatusBadRequest || message(t, body) != "Insufficient funds." {
		t.Fatalf("= %d %s, want 400 Insufficient funds.", status, body)
	}
	status, _ = call(t, srv, http.MethodPost, "/payments", pay(from, testTreasury, "24.5", "swag:ord:SS-3"), "nimal@wso2.com")
	if status != http.StatusCreated {
		t.Fatalf("fractional payment = %d, want 201", status)
	}
	if got := wallets(t, srv, "nimal@wso2.com")[1].Balance; got != "0.5" {
		t.Errorf("balance = %s, want 0.5", got)
	}
}

func TestPaymentsForbiddenOnSomeoneElsesWallet(t *testing.T) {
	srv := newTestServer(t)
	theirs := wallets(t, srv, "kamal@wso2.com")[0].WalletAddress
	status, body := call(t, srv, http.MethodPost, "/payments", pay(theirs, testTreasury, "1", "swag:ord:SS-4"), "nimal@wso2.com")
	if status != http.StatusForbidden || message(t, body) != "Forbidden." {
		t.Fatalf("= %d %s, want 403 Forbidden.", status, body)
	}
	if got := wallets(t, srv, "kamal@wso2.com")[0].Balance; got != "500" {
		t.Errorf("victim balance = %s, want 500", got)
	}
}

func TestPaymentsRejects(t *testing.T) {
	srv := newTestServer(t)
	from := wallets(t, srv, "nimal@wso2.com")[0].WalletAddress
	if status, _ := call(t, srv, http.MethodPost, "/payments", pay(from, testTreasury, "1", "swag:ord:SS-5"), ""); status != http.StatusUnauthorized {
		t.Errorf("no assertion: %d, want 401", status)
	}
	for name, req := range map[string]map[string]string{
		"zero amount":     pay(from, testTreasury, "0", "r1"),
		"negative amount": pay(from, testTreasury, "-1", "r2"),
		"fraction amount": pay(from, testTreasury, "1/3", "r3"),
		"bad reference":   pay(from, testTreasury, "1", "-bad"),
		"no to address":   pay(from, "", "1", "r4"),
	} {
		if status, body := call(t, srv, http.MethodPost, "/payments", req, "nimal@wso2.com"); status != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", name, status, body)
		}
	}
}

func TestMasterBalanceAddress(t *testing.T) {
	srv := newTestServer(t)
	status, body := call(t, srv, http.MethodGet, "/wallets/master/balance", nil, "")
	b := decode[walletJSON](t, body)
	if status != http.StatusOK || b.WalletAddress != testTreasury || b.Balance != "0" {
		t.Errorf("= %d %s", status, body)
	}
	custom := httptest.NewServer(newHandler("0x1111111111111111111111111111111111111111"))
	defer custom.Close()
	_, body = call(t, custom, http.MethodGet, "/wallets/master/balance", nil, "")
	if b := decode[walletJSON](t, body); b.WalletAddress != "0x1111111111111111111111111111111111111111" {
		t.Errorf("custom treasury = %s", body)
	}
}

func TestTransactionByReference(t *testing.T) {
	srv := newTestServer(t)
	status, body := call(t, srv, http.MethodGet, "/transactions/swag:ord:SS-9", nil, "")
	if status != http.StatusNotFound || message(t, body) != "Not found." {
		t.Fatalf("unknown = %d %s, want 404 Not found.", status, body)
	}
	from := wallets(t, srv, "nimal@wso2.com")[0].WalletAddress
	call(t, srv, http.MethodPost, "/payments", pay(from, testTreasury, "60", "swag:ord:SS-9"), "nimal@wso2.com")
	status, body = call(t, srv, http.MethodGet, "/transactions/swag:ord:SS-9", nil, "")
	tx := decode[txJSON](t, body)
	if status != http.StatusOK || tx.Reference != "swag:ord:SS-9" || tx.FromAddress != from || tx.ToAddress != testTreasury || tx.Amount != "60" {
		t.Fatalf("= %d %s", status, body)
	}
	if _, err := time.Parse(time.RFC3339, tx.Timestamp); err != nil {
		t.Errorf("timestamp %q is not RFC3339", tx.Timestamp)
	}
}

func TestSearchByToAddresses(t *testing.T) {
	srv := newTestServer(t)
	nimal := wallets(t, srv, "nimal@wso2.com")
	kamal := wallets(t, srv, "kamal@wso2.com")
	other := "0x2222222222222222222222222222222222222222"
	for _, p := range []struct {
		email string
		req   map[string]string
	}{
		{"nimal@wso2.com", pay(nimal[0].WalletAddress, testTreasury, "10", "swag:ord:A")},
		{"kamal@wso2.com", pay(kamal[0].WalletAddress, testTreasury, "20", "swag:ord:B")},
		{"nimal@wso2.com", pay(nimal[1].WalletAddress, other, "5", "elsewhere:C")},
		{"kamal@wso2.com", pay(kamal[1].WalletAddress, testTreasury, "1", "swag:ord:D")},
	} {
		if status, body := call(t, srv, http.MethodPost, "/payments", p.req, p.email); status != http.StatusCreated {
			t.Fatalf("pay %s = %d %s", p.req["reference"], status, body)
		}
	}

	search := func(filter map[string]any) pageJSON {
		t.Helper()
		status, body := call(t, srv, http.MethodPost, "/transactions/search", filter, "")
		if status != http.StatusOK {
			t.Fatalf("search %v = %d %s", filter, status, body)
		}
		return decode[pageJSON](t, body)
	}
	refs := func(p pageJSON) []string {
		var out []string
		for _, tx := range p.Transactions {
			out = append(out, tx.Reference)
		}
		return out
	}

	p := search(map[string]any{"toAddresses": []string{testTreasury}})
	if got := refs(p); p.Total != 3 || p.Limit != 15 || p.Offset != 0 || len(got) != 3 || got[0] != "swag:ord:A" || got[2] != "swag:ord:D" {
		t.Errorf("toAddresses = %+v", p)
	}
	p = search(map[string]any{"toAddresses": []string{testTreasury}, "limit": 1, "offset": 1})
	if got := refs(p); p.Total != 3 || p.Limit != 1 || p.Offset != 1 || len(got) != 1 || got[0] != "swag:ord:B" {
		t.Errorf("paged = %+v", p)
	}
	if p := search(map[string]any{"limit": 500}); p.Limit != 100 || p.Total != 4 {
		t.Errorf("limit cap = %+v", p)
	}
	if got := refs(search(map[string]any{"fromAddresses": []string{nimal[1].WalletAddress}})); len(got) != 1 || got[0] != "elsewhere:C" {
		t.Errorf("fromAddresses = %v", got)
	}
	if got := refs(search(map[string]any{"reference": "swag:ord:B"})); len(got) != 1 || got[0] != "swag:ord:B" {
		t.Errorf("reference = %v", got)
	}
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if p := search(map[string]any{"dateFrom": future}); p.Total != 0 || p.Transactions == nil {
		t.Errorf("dateFrom in the future = %+v, want an empty list", p)
	}
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	if p := search(map[string]any{"dateFrom": past, "toAddresses": []string{testTreasury}}); p.Total != 3 {
		t.Errorf("dateFrom in the past = %+v", p)
	}
	if status, _ := call(t, srv, http.MethodPost, "/transactions/search", map[string]any{"dateFrom": "yesterday"}, ""); status != http.StatusBadRequest {
		t.Errorf("bad dateFrom = %d, want 400", status)
	}
}

func TestPaymentToAWalletBeforeItsOwnerArrivesIsKept(t *testing.T) {
	srv := newTestServer(t)
	from := wallets(t, srv, "alice@wso2.com")[0].WalletAddress
	bob := walletAddress("bob@wso2.com", 0)
	if status, body := call(t, srv, http.MethodPost, "/payments", pay(from, bob, "10", "swag:ord:SS-7"), "alice@wso2.com"); status != http.StatusCreated {
		t.Fatalf("pay bob = %d %s", status, body)
	}
	if ws := wallets(t, srv, "bob@wso2.com"); ws[0].WalletAddress != bob || ws[0].Balance != "510" {
		t.Fatalf("bob's wallets = %+v, want the starting 500 plus the 10 received", ws)
	}
}
