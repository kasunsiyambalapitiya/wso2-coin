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
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/repository"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/reqctx"
)

func itoa(n int) string { return strconv.Itoa(n) }

// AlertStore is the stock-alert persistence.
type AlertStore interface {
	Alerts(ctx context.Context, email string) ([]repository.Alert, error)
	CreateAlert(ctx context.Context, email, variantID string) (string, error)
	CancelAlert(ctx context.Context, email, id string) error
}

// AlertsHandler serves the caller's "Notify me" alerts.
type AlertsHandler struct{ store AlertStore }

// NewAlertsHandler builds the handler.
func NewAlertsHandler(s AlertStore) *AlertsHandler { return &AlertsHandler{store: s} }

// List returns the caller's alerts.
func (h *AlertsHandler) List(c *gin.Context) {
	u := reqctx.UserFrom(c.Request.Context())
	alerts, err := h.store.Alerts(c.Request.Context(), u.Email)
	if err != nil {
		writeInternal(c, "alerts: list failed", err)
		return
	}
	if alerts == nil {
		alerts = []repository.Alert{}
	}
	writeJSON(c, http.StatusOK, gin.H{"items": alerts})
}

type alertBody struct {
	VariantID string `json:"variantId"`
}

func (b alertBody) validate() error {
	if !uuidRe.MatchString(b.VariantID) {
		return errors.New("variantId must be a UUID.")
	}
	return nil
}

// Create adds an alert on an out-of-stock, restockable variant.
func (h *AlertsHandler) Create(c *gin.Context) {
	var b alertBody
	if !readJSON(c, &b) {
		return
	}
	u := reqctx.UserFrom(c.Request.Context())
	id, err := h.store.CreateAlert(c.Request.Context(), u.Email, b.VariantID)
	switch {
	case errors.Is(err, repository.ErrNotFound):
		writeError(c, http.StatusNotFound, ErrMsgNotFound)
	case errors.Is(err, repository.ErrNotAvailable):
		writeErrorCode(c, http.StatusConflict, "Alerts are for items that are out of stock and will come back.", CodeNotAvailable)
	case err != nil:
		writeInternal(c, "alerts: create failed", err)
	default:
		writeJSON(c, http.StatusCreated, gin.H{"id": id})
	}
}

// Delete cancels one of the caller's pending alerts.
func (h *AlertsHandler) Delete(c *gin.Context) {
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	u := reqctx.UserFrom(c.Request.Context())
	err := h.store.CancelAlert(c.Request.Context(), u.Email, id)
	switch {
	case errors.Is(err, repository.ErrNotFound):
		writeError(c, http.StatusNotFound, ErrMsgNotFound)
	case err != nil:
		writeInternal(c, "alerts: cancel failed", err)
	default:
		c.Status(http.StatusNoContent)
	}
}
