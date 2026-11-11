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

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/hr"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/reqctx"
)

// EmployeeLookup reads the caller's HR record (hr.Cache).
type EmployeeLookup interface {
	Lookup(ctx context.Context, email string) (*hr.Employee, error)
}

// UsersHandler serves GET /users/me.
type UsersHandler struct {
	guard *AccessGuard
	hr    EmployeeLookup
}

// NewUsersHandler builds the handler.
func NewUsersHandler(guard *AccessGuard, employees EmployeeLookup) *UsersHandler {
	return &UsersHandler{guard: guard, hr: employees}
}

type meResponse struct {
	Email          string   `json:"email"`
	FirstName      string   `json:"firstName"`
	LastName       string   `json:"lastName"`
	Roles          []string `json:"roles"`
	EmployeeStatus *string  `json:"employeeStatus"`
}

// Me returns the caller, the portal roles the guard grants, and the HR status
// (null when HR is down or has no record).
func (h *UsersHandler) Me(c *gin.Context) {
	u := reqctx.UserFrom(c.Request.Context())
	resp := meResponse{Email: u.Email, FirstName: u.FirstName, LastName: u.LastName, Roles: h.guard.Roles(u)}
	emp, err := h.hr.Lookup(c.Request.Context(), u.Email)
	if err != nil {
		slog.WarnContext(c.Request.Context(), "users/me: HR lookup failed", "err", err)
	} else if emp != nil {
		status := emp.EmployeeStatus
		resp.EmployeeStatus = &status
		if resp.FirstName == "" && resp.LastName == "" {
			resp.FirstName, resp.LastName = emp.FirstName, emp.LastName
		}
	}
	writeJSON(c, http.StatusOK, resp)
}
