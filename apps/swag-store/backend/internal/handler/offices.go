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
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/repository"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/reqctx"
)

// OfficeStore is the pickup-office persistence.
type OfficeStore interface {
	Offices(ctx context.Context, activeOnly bool) ([]repository.Office, error)
	Office(ctx context.Context, id string) (*repository.Office, error)
	DefaultOfficeID(ctx context.Context, email, location string) (string, error)
	SaveOffice(ctx context.Context, a repository.Actor, o repository.Office) (*repository.Office, error)
}

// OfficesHandler serves pickup offices to shoppers and Fulfilment Ops.
type OfficesHandler struct {
	store OfficeStore
	hr    EmployeeLookup
}

// NewOfficesHandler builds the handler.
func NewOfficesHandler(s OfficeStore, employees EmployeeLookup) *OfficesHandler {
	return &OfficesHandler{store: s, hr: employees}
}

func actorOf(c *gin.Context) repository.Actor {
	u := reqctx.UserFrom(c.Request.Context())
	return repository.Actor{Email: u.Email, UserID: u.UserID}
}

// List returns active offices and the caller's default (backend-spec §7.5).
// HR is only a hint here: when it fails, the default falls back to the last order.
func (h *OfficesHandler) List(c *gin.Context) {
	ctx := c.Request.Context()
	u := reqctx.UserFrom(ctx)
	offices, err := h.store.Offices(ctx, true)
	if err != nil {
		writeInternal(c, "offices: list failed", err)
		return
	}
	location := ""
	if emp, err := h.hr.Lookup(ctx, u.Email); err != nil {
		slog.WarnContext(ctx, "offices: HR lookup failed; default office without location", "err", err)
	} else if emp != nil {
		location = emp.Location
	}
	def, err := h.store.DefaultOfficeID(ctx, u.Email, location)
	if err != nil {
		writeInternal(c, "offices: default failed", err)
		return
	}
	out := make([]officeJSON, 0, len(offices))
	for _, o := range offices {
		out = append(out, officeJSON{ID: o.ID, Name: o.Name, Address: o.Address, CollectionPoint: o.CollectionPoint,
			Hours: o.Hours, LeadTimeDays: o.LeadTimeDays})
	}
	writeJSON(c, http.StatusOK, gin.H{"items": out, "defaultOfficeId": strPtr(def)})
}

// AdminList returns every office, including inactive ones and the HR mapping.
func (h *OfficesHandler) AdminList(c *gin.Context) {
	offices, err := h.store.Offices(c.Request.Context(), false)
	if err != nil {
		writeInternal(c, "offices: list failed", err)
		return
	}
	writeJSON(c, http.StatusOK, gin.H{"items": offices})
}

type officeBody struct {
	Name            *string   `json:"name"`
	Address         *string   `json:"address"`
	Country         *string   `json:"country"`
	CollectionPoint *string   `json:"collectionPoint"`
	Hours           *string   `json:"hours"`
	LeadTimeDays    *int      `json:"leadTimeDays"`
	HRLocations     *[]string `json:"hrLocations"`
	Active          *bool     `json:"active"`
	Sort            *int      `json:"sort"`
}

func (b officeBody) apply(o *repository.Office) error {
	set := func(dst *string, v *string, field string, max int, required bool) error {
		if v == nil {
			return nil
		}
		s := strings.TrimSpace(*v)
		if (required && s == "") || len(s) > max {
			return errors.New(field + " must be 1 to " + itoa(max) + " characters.")
		}
		*dst = s
		return nil
	}
	if err := set(&o.Name, b.Name, "name", 120, true); err != nil {
		return err
	}
	for _, f := range []struct {
		dst   *string
		v     *string
		name  string
		limit int
	}{{&o.Address, b.Address, "address", 500}, {&o.Country, b.Country, "country", 80},
		{&o.CollectionPoint, b.CollectionPoint, "collectionPoint", 200}, {&o.Hours, b.Hours, "hours", 200}} {
		if err := set(f.dst, f.v, f.name, f.limit, false); err != nil {
			return err
		}
	}
	if b.LeadTimeDays != nil {
		if *b.LeadTimeDays < 0 || *b.LeadTimeDays > 60 {
			return errors.New("leadTimeDays must be between 0 and 60.")
		}
		o.LeadTimeDays = *b.LeadTimeDays
	}
	if b.HRLocations != nil {
		if len(*b.HRLocations) > 20 {
			return errors.New("hrLocations holds at most 20 values.")
		}
		locs := []string{}
		for _, l := range *b.HRLocations {
			if l = strings.TrimSpace(l); l != "" && len(l) <= 80 {
				locs = append(locs, l)
			}
		}
		o.HRLocations = locs
	}
	if b.Active != nil {
		o.Active = *b.Active
	}
	if b.Sort != nil {
		o.Sort = *b.Sort
	}
	if o.Name == "" {
		return errors.New("name is required.")
	}
	return nil
}

// Create adds an office.
func (h *OfficesHandler) Create(c *gin.Context) {
	var b officeBody
	if !readJSON(c, &b) {
		return
	}
	o := repository.Office{LeadTimeDays: 2, HRLocations: []string{}, Active: true}
	if err := b.apply(&o); err != nil {
		writeError(c, http.StatusBadRequest, err.Error())
		return
	}
	h.save(c, o, http.StatusCreated)
}

// Update edits an office. Offices are deactivated, never deleted.
func (h *OfficesHandler) Update(c *gin.Context) {
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	var b officeBody
	if !readJSON(c, &b) {
		return
	}
	cur, err := h.store.Office(c.Request.Context(), id)
	if err != nil {
		writeInternal(c, "offices: read failed", err)
		return
	}
	if cur == nil {
		writeError(c, http.StatusNotFound, ErrMsgNotFound)
		return
	}
	o := *cur
	if err := b.apply(&o); err != nil {
		writeError(c, http.StatusBadRequest, err.Error())
		return
	}
	h.save(c, o, http.StatusOK)
}

func (h *OfficesHandler) save(c *gin.Context, o repository.Office, status int) {
	saved, err := h.store.SaveOffice(c.Request.Context(), actorOf(c), o)
	switch {
	case errors.Is(err, repository.ErrConflict):
		writeErrorCode(c, http.StatusConflict, "An office with this name already exists.", CodeStateConflict)
	case errors.Is(err, repository.ErrNotFound):
		writeError(c, http.StatusNotFound, ErrMsgNotFound)
	case err != nil:
		writeInternal(c, "offices: save failed", err)
	default:
		writeJSON(c, status, saved)
	}
}
