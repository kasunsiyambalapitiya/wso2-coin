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
	"slices"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/catalog"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/repository"
)

// PhotoURLs turns a stored photo path into a signed, read-only URL, or nil
// when photos are off or can't be signed right now.
type PhotoURLs interface {
	ReadURL(path string) *string
}

// PhotoStore also stores new photos. Put never overwrites a blob.
type PhotoStore interface {
	PhotoURLs
	Put(ctx context.Context, path string, body []byte, contentType string) error
}

type imageJSON struct {
	View string  `json:"view"`
	Path string  `json:"path"`
	URL  *string `json:"url"`
	Alt  string  `json:"alt"`
}

// photoURL is the signed URL for a card's or cart line's photo, or nil.
func photoURL(photos PhotoURLs, path string) *string {
	if path == "" {
		return nil
	}
	return photos.ReadURL(path)
}

// imagesJSON lists a product's photos in view order, each with its signed URL.
func imagesJSON(photos PhotoURLs, images []repository.Image) []imageJSON {
	out := make([]imageJSON, 0, len(images))
	for _, im := range images {
		out = append(out, imageJSON{View: im.View, Path: im.Path, URL: photoURL(photos, im.Path), Alt: im.Alt})
	}
	slices.SortStableFunc(out, func(a, b imageJSON) int {
		ra, _ := catalog.PhotoRank(a.View)
		rb, _ := catalog.PhotoRank(b.View)
		return ra - rb
	})
	return out
}
