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
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
)

// PhotoViews are the photo slots of a product, in gallery order. Each view
// holds at most one photo, and front is the main one.
var PhotoViews = []string{"front", "back", "left", "right", "top", "bottom", "detail-1", "detail-2", "detail-3", "detail-4"}

// photoPathRe is the blob name of a product photo:
// products/{productId}/{view}-{8 lowercase hex}.{jpg|png|webp}.
var photoPathRe = regexp.MustCompile(`^products/([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})/` +
	`(front|back|left|right|top|bottom|detail-[1-4])-[0-9a-f]{8}\.(jpg|png|webp)$`)

var photoProductRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// PhotoRank is the view's place in gallery order.
func PhotoRank(view string) (int, bool) {
	i := slices.Index(PhotoViews, view)
	return i, i >= 0
}

// PhotoExt is the file extension for a sniffed content type. Only JPEG, PNG
// and WebP are photos.
func PhotoExt(contentType string) (string, bool) {
	switch contentType {
	case "image/jpeg":
		return "jpg", true
	case "image/png":
		return "png", true
	case "image/webp":
		return "webp", true
	}
	return "", false
}

// NewPhotoPath names a new upload. The random part makes every upload a new
// blob, so nothing is overwritten and no cache shows an old photo.
func NewPhotoPath(productID, view, ext string) (string, error) {
	if !photoProductRe.MatchString(productID) {
		return "", fmt.Errorf("photo path: bad product id")
	}
	if _, ok := PhotoRank(view); !ok {
		return "", fmt.Errorf("photo path: unknown view %q", view)
	}
	if ext != "jpg" && ext != "png" && ext != "webp" {
		return "", fmt.Errorf("photo path: unknown extension %q", ext)
	}
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "products/" + productID + "/" + view + "-" + hex.EncodeToString(b) + "." + ext, nil
}

// ParsePhotoPath splits a photo blob name into its product id and view.
func ParsePhotoPath(path string) (productID, view string, ok bool) {
	m := photoPathRe.FindStringSubmatch(path)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// ValidPhotoPath reports whether path is a photo of this product in this view.
func ValidPhotoPath(productID, view, path string) bool {
	id, v, ok := ParsePhotoPath(path)
	return ok && id == productID && v == view
}
