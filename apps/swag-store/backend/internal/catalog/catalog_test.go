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

package catalog

import (
	"strings"
	"testing"
)

func ptr(n int) *int { return &n }

func TestAvailabilityOf(t *testing.T) {
	cases := []struct {
		free   int
		policy RestockPolicy
		want   Availability
	}{
		{10, Restockable, InStock},
		{6, Restockable, InStock},
		{5, Restockable, LowStock},
		{1, Limited, LowStock},
		{0, Restockable, OutOfStock},
		{-2, Restockable, OutOfStock},
		{0, Limited, SoldOut},
	}
	for _, tc := range cases {
		if got := AvailabilityOf(tc.free, 5, tc.policy); got != tc.want {
			t.Errorf("AvailabilityOf(%d, %s) = %s, want %s", tc.free, tc.policy, got, tc.want)
		}
	}
}

func TestCheckLine(t *testing.T) {
	base := LineFacts{ProductName: "WSO2 Hoodie", Options: "Black · M", ProductStatus: StatusActive, VariantActive: true, Free: 3, Quantity: 2}
	cases := []struct {
		name   string
		mutate func(*LineFacts)
		state  LineState
		reason string
	}{
		{"ok", func(*LineFacts) {}, LineOK, ""},
		{"removed product", func(f *LineFacts) { f.ProductStatus = StatusRemoved }, LineRemoved, "no longer sold"},
		{"draft product", func(f *LineFacts) { f.ProductStatus = StatusDraft }, LineRemoved, "no longer sold"},
		{"inactive variant", func(f *LineFacts) { f.VariantActive = false }, LineRemoved, "no longer sold"},
		{"short stock", func(f *LineFacts) { f.Free = 1 }, LineOutOfStock, "Only 1 left"},
		{"no stock", func(f *LineFacts) { f.Free = 0 }, LineOutOfStock, "out of stock"},
		{"limit", func(f *LineFacts) { f.MaxPerUser = ptr(2); f.PaidUnits = 1 }, LineLimit, "limit of 2"},
		{"limit exact", func(f *LineFacts) { f.MaxPerUser = ptr(3); f.PaidUnits = 1 }, LineOK, ""},
	}
	for _, tc := range cases {
		f := base
		tc.mutate(&f)
		state, reason := CheckLine(f)
		if state != tc.state || !strings.Contains(reason, tc.reason) {
			t.Errorf("%s: (%s, %q), want (%s, ~%q)", tc.name, state, reason, tc.state, tc.reason)
		}
	}
}

func TestVariantMatrix(t *testing.T) {
	got := VariantMatrix([][]string{{"Black", "Navy"}, {"S", "M"}})
	want := [][]string{{"Black", "S"}, {"Black", "M"}, {"Navy", "S"}, {"Navy", "M"}}
	if len(got) != len(want) {
		t.Fatalf("matrix = %v", got)
	}
	for i := range want {
		if strings.Join(got[i], "|") != strings.Join(want[i], "|") {
			t.Fatalf("matrix = %v, want %v", got, want)
		}
	}
	if m := VariantMatrix(nil); len(m) != 1 || len(m[0]) != 0 {
		t.Fatalf("no options must give one empty combination, got %v", m)
	}
}

func TestValidImageURL(t *testing.T) {
	hosts := []string{"*.wso2.com", "wso2.cachefly.net"}
	cases := map[string]bool{
		"https://wso2.cachefly.net/a.png":      true,
		"https://cdn.wso2.com/a.png":           true,
		"https://a.b.wso2.com/a.png":           true,
		"https://wso2.com/a.png":               false,
		"http://cdn.wso2.com/a.png":            false,
		"https://evilwso2.com/a.png":           false,
		"https://cdn.wso2.com.evil.net/a.png":  false,
		"javascript:alert(1)":                  false,
		"https://user@cdn.wso2.com/a.png":      false,
		"https://wso2.cachefly.net:8443/a.png": false,
	}
	for u, want := range cases {
		if got := ValidImageURL(u, hosts); got != want {
			t.Errorf("ValidImageURL(%q) = %v, want %v", u, got, want)
		}
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"WSO2 Hoodie":           "wso2-hoodie",
		"  Laptop  Pouch!! ":    "laptop-pouch",
		"Café Mug (Large)":      "caf-mug-large",
		"Bluetooth Speaker 2.0": "bluetooth-speaker-2-0",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOptionsLabel(t *testing.T) {
	if got := OptionsLabel([]string{"Black", "M"}); got != "Black · M" {
		t.Fatal(got)
	}
	if got := OptionsLabel(nil); got != "" {
		t.Fatal(got)
	}
}

func TestCheckLinesCountsEarlierLinesOfTheSameProduct(t *testing.T) {
	limit := 1
	lines := []LineFacts{
		{ProductID: "p1", ProductName: "Hoodie", Options: "M", ProductStatus: StatusActive, VariantActive: true, Free: 5, Quantity: 1, MaxPerUser: &limit},
		{ProductID: "p1", ProductName: "Hoodie", Options: "L", ProductStatus: StatusActive, VariantActive: true, Free: 5, Quantity: 1, MaxPerUser: &limit},
		{ProductID: "p2", ProductName: "Mug", ProductStatus: StatusActive, VariantActive: true, Free: 5, Quantity: 1, MaxPerUser: &limit},
	}
	got := CheckLines(lines)
	if got[0].State != LineOK || got[1].State != LineLimit || got[2].State != LineOK {
		t.Fatalf("states = %+v", got)
	}
}
