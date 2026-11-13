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
	"regexp"
	"testing"
)

const photoProduct = "0192f3c4-7d1e-7a40-9b2c-5e8f1a2b3c4d"

func TestPhotoViewsOrder(t *testing.T) {
	if r, ok := PhotoRank("front"); !ok || r != 0 {
		t.Fatalf("front = %d, %v", r, ok)
	}
	if r, ok := PhotoRank("detail-4"); !ok || r != 9 {
		t.Fatalf("detail-4 = %d, %v", r, ok)
	}
	if _, ok := PhotoRank("side"); ok {
		t.Fatal("side is not a view")
	}
	if len(PhotoViews) != 10 {
		t.Fatalf("views = %v", PhotoViews)
	}
}

func TestPhotoExt(t *testing.T) {
	for ct, want := range map[string]string{"image/jpeg": "jpg", "image/png": "png", "image/webp": "webp"} {
		if got, ok := PhotoExt(ct); !ok || got != want {
			t.Errorf("%s = %q, %v", ct, got, ok)
		}
	}
	for _, ct := range []string{"image/gif", "image/svg+xml", "text/html; charset=utf-8", ""} {
		if _, ok := PhotoExt(ct); ok {
			t.Errorf("%q accepted", ct)
		}
	}
}

func TestNewPhotoPathShape(t *testing.T) {
	p, err := NewPhotoPath(photoProduct, "back", "webp")
	if err != nil || !regexp.MustCompile(`^products/`+photoProduct+`/back-[0-9a-f]{8}\.webp$`).MatchString(p) {
		t.Fatalf("path = %q, %v", p, err)
	}
	q, _ := NewPhotoPath(photoProduct, "back", "webp")
	if p == q {
		t.Fatal("two uploads got the same name")
	}
	if _, err := NewPhotoPath(photoProduct, "side", "webp"); err == nil {
		t.Fatal("unknown view accepted")
	}
	if _, err := NewPhotoPath(photoProduct, "front", "gif"); err == nil {
		t.Fatal("gif accepted")
	}
	if _, err := NewPhotoPath("not-a-uuid", "front", "jpg"); err == nil {
		t.Fatal("bad product id accepted")
	}
}

func TestValidPhotoPath(t *testing.T) {
	other := "0192f3c4-7d1e-7a40-9b2c-000000000000"
	ok := "products/" + photoProduct + "/front-3f9a2c1d.jpg"
	for _, tc := range []struct {
		view, path string
		want       bool
	}{
		{"front", ok, true},
		{"detail-3", "products/" + photoProduct + "/detail-3-0a1b2c3d.png", true},
		{"back", ok, false},
		{"front", "products/" + other + "/front-3f9a2c1d.jpg", false},
		{"front", "products/" + photoProduct + "/front-3F9A2C1D.jpg", false},
		{"front", "products/" + photoProduct + "/front-3f9a2c1d.svg", false},
		{"front", "products/" + photoProduct + "/../x/front-3f9a2c1d.jpg", false},
		{"front", ok + "?x=1", false},
		{"front", "/" + ok, false},
	} {
		if got := ValidPhotoPath(photoProduct, tc.view, tc.path); got != tc.want {
			t.Errorf("%s %s = %v", tc.view, tc.path, got)
		}
	}
}

func TestParsePhotoPath(t *testing.T) {
	id, view, ok := ParsePhotoPath("products/" + photoProduct + "/detail-1-3f9a2c1d.webp")
	if !ok || id != photoProduct || view != "detail-1" {
		t.Fatalf("= %q %q %v", id, view, ok)
	}
	if _, _, ok := ParsePhotoPath("products/x/front-3f9a2c1d.webp"); ok {
		t.Fatal("bad id parsed")
	}
}
