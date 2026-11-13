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
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/apierror"
)

// Fixed messages. Clients branch on errorCode, never on these.
const (
	ErrMsgUnauthorized       = "You are not authorized to perform this action. Please try again."
	ErrMsgForbidden          = "Access to the requested resource is forbidden!"
	ErrMsgNotWSO2            = "The Swag Store is open to WSO2 employees only."
	ErrMsgNotFound           = "The requested resource was not found!"
	ErrMsgMethodNotAllowed   = "Method not allowed."
	ErrMsgBadRequest         = "Invalid request payload."
	ErrMsgTooLarge           = "Request body too large."
	ErrMsgInternal           = "An internal server error occurred. Please try again later."
	ErrMsgInvalidUUID        = "Invalid UUID format."
	ErrMsgUnavailable        = "A service the store depends on is unavailable. Please try again later."
	ErrMsgCoinsUnavailable   = "Your wallets can't be reached right now. Please try again later."
	ErrMsgHRUnavailable      = "We can't check your employee record right now. Please try again later."
	ErrMsgPaymentsPaused     = "Payments are paused for a moment. Your coins are safe. Try again later."
	ErrMsgNotActiveEmployee  = "Only active employees can place orders."
	ErrMsgWalletNotOwned     = "That wallet is not yours."
	ErrMsgInsufficientFunds  = "Your wallet balance is below the order total."
	ErrMsgPaymentFailed      = "The payment was refused. No coins were taken."
	ErrMsgQuoteChanged       = "Your cart changed. Please review it and try again."
	ErrMsgOutOfStock         = "Not enough stock left."
	ErrMsgNotAvailable       = "This item is not available."
	ErrMsgLimitReached       = "You have reached the limit for this item."
	ErrMsgStateConflict      = "This changed in the meantime. Refresh and try again."
	ErrMsgIdempotencyMissing = "Header Idempotency-Key must be a UUID."
)

// Stable error codes (snake_case).
const (
	CodeNotWSO2             = "not_wso2"
	CodeWalletNotOwned      = "wallet_not_owned"
	CodeNotActiveEmployee   = "not_active_employee"
	CodeInsufficientFunds   = "insufficient_funds"
	CodePaymentFailed       = "payment_failed"
	CodeQuoteChanged        = "quote_changed"
	CodeOutOfStock          = "out_of_stock"
	CodeNotAvailable        = "not_available"
	CodeLimitReached        = "limit_reached"
	CodeStateConflict       = "state_conflict"
	CodeHRUnavailable       = "hr_unavailable"
	CodeCoinsUnavailable    = "coins_unavailable"
	CodePaymentsPaused      = "payments_paused"
	CodeUnsupportedImage    = "unsupported_image"
	CodeAssetsNotConfigured = "assets_not_configured"
	CodeAssetsUnavailable   = "assets_unavailable"
)

type errorBody struct {
	Message   string `json:"message"`
	ErrorCode string `json:"errorCode,omitempty"`
}

func writeError(c *gin.Context, status int, message string) {
	writeErrorCode(c, status, message, "")
}

func writeErrorCode(c *gin.Context, status int, message, code string) {
	c.JSON(status, errorBody{Message: message, ErrorCode: code})
}

func abortError(c *gin.Context, status int, message, code string) {
	c.AbortWithStatusJSON(status, errorBody{Message: message, ErrorCode: code})
}

func writeJSON(c *gin.Context, status int, v any) {
	c.JSON(status, v)
}

// writeInternal logs err with the correlation id and writes the fixed 500.
func writeInternal(c *gin.Context, msg string, err error) {
	slog.ErrorContext(c.Request.Context(), msg, "err", err)
	writeError(c, http.StatusInternalServerError, ErrMsgInternal)
}

// mapUpstreamErrorGeneric logs an upstream error in full and maps it without
// echoing the upstream body: 502/503/504 → 503 with fallback, 404 → 404,
// anything else (including network errors) → 500 with fallback.
func mapUpstreamErrorGeneric(c *gin.Context, err error, fallback string) {
	slog.ErrorContext(c.Request.Context(), "upstream call failed", "err", err)
	var apiErr *apierror.Error
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case http.StatusNotFound:
			writeError(c, http.StatusNotFound, ErrMsgNotFound)
			return
		case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			writeError(c, http.StatusServiceUnavailable, fallback)
			return
		}
	}
	writeError(c, http.StatusInternalServerError, fallback)
}

// NoRoute is the JSON 404 for unknown paths.
func NoRoute(c *gin.Context) { writeError(c, http.StatusNotFound, ErrMsgNotFound) }

// NoMethod is the JSON 405 for a known path with the wrong method.
func NoMethod(c *gin.Context) { writeError(c, http.StatusMethodNotAllowed, ErrMsgMethodNotAllowed) }
