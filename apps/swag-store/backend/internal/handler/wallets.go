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
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/coins"
)

// WalletLister lists the caller's wallets (coins.Client).
type WalletLister interface {
	Wallets(ctx context.Context) ([]coins.Wallet, error)
}

// WalletsHandler serves GET /wallets.
type WalletsHandler struct{ coins WalletLister }

// NewWalletsHandler builds the handler.
func NewWalletsHandler(c WalletLister) *WalletsHandler { return &WalletsHandler{coins: c} }

type walletResponse struct {
	WalletAddress string `json:"walletAddress"`
	Balance       string `json:"balance"`
	DefaultWallet bool   `json:"defaultWallet"`
}

// List proxies /wallets/me, default first. Balances are never cached.
func (h *WalletsHandler) List(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	ws, err := h.coins.Wallets(c.Request.Context())
	if err != nil {
		slog.ErrorContext(c.Request.Context(), "wallets: transaction-service failed", "err", err)
		writeErrorCode(c, http.StatusServiceUnavailable, ErrMsgCoinsUnavailable, CodeCoinsUnavailable)
		return
	}
	out := make([]walletResponse, 0, len(ws))
	for _, w := range ws {
		out = append(out, walletResponse{WalletAddress: w.Address, Balance: w.BalanceText, DefaultWallet: w.Default})
	}
	writeJSON(c, http.StatusOK, out)
}
