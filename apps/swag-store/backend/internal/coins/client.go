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

// Package coins is the client of wso2-coin transaction-service. User calls
// (wallets, payments) forward the caller's verified assertion as
// X-User-Assertion; every call authenticates with the store's shared
// client-credentials app, whose client id maps to the store treasury.
package coins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/apierror"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/reqctx"
)

// tokenFetchTimeout bounds a token-endpoint call.
var tokenFetchTimeout = 10 * time.Second

// PayTimeout bounds POST /payments on top of the client timeout. A payment
// that times out stays PENDING_PAYMENT; the reconciler settles it.
var PayTimeout = 10 * time.Second

// ErrNoUserAssertion means a user call was made outside a signed-in request.
var ErrNoUserAssertion = errors.New("coins: no user assertion in context")

// Config holds the base URL and the shared OAuth2 app.
type Config struct {
	BaseURL      string
	TokenURL     string
	ClientID     string
	ClientSecret string
	Scopes       []string
	Timeout      time.Duration // UPSTREAM_TIMEOUT
}

// Client calls transaction-service. It never contacts anything when built.
type Client struct {
	http    *http.Client
	baseURL string
}

// NewClient builds the client. Tokens are fetched lazily by x/oauth2.
func NewClient(cfg Config) *Client {
	cc := clientcredentials.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		TokenURL:     cfg.TokenURL,
		Scopes:       cfg.Scopes,
	}
	tokenCtx := context.WithValue(context.Background(), oauth2.HTTPClient, &http.Client{Timeout: tokenFetchTimeout})
	hc := cc.Client(tokenCtx)
	hc.Timeout = cfg.Timeout
	if hc.Timeout == 0 {
		hc.Timeout = 25 * time.Second
	}
	return &Client{http: hc, baseURL: strings.TrimRight(cfg.BaseURL, "/")}
}

// Wallet is one of the caller's wallets.
type Wallet struct {
	Address     string
	Balance     *big.Rat
	BalanceText string // as transaction-service sent it
	Default     bool
}

// PayRequest is the body of POST /payments. Only checkout builds one.
type PayRequest struct {
	FromAddress string `json:"fromAddress"`
	ToAddress   string `json:"toAddress"`
	Amount      string `json:"amount"`
	Reference   string `json:"reference"`
	Source      string `json:"source"`
}

// Payment is transaction-service's record of a payment.
type Payment struct {
	Reference   string `json:"reference"`
	FromAddress string `json:"fromAddress"`
	ToAddress   string `json:"toAddress"`
	Amount      string `json:"amount"`
	Replayed    bool   `json:"-"` // 200: the reference was already recorded
}

// Transaction is one ledger entry.
type Transaction struct {
	Reference   string `json:"reference"`
	FromAddress string `json:"fromAddress"`
	ToAddress   string `json:"toAddress"`
	Amount      string `json:"amount"`
	Timestamp   string `json:"timestamp"`
}

// SearchFilter is the body of POST /transactions/search.
type SearchFilter struct {
	FromAddresses []string `json:"fromAddresses,omitempty"`
	ToAddresses   []string `json:"toAddresses,omitempty"`
	Reference     string   `json:"reference,omitempty"`
	DateFrom      string   `json:"dateFrom,omitempty"`
	DateTo        string   `json:"dateTo,omitempty"`
	Limit         int      `json:"limit,omitempty"`
	Offset        int      `json:"offset,omitempty"`
}

// TransactionPage is one page of search results.
type TransactionPage struct {
	Transactions []Transaction `json:"transactions"`
	Total        int           `json:"total"`
	Limit        int           `json:"limit"`
	Offset       int           `json:"offset"`
}

// Wallets lists the caller's wallets, default first (GET /wallets/me).
func (c *Client) Wallets(ctx context.Context) ([]Wallet, error) {
	body, _, err := c.do(ctx, http.MethodGet, "/wallets/me", nil, true)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		WalletAddress string `json:"walletAddress"`
		Balance       string `json:"balance"`
		DefaultWallet bool   `json:"defaultWallet"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("coins: decode wallets: %w", err)
	}
	out := make([]Wallet, 0, len(raw))
	for _, w := range raw {
		bal, ok := new(big.Rat).SetString(w.Balance)
		if !ok {
			return nil, fmt.Errorf("coins: wallet balance %q is not a decimal", w.Balance)
		}
		out = append(out, Wallet{Address: w.WalletAddress, Balance: bal, BalanceText: w.Balance, Default: w.DefaultWallet})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Default && !out[j].Default })
	return out, nil
}

// Pay collects a payment from the caller's wallet (POST /payments). A 200
// replay comes back with Replayed set. Errors keep the upstream status.
func (c *Client) Pay(ctx context.Context, req PayRequest) (*Payment, error) {
	ctx, cancel := context.WithTimeout(ctx, PayTimeout)
	defer cancel()
	body, status, err := c.do(ctx, http.MethodPost, "/payments", req, true)
	if err != nil {
		return nil, err
	}
	var p Payment
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("coins: decode payment: %w", err)
	}
	p.Replayed = status == http.StatusOK
	return &p, nil
}

// Transaction reads one ledger entry by reference. It returns (nil, nil) when
// there is none.
func (c *Client) Transaction(ctx context.Context, reference string) (*Transaction, error) {
	body, _, err := c.do(ctx, http.MethodGet, "/transactions/"+url.PathEscape(reference), nil, false)
	if apierror.Status(err) == http.StatusNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var t Transaction
	if err := json.Unmarshal(body, &t); err != nil {
		return nil, fmt.Errorf("coins: decode transaction: %w", err)
	}
	return &t, nil
}

// Search pages through transactions (POST /transactions/search).
func (c *Client) Search(ctx context.Context, f SearchFilter) (*TransactionPage, error) {
	body, _, err := c.do(ctx, http.MethodPost, "/transactions/search", f, false)
	if err != nil {
		return nil, err
	}
	var p TransactionPage
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("coins: decode search: %w", err)
	}
	return &p, nil
}

// Treasury returns the wallet transaction-service maps to the store's own
// client id (GET /wallets/master/balance). The balance is ignored.
func (c *Client) Treasury(ctx context.Context) (string, error) {
	body, _, err := c.do(ctx, http.MethodGet, "/wallets/master/balance", nil, false)
	if err != nil {
		return "", err
	}
	var b struct {
		WalletAddress string `json:"walletAddress"`
	}
	if err := json.Unmarshal(body, &b); err != nil {
		return "", fmt.Errorf("coins: decode master balance: %w", err)
	}
	if b.WalletAddress == "" {
		return "", errors.New("coins: master balance has no walletAddress")
	}
	return b.WalletAddress, nil
}

// Health calls GET /health. It is used by /health/dependencies only.
func (c *Client) Health(ctx context.Context) error {
	_, _, err := c.do(ctx, http.MethodGet, "/health", nil, false)
	return err
}

// IsInsufficientFunds reports whether err is transaction-service's refusal for
// a low balance. It has no error code yet, so this matches the message text of
// a 400; keep it the only place that does.
func IsInsufficientFunds(err error) bool {
	var e *apierror.Error
	if !errors.As(err, &e) || e.StatusCode != http.StatusBadRequest {
		return false
	}
	var body struct {
		Message string `json:"message"`
	}
	if json.Unmarshal([]byte(e.Body), &body) != nil {
		return false
	}
	return strings.Contains(strings.ToLower(body.Message), "insufficient funds")
}

func (c *Client) do(ctx context.Context, method, path string, payload any, asUser bool) ([]byte, int, error) {
	var reader io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, 0, fmt.Errorf("coins: encode %s %s: %w", method, path, err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, 0, fmt.Errorf("coins: build request %s %s: %w", method, path, err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if id := reqctx.CorrelationID(ctx); id != "" {
		req.Header.Set("X-Correlation-ID", id)
	}
	if asUser {
		token := reqctx.UserAssertion(ctx)
		if token == "" {
			return nil, 0, ErrNoUserAssertion
		}
		req.Header.Set("X-User-Assertion", token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("coins: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("coins: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, apierror.New(resp.StatusCode, body)
	}
	return body, resp.StatusCode, nil
}
