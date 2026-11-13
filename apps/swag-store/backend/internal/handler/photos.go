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
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/assets"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/catalog"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/repository"
)

// PhotoURLs turns a stored photo path into a signed, read-only URL, or nil
// when photos are off or can't be signed right now.
type PhotoURLs interface {
	ReadURL(path string) *string
}

// PhotoStore also stores new photos. Put never overwrites a blob. Health is
// "ok", "not_configured", or "key_unavailable" when photos can't be signed.
type PhotoStore interface {
	PhotoURLs
	Put(ctx context.Context, path string, body []byte, contentType string) error
	Health() string
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

// multipartSlack is room for the form's boundaries and headers on top of
// the photo itself.
const multipartSlack = 64 << 10

// UploadImage stores one photo for a view (POST /admin/products/{id}/images,
// multipart/form-data with a view field and a file part). The product is not
// changed: the editor saves the photo list with its next PATCH.
func (h *AdminCatalogHandler) UploadImage(c *gin.Context) {
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	// The form is read before any refusal: a reply sent while the browser is
	// still uploading can reach it as a reset connection, not the message.
	view, body, ok := h.readPhotoForm(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	d, err := h.store.ProductByID(ctx, id)
	if err != nil {
		writeInternal(c, "admin photos: read product failed", err)
		return
	}
	if d == nil {
		writeError(c, http.StatusNotFound, ErrMsgNotFound)
		return
	}
	if d.Product.Status == catalog.StatusRemoved {
		writeErrorCode(c, http.StatusConflict, "This product was removed. Restore it to change its photos.", CodeStateConflict)
		return
	}
	// A photo that can't be shown isn't stored: the admin would see a blank slot.
	switch h.photos.Health() {
	case assets.HealthNotConfigured:
		writeErrorCode(c, http.StatusServiceUnavailable, "Photos aren't set up in this environment.", CodeAssetsNotConfigured)
		return
	case assets.HealthKeyMissing:
		slog.ErrorContext(ctx, "admin photos: no signing key; upload refused")
		writeErrorCode(c, http.StatusServiceUnavailable, "Photo storage can't be reached right now. Try again in a minute.", CodeAssetsUnavailable)
		return
	}
	// The bytes decide the type; the part's Content-Type and file name are not trusted.
	contentType := http.DetectContentType(body)
	ext, ok := catalog.PhotoExt(contentType)
	if !ok {
		writeErrorCode(c, http.StatusUnsupportedMediaType, "Photos must be JPEG, PNG or WebP.", CodeUnsupportedImage)
		return
	}
	path, err := h.putPhoto(ctx, id, view, ext, body, contentType)
	switch {
	case errors.Is(err, assets.ErrNotConfigured):
		writeErrorCode(c, http.StatusServiceUnavailable, "Photos aren't set up in this environment.", CodeAssetsNotConfigured)
		return
	case err != nil:
		slog.ErrorContext(ctx, "admin photos: upload failed", "err", err)
		writeErrorCode(c, http.StatusServiceUnavailable, "Photo storage can't be reached right now. Try again in a minute.", CodeAssetsUnavailable)
		return
	}
	if err := h.store.RecordPhotoUpload(ctx, actorOf(c), id, repository.PhotoUploadDetail{
		View: view, Path: path, Bytes: len(body), ContentType: contentType,
	}); err != nil {
		writeInternal(c, "admin photos: audit failed", err)
		return
	}
	writeJSON(c, http.StatusCreated, gin.H{"view": view, "path": path, "url": h.photos.ReadURL(path)})
}

// putPhoto stores the photo under a new name. Names are random, so a clash
// gets one more try with another name.
func (h *AdminCatalogHandler) putPhoto(ctx context.Context, productID, view, ext string, body []byte, contentType string) (string, error) {
	for range 2 {
		path, err := catalog.NewPhotoPath(productID, view, ext)
		if err != nil {
			return "", err
		}
		err = h.photos.Put(ctx, path, body, contentType)
		if !errors.Is(err, assets.ErrExists) {
			return path, err
		}
	}
	return "", errors.New("photo name clashed twice")
}

// readPhotoForm reads exactly one view field and one file part, part by part,
// so nothing is written to disk. It writes the error response itself.
func (h *AdminCatalogHandler) readPhotoForm(c *gin.Context) (view string, body []byte, ok bool) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, h.maxUpload+multipartSlack)
	mr, err := c.Request.MultipartReader()
	if err != nil {
		writeError(c, http.StatusUnsupportedMediaType, "Send the photo as multipart/form-data.")
		return "", nil, false
	}
	const oneOfEach = "Send one view and one file."
	views, files := 0, 0
	for {
		p, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			h.writeFormError(c, err)
			return "", nil, false
		}
		switch p.FormName() {
		case "view":
			views++
			b, err := io.ReadAll(io.LimitReader(p, 32))
			if err != nil {
				h.writeFormError(c, err)
				return "", nil, false
			}
			view = strings.TrimSpace(string(b))
		case "file":
			files++
			b, err := io.ReadAll(io.LimitReader(p, h.maxUpload+1))
			if err != nil {
				h.writeFormError(c, err)
				return "", nil, false
			}
			if int64(len(b)) > h.maxUpload {
				writeError(c, http.StatusRequestEntityTooLarge, "Photos are at most "+sizeText(h.maxUpload)+".")
				return "", nil, false
			}
			body = b
		default:
			writeError(c, http.StatusBadRequest, oneOfEach)
			return "", nil, false
		}
		_ = p.Close()
	}
	// Read to the end (still capped by MaxBytesReader) so the reply never races the upload.
	_, _ = io.Copy(io.Discard, c.Request.Body)
	switch {
	case views != 1 || files != 1:
		writeError(c, http.StatusBadRequest, oneOfEach)
	case len(body) == 0:
		writeError(c, http.StatusBadRequest, "The file is empty.")
	default:
		if _, known := catalog.PhotoRank(view); known {
			return view, body, true
		}
		writeError(c, http.StatusBadRequest, "Unknown photo view.")
	}
	return "", nil, false
}

func (h *AdminCatalogHandler) writeFormError(c *gin.Context, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(c, http.StatusRequestEntityTooLarge, "Photos are at most "+sizeText(h.maxUpload)+".")
		return
	}
	writeError(c, http.StatusBadRequest, ErrMsgBadRequest)
}

// sizeText is a byte limit as people read it: "5 MB", or "512 KB" below 1 MB.
func sizeText(n int64) string {
	if n >= 1<<20 {
		return strconv.FormatInt(n>>20, 10) + " MB"
	}
	return strconv.FormatInt(n>>10, 10) + " KB"
}
