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

// Package catalog holds the pure catalog rules: availability, the quote's line
// checks, the variant matrix, image hosts and slugs. No SQL, no HTTP.
package catalog

import (
	"fmt"
	"strings"
)

// Product statuses, as stored.
const (
	StatusDraft   = "DRAFT"
	StatusActive  = "ACTIVE"
	StatusRemoved = "REMOVED"
)

// RestockPolicy says whether a sold-out item comes back (design D18).
type RestockPolicy string

const (
	Restockable RestockPolicy = "RESTOCKABLE"
	Limited     RestockPolicy = "LIMITED"
)

// Availability is what the shop shows for a variant or product.
type Availability string

const (
	InStock    Availability = "IN_STOCK"
	LowStock   Availability = "LOW_STOCK"
	OutOfStock Availability = "OUT_OF_STOCK" // restockable, may come back: Notify me
	SoldOut    Availability = "SOLD_OUT"     // limited, gone for good: alternatives only
)

// AvailabilityOf maps free stock to an availability.
func AvailabilityOf(free, lowThreshold int, policy RestockPolicy) Availability {
	switch {
	case free <= 0 && policy == Limited:
		return SoldOut
	case free <= 0:
		return OutOfStock
	case free <= lowThreshold:
		return LowStock
	default:
		return InStock
	}
}

// LineState is a cart or quote line's state.
type LineState string

const (
	LineOK         LineState = "OK"
	LineOutOfStock LineState = "OUT_OF_STOCK"
	LineRemoved    LineState = "REMOVED"
	LineLimit      LineState = "LIMIT"
)

// LineFacts is everything CheckLine needs about one cart line.
type LineFacts struct {
	ProductID     string // lines of one product share its max_per_user
	ProductName   string
	Options       string
	ProductStatus string
	VariantActive bool
	Free          int
	Quantity      int
	MaxPerUser    *int
	PaidUnits     int // units already ordered within the limit window
}

// CheckLine applies the quote rules to one line (backend-spec §8.2) and
// returns its state and a reason to show the shopper.
func CheckLine(f LineFacts) (LineState, string) {
	label := f.ProductName
	if f.Options != "" {
		label += " · " + f.Options
	}
	switch {
	case f.ProductStatus != StatusActive || !f.VariantActive:
		return LineRemoved, label + " is no longer sold."
	case f.Free <= 0:
		return LineOutOfStock, label + " is out of stock."
	case f.Free < f.Quantity:
		return LineOutOfStock, fmt.Sprintf("Only %d left of %s.", f.Free, label)
	case f.MaxPerUser != nil && f.PaidUnits+f.Quantity > *f.MaxPerUser:
		return LineLimit, fmt.Sprintf("%s has a limit of %d per person.", label, *f.MaxPerUser)
	}
	return LineOK, ""
}

// LineResult is CheckLine's answer for one line.
type LineResult struct {
	State  LineState
	Reason string
}

// CheckLines checks a whole cart. max_per_user is per product, so each line
// also counts the units of earlier lines of the same product.
func CheckLines(lines []LineFacts) []LineResult {
	inCart := map[string]int{}
	out := make([]LineResult, 0, len(lines))
	for _, f := range lines {
		earlier := 0
		if f.ProductID != "" {
			earlier = inCart[f.ProductID]
			inCart[f.ProductID] += f.Quantity
		}
		f.PaidUnits += earlier
		state, reason := CheckLine(f)
		out = append(out, LineResult{State: state, Reason: reason})
	}
	return out
}

// VariantMatrix returns every combination of the option values, in option
// order. No options gives one empty combination (the single default variant).
func VariantMatrix(values [][]string) [][]string {
	out := [][]string{{}}
	for _, vals := range values {
		var next [][]string
		for _, prefix := range out {
			for _, v := range vals {
				combo := append(append([]string{}, prefix...), v)
				next = append(next, combo)
			}
		}
		out = next
	}
	return out
}

// OptionsLabel joins option values for display: "Black · M".
func OptionsLabel(values []string) string { return strings.Join(values, " · ") }

// Slugify makes a URL slug: lower-case ASCII letters and digits joined by "-".
func Slugify(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case r < 128 && !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimRight(b.String(), "-")
}
