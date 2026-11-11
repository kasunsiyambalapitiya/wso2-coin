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
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// startingBalances are the balances of every new user's wallets, default first.
var startingBalances = []string{"500", "25"}

var (
	amountRE    = regexp.MustCompile(`^[0-9]{1,30}(\.[0-9]{1,18})?$`)
	referenceRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,65}$`)
	serviceRef  = regexp.MustCompile(`^0x[0-9a-fA-F]{64}$`)
	sourceRE    = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,31}$`)
)

// ledger is the transaction-service fake. Every field is guarded by mu.
type ledger struct {
	mu       sync.Mutex
	treasury string
	balances map[string]*big.Rat // address → balance, users' wallets and the treasury
	owned    map[string][]string // email → wallet addresses, default first
	txs      []*transaction      // oldest first
	byRef    map[string]*transaction
}

type transaction struct {
	reference, fromAddress, toAddress, source string
	amount                                    *big.Rat
	at                                        time.Time
}

type paymentRequest struct {
	FromAddress string `json:"fromAddress"`
	ToAddress   string `json:"toAddress"`
	Amount      string `json:"amount"`
	Reference   string `json:"reference"`
	Source      string `json:"source"`
}

type paymentResponse struct {
	Reference   string `json:"reference"`
	FromAddress string `json:"fromAddress"`
	ToAddress   string `json:"toAddress"`
	Amount      string `json:"amount"`
}

type transactionJSON struct {
	Reference   string `json:"reference"`
	FromAddress string `json:"fromAddress"`
	ToAddress   string `json:"toAddress"`
	Amount      string `json:"amount"`
	Timestamp   string `json:"timestamp"`
}

type searchRequest struct {
	FromAddresses []string `json:"fromAddresses"`
	ToAddresses   []string `json:"toAddresses"`
	Reference     string   `json:"reference"`
	DateFrom      string   `json:"dateFrom"`
	DateTo        string   `json:"dateTo"`
	Limit         int      `json:"limit"`
	Offset        int      `json:"offset"`
}

func newLedger(treasury string) *ledger {
	return &ledger{
		treasury: treasury,
		balances: map[string]*big.Rat{treasury: new(big.Rat)},
		owned:    map[string][]string{},
		byRef:    map[string]*transaction{},
	}
}

func (l *ledger) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /wallets/me", l.handleWalletsMe)
	mux.HandleFunc("POST /payments", l.handlePayment)
	mux.HandleFunc("GET /transactions/{reference}", l.handleTransaction)
	mux.HandleFunc("POST /transactions/search", l.handleSearch)
	mux.HandleFunc("GET /wallets/master/balance", l.handleMasterBalance)
}

func (l *ledger) handleWalletsMe(w http.ResponseWriter, r *http.Request) {
	email, ok := assertionEmail(r)
	if !ok {
		writeMessage(w, http.StatusUnauthorized, "Unauthorized.")
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	type wallet struct {
		WalletAddress string `json:"walletAddress"`
		Balance       string `json:"balance"`
		DefaultWallet bool   `json:"defaultWallet"`
	}
	var out []wallet
	for i, addr := range l.walletsOf(email) {
		out = append(out, wallet{addr, formatAmount(l.balances[addr]), i == 0})
	}
	writeJSON(w, http.StatusOK, out)
}

func (l *ledger) handlePayment(w http.ResponseWriter, r *http.Request) {
	email, ok := assertionEmail(r)
	if !ok {
		writeMessage(w, http.StatusUnauthorized, "Unauthorized.")
		return
	}
	var req paymentRequest
	if err := readJSON(r, 1<<20, &req); err != nil {
		writeMessage(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	amount, msg := validatePayment(req)
	if msg != "" {
		writeMessage(w, http.StatusBadRequest, msg)
		return
	}
	from, to := req.FromAddress, req.ToAddress

	l.mu.Lock()
	defer l.mu.Unlock()
	if !slices.Contains(l.walletsOf(email), from) {
		writeMessage(w, http.StatusForbidden, "Forbidden.")
		return
	}
	if prev, ok := l.byRef[req.Reference]; ok {
		if prev.fromAddress != from || prev.toAddress != to || prev.amount.Cmp(amount) != 0 || prev.source != req.Source {
			writeMessage(w, http.StatusConflict, "Reference already used with different details.")
			return
		}
		writeJSON(w, http.StatusOK, prev.payment())
		return
	}
	if l.balances[from].Cmp(amount) < 0 {
		writeMessage(w, http.StatusBadRequest, "Insufficient funds.")
		return
	}
	l.balances[from].Sub(l.balances[from], amount)
	if l.balances[to] == nil {
		l.balances[to] = new(big.Rat)
	}
	l.balances[to].Add(l.balances[to], amount)
	tx := &transaction{reference: req.Reference, fromAddress: from, toAddress: to, source: req.Source,
		amount: amount, at: time.Now().UTC().Truncate(time.Second)}
	l.txs = append(l.txs, tx)
	l.byRef[tx.reference] = tx
	w.Header().Set("Location", "/transactions/"+tx.reference)
	writeJSON(w, http.StatusCreated, tx.payment())
}

// validatePayment returns the amount, or the 400 message.
func validatePayment(req paymentRequest) (*big.Rat, string) {
	if req.FromAddress == "" || req.ToAddress == "" {
		return nil, "fromAddress and toAddress are required."
	}
	if !referenceRE.MatchString(req.Reference) || serviceRef.MatchString(req.Reference) {
		return nil, "Invalid reference."
	}
	if !sourceRE.MatchString(req.Source) {
		return nil, "Invalid source."
	}
	if !amountRE.MatchString(req.Amount) {
		return nil, "Invalid amount."
	}
	amount, ok := new(big.Rat).SetString(req.Amount)
	if !ok || amount.Sign() <= 0 {
		return nil, "Invalid amount."
	}
	return amount, ""
}

func (l *ledger) handleTransaction(w http.ResponseWriter, r *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()
	tx, ok := l.byRef[r.PathValue("reference")]
	if !ok {
		writeMessage(w, http.StatusNotFound, "Not found.")
		return
	}
	writeJSON(w, http.StatusOK, tx.json())
}

func (l *ledger) handleSearch(w http.ResponseWriter, r *http.Request) {
	var f searchRequest
	if err := readJSON(r, 1<<20, &f); err != nil {
		writeMessage(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	var from, to time.Time
	var err error
	if f.DateFrom != "" {
		if from, err = time.Parse(time.RFC3339, f.DateFrom); err != nil {
			writeMessage(w, http.StatusBadRequest, "Invalid dateFrom.")
			return
		}
	}
	if f.DateTo != "" {
		if to, err = time.Parse(time.RFC3339, f.DateTo); err != nil {
			writeMessage(w, http.StatusBadRequest, "Invalid dateTo.")
			return
		}
	}
	if f.Limit <= 0 {
		f.Limit = 15
	}
	f.Limit = min(f.Limit, 100)
	f.Offset = max(f.Offset, 0)
	matches := func(list []string, addr string) bool {
		return len(list) == 0 || slices.ContainsFunc(list, func(a string) bool { return strings.EqualFold(a, addr) })
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	hits := []transactionJSON{}
	total := 0
	for _, tx := range l.txs {
		if !matches(f.FromAddresses, tx.fromAddress) || !matches(f.ToAddresses, tx.toAddress) ||
			(f.Reference != "" && f.Reference != tx.reference) ||
			(!from.IsZero() && tx.at.Before(from)) || (!to.IsZero() && tx.at.After(to)) {
			continue
		}
		if total >= f.Offset && len(hits) < f.Limit {
			hits = append(hits, tx.json())
		}
		total++
	}
	writeJSON(w, http.StatusOK, map[string]any{"transactions": hits, "total": total, "limit": f.Limit, "offset": f.Offset})
}

func (l *ledger) handleMasterBalance(w http.ResponseWriter, _ *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"walletAddress": l.treasury, "balance": formatAmount(l.balances[l.treasury])})
}

// walletsOf returns the user's wallets, default first, creating them on first
// sight. The caller holds mu.
func (l *ledger) walletsOf(email string) []string {
	if addrs, ok := l.owned[email]; ok {
		return addrs
	}
	var addrs []string
	for i, bal := range startingBalances {
		addr := walletAddress(email, i)
		b, _ := new(big.Rat).SetString(bal)
		l.balances[addr] = b
		addrs = append(addrs, addr)
	}
	l.owned[email] = addrs
	return addrs
}

// walletAddress derives a stable 0x + 40 hex address from the email.
func walletAddress(email string, i int) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "swag-store-fake:%s:%d", email, i))
	return "0x" + hex.EncodeToString(sum[:20])
}

// assertionEmail reads the email claim of X-User-Assertion WITHOUT verifying
// the token. Fine for a local fake; never do this anywhere else.
func assertionEmail(r *http.Request) (string, bool) {
	parts := strings.Split(r.Header.Get("X-User-Assertion"), ".")
	if len(parts) != 3 {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return "", false
	}
	var claims struct {
		Email string `json:"email"`
	}
	if json.Unmarshal(raw, &claims) != nil || claims.Email == "" {
		return "", false
	}
	return strings.ToLower(claims.Email), true
}

// formatAmount writes r as a plain decimal ("500", "0.5"), never a float.
func formatAmount(r *big.Rat) string {
	if r == nil {
		return "0"
	}
	if r.IsInt() {
		return r.Num().String()
	}
	s := strings.TrimRight(r.FloatString(18), "0")
	return strings.TrimSuffix(s, ".")
}

func (tx *transaction) payment() paymentResponse {
	return paymentResponse{tx.reference, tx.fromAddress, tx.toAddress, formatAmount(tx.amount)}
}

func (tx *transaction) json() transactionJSON {
	return transactionJSON{tx.reference, tx.fromAddress, tx.toAddress, formatAmount(tx.amount), tx.at.Format(time.RFC3339)}
}
