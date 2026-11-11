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

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/store"
)

func officeFrom(o store.PickupOffice) Office {
	var locs []string
	_ = json.Unmarshal(o.HrLocationsJson, &locs)
	if locs == nil {
		locs = []string{}
	}
	return Office{ID: o.ID, Name: o.Name, Address: o.Address, Country: o.Country, CollectionPoint: o.CollectionPoint,
		Hours: o.Hours, LeadTimeDays: int(o.LeadTimeDays), HRLocations: locs, Active: o.Active, Sort: int(o.Sort)}
}

// Office returns one office, or (nil, nil) when there is none.
func (s *Store) Office(ctx context.Context, id string) (*Office, error) {
	o, err := s.q.GetOffice(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	v := officeFrom(o)
	return &v, nil
}

// Offices lists offices; activeOnly is what shoppers see.
func (s *Store) Offices(ctx context.Context, activeOnly bool) ([]Office, error) {
	var rows []store.PickupOffice
	var err error
	if activeOnly {
		rows, err = s.q.ListActiveOffices(ctx)
	} else {
		rows, err = s.q.ListOffices(ctx)
	}
	if err != nil {
		return nil, err
	}
	out := make([]Office, 0, len(rows))
	for _, r := range rows {
		out = append(out, officeFrom(r))
	}
	return out, nil
}

// DefaultOfficeID is the office of the caller's last order if still active,
// else the first active office whose HR locations contain location, else "".
func (s *Store) DefaultOfficeID(ctx context.Context, email, location string) (string, error) {
	active, err := s.Offices(ctx, true)
	if err != nil {
		return "", err
	}
	last, err := s.q.LastOrderOffice(ctx, email)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	for _, o := range active {
		if o.ID == last {
			return o.ID, nil
		}
	}
	if location = strings.TrimSpace(location); location != "" {
		for _, o := range active {
			if slices.ContainsFunc(o.HRLocations, func(l string) bool { return strings.EqualFold(l, location) }) {
				return o.ID, nil
			}
		}
	}
	return "", nil
}

// SaveOffice creates (empty ID) or updates an office, with an admin action row.
func (s *Store) SaveOffice(ctx context.Context, a Actor, o Office) (*Office, error) {
	locs, err := json.Marshal(o.HRLocations)
	if err != nil {
		return nil, err
	}
	creating := o.ID == ""
	if creating {
		o.ID = newID()
	}
	err = s.inTx(ctx, func(q *store.Queries) error {
		if creating {
			err := q.CreateOffice(ctx, store.CreateOfficeParams{ID: o.ID, Name: o.Name, Address: o.Address, Country: o.Country,
				CollectionPoint: o.CollectionPoint, Hours: o.Hours, LeadTimeDays: int32(o.LeadTimeDays), // #nosec G115
				HrLocationsJson: locs, Active: o.Active, Sort: int32(o.Sort)}) // #nosec G115
			if isDuplicate(err) {
				return ErrConflict
			}
			if err != nil {
				return err
			}
			return audit(ctx, q, a, "office_created", "pickup_office", o.ID, map[string]any{"name": o.Name})
		}
		n, err := q.UpdateOffice(ctx, store.UpdateOfficeParams{Name: o.Name, Address: o.Address, Country: o.Country,
			CollectionPoint: o.CollectionPoint, Hours: o.Hours, LeadTimeDays: int32(o.LeadTimeDays), // #nosec G115
			HrLocationsJson: locs, Active: o.Active, Sort: int32(o.Sort), ID: o.ID}) // #nosec G115
		if isDuplicate(err) {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		if n == 0 {
			if _, err := q.GetOffice(ctx, o.ID); err != nil {
				return notFound(err)
			}
		}
		return audit(ctx, q, a, "office_updated", "pickup_office", o.ID, map[string]any{"name": o.Name, "active": o.Active})
	})
	if err != nil {
		return nil, err
	}
	return &o, nil
}
