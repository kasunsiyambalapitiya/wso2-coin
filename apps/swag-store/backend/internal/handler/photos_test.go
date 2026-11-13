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
	"bytes"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/assets"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/catalog"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/reqctx"
)

var (
	jpegBytes = append([]byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00"), bytes.Repeat([]byte{0x11}, 64)...)
	pngBytes  = append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), bytes.Repeat([]byte{0x22}, 64)...)
	webpBytes = append([]byte("RIFF\x40\x00\x00\x00WEBPVP8 "), bytes.Repeat([]byte{0x33}, 64)...)
)

type part struct {
	name, filename, contentType string
	body                        []byte
}

func formBody(t *testing.T, parts ...part) (body, contentType string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, p := range parts {
		h := textproto.MIMEHeader{}
		if p.filename != "" {
			h.Set("Content-Disposition", `form-data; name="`+p.name+`"; filename="`+p.filename+`"`)
			h.Set("Content-Type", p.contentType)
		} else {
			h.Set("Content-Disposition", `form-data; name="`+p.name+`"`)
		}
		pw, err := w.CreatePart(h)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = pw.Write(p.body)
	}
	_ = w.Close()
	return buf.String(), w.FormDataContentType()
}

func view(v string) part { return part{name: "view", body: []byte(v)} }
func file(b []byte) part {
	return part{name: "file", filename: "photo.png", contentType: "image/png", body: b}
}

// uploadRouter mounts UploadImage and reports whether the request was ever
// parsed into a MultipartForm (which spills large parts to disk).
func uploadRouter(h *AdminCatalogHandler, parsed *bool) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(reqctx.WithUser(c.Request.Context(), catalogAdmin))
		c.Next()
		if parsed != nil {
			// MultipartReader leaves an empty sentinel form; ParseMultipartForm
			// would have filled File (and spilled big parts to temp files).
			f := c.Request.MultipartForm
			*parsed = f != nil && (len(f.File) > 0 || len(f.Value) > 0)
		}
	})
	r.POST("/admin/products/:id/images", h.UploadImage)
	return r
}

func upload(r *gin.Engine, body, contentType string) *httptest.ResponseRecorder {
	return do(r, http.MethodPost, "/admin/products/"+pid+"/images", body, "Content-Type", contentType)
}

func TestPhotoUploadMultipart(t *testing.T) {
	f := &fakeAdminCatalog{detail: activeDetail()}
	photos := &fakePhotos{}
	r := uploadRouter(NewAdminCatalogHandler(f, NewListCache(), photos, 1024), nil)

	if rec := upload(r, `{"view":"front"}`, "application/json"); rec.Code != 415 {
		t.Errorf("json = %d", rec.Code)
	}
	for name, parts := range map[string][]part{
		"only file":   {file(pngBytes)},
		"only view":   {view("front")},
		"two files":   {view("front"), file(pngBytes), file(pngBytes)},
		"extra field": {view("front"), file(pngBytes), {name: "x", body: []byte("1")}},
		"empty file":  {view("front"), file(nil)},
		"bad view":    {view("side"), file(pngBytes)},
	} {
		body, ct := formBody(t, parts...)
		if rec := upload(r, body, ct); rec.Code != 400 {
			t.Errorf("%s = %d %s", name, rec.Code, rec.Body)
		}
	}
	body, ct := formBody(t, view("front"), file(append(pngBytes, bytes.Repeat([]byte{0}, 1024)...)))
	if rec := upload(r, body, ct); rec.Code != 413 || !strings.Contains(rec.Body.String(), "Photos are at most") {
		t.Errorf("too large = %d %s", rec.Code, rec.Body)
	}
	if len(photos.puts) != 0 || len(f.uploads) != 0 {
		t.Fatalf("a refused upload reached storage: %v %v", photos.puts, f.uploads)
	}

	// Happy path: a WebP for the back view.
	body, ct = formBody(t, view("back"), file(webpBytes))
	rec := upload(r, body, ct)
	got := decode[struct {
		View, Path string
		URL        *string
	}](t, rec)
	if rec.Code != 201 || got.View != "back" || !catalog.ValidPhotoPath(pid, "back", got.Path) || !strings.HasSuffix(got.Path, ".webp") ||
		got.URL == nil || *got.URL != "https://acc.blob.core.windows.net/c/"+got.Path+"?sig=x" {
		t.Fatalf("upload = %d %s", rec.Code, rec.Body)
	}
	if len(f.uploads) != 1 || f.uploads[0].ContentType != "image/webp" || f.uploads[0].Bytes != len(webpBytes) || f.uploads[0].Path != got.Path {
		t.Fatalf("audit = %+v", f.uploads)
	}

	// A name clash draws a new name once; two clashes give up.
	photos.putErrs = []error{assets.ErrExists}
	body, ct = formBody(t, view("top"), file(pngBytes))
	if rec := upload(r, body, ct); rec.Code != 201 || len(photos.puts) != 3 || photos.puts[1] == photos.puts[2] {
		t.Fatalf("one clash = %d %v", rec.Code, photos.puts)
	}
	photos.putErrs = []error{assets.ErrExists, assets.ErrExists}
	body, ct = formBody(t, view("top"), file(pngBytes))
	if rec := upload(r, body, ct); rec.Code != 503 || decode[errorBody](t, rec).ErrorCode != CodeAssetsUnavailable {
		t.Fatalf("two clashes = %d %s", rec.Code, rec.Body)
	}

	photos.putErrs = []error{assets.ErrNotConfigured}
	body, ct = formBody(t, view("top"), file(pngBytes))
	rec = upload(r, body, ct)
	if rec.Code != 503 || decode[errorBody](t, rec).ErrorCode != CodeAssetsNotConfigured || !strings.Contains(rec.Body.String(), "Photos aren't set up in this environment.") {
		t.Fatalf("not configured = %d %s", rec.Code, rec.Body)
	}
	photos.putErrs = []error{errors.New("boom")}
	body, ct = formBody(t, view("top"), file(pngBytes))
	rec = upload(r, body, ct)
	if rec.Code != 503 || decode[errorBody](t, rec).ErrorCode != CodeAssetsUnavailable ||
		!strings.Contains(rec.Body.String(), "Photo storage can't be reached right now. Try again in a minute.") {
		t.Fatalf("storage down = %d %s", rec.Code, rec.Body)
	}

	// The product must exist and not be removed.
	f.detail.Product.Status = "REMOVED"
	body, ct = formBody(t, view("front"), file(pngBytes))
	rec = upload(r, body, ct)
	if rec.Code != 409 || decode[errorBody](t, rec).ErrorCode != CodeStateConflict ||
		!strings.Contains(rec.Body.String(), "This product was removed. Restore it to change its photos.") {
		t.Fatalf("removed = %d %s", rec.Code, rec.Body)
	}
	f.detail = nil
	if rec := upload(r, body, ct); rec.Code != 404 {
		t.Fatalf("missing = %d", rec.Code)
	}
}

func TestPhotoUploadNeverParsesToDisk(t *testing.T) {
	var parsed bool
	h := NewAdminCatalogHandler(&fakeAdminCatalog{detail: activeDetail()}, NewListCache(), &fakePhotos{}, 5<<20)
	body, ct := formBody(t, view("front"), file(append(pngBytes, bytes.Repeat([]byte{7}, 4<<20)...)))
	if rec := upload(uploadRouter(h, &parsed), body, ct); rec.Code != 201 || parsed {
		t.Fatalf("4 MiB upload = %d, parsed into a MultipartForm: %v", rec.Code, parsed)
	}
}

func TestPhotoUploadSniffsType(t *testing.T) {
	h := NewAdminCatalogHandler(&fakeAdminCatalog{detail: activeDetail()}, NewListCache(), &fakePhotos{}, 5<<20)
	r := uploadRouter(h, nil)
	for want, b := range map[string][]byte{"jpg": jpegBytes, "png": pngBytes, "webp": webpBytes} {
		body, ct := formBody(t, view("front"), file(b)) // the part always says image/png
		rec := upload(r, body, ct)
		if rec.Code != 201 || !strings.HasSuffix(decode[struct{ Path string }](t, rec).Path, "."+want) {
			t.Errorf("%s = %d %s", want, rec.Code, rec.Body)
		}
	}
	for name, b := range map[string][]byte{
		"svg":  []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`),
		"html": []byte("<html><body>hi</body></html>"),
		"gif":  []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00"),
		"heic": []byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00mif1heic"),
	} {
		body, ct := formBody(t, view("front"), file(b))
		rec := upload(r, body, ct)
		if rec.Code != 415 || decode[errorBody](t, rec).ErrorCode != CodeUnsupportedImage ||
			!strings.Contains(rec.Body.String(), "Photos must be JPEG, PNG or WebP.") {
			t.Errorf("%s = %d %s", name, rec.Code, rec.Body)
		}
	}
}

func TestPhotoUploadRefusedWithoutSigningKey(t *testing.T) {
	photos := &fakePhotos{health: "key_unavailable"}
	h := NewAdminCatalogHandler(&fakeAdminCatalog{detail: activeDetail()}, NewListCache(), photos, 5<<20)
	body, ct := formBody(t, view("front"), file(pngBytes))
	rec := upload(uploadRouter(h, nil), body, ct)
	if rec.Code != 503 || decode[errorBody](t, rec).ErrorCode != CodeAssetsUnavailable || len(photos.puts) != 0 {
		t.Fatalf("no signing key = %d %s, puts %v", rec.Code, rec.Body, photos.puts)
	}
	off := NewAdminCatalogHandler(&fakeAdminCatalog{detail: activeDetail()}, NewListCache(), &fakePhotos{off: true}, 5<<20)
	if rec := upload(uploadRouter(off, nil), body, ct); rec.Code != 503 || decode[errorBody](t, rec).ErrorCode != CodeAssetsNotConfigured {
		t.Fatalf("photos off = %d %s", rec.Code, rec.Body)
	}
}

// eofReader reports whether the handler read the whole upload.
type eofReader struct {
	r    io.Reader
	done bool
}

func (e *eofReader) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err == io.EOF {
		e.done = true
	}
	return n, err
}

// A refusal sent before the upload is read can reach the browser as a reset
// connection instead of the message, so the form is read first.
func TestPhotoUploadReadsBodyBeforeRefusing(t *testing.T) {
	d := activeDetail()
	d.Product.Status = "REMOVED"
	h := NewAdminCatalogHandler(&fakeAdminCatalog{detail: d}, NewListCache(), &fakePhotos{}, 5<<20)
	body, ct := formBody(t, view("front"), file(append(pngBytes, bytes.Repeat([]byte{9}, 2<<20)...)))
	src := &eofReader{r: strings.NewReader(body)}
	req := httptest.NewRequest(http.MethodPost, "/admin/products/"+pid+"/images", src)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	uploadRouter(h, nil).ServeHTTP(rec, req)
	if rec.Code != 409 || !src.done {
		t.Fatalf("removed product = %d, upload read to the end: %v", rec.Code, src.done)
	}
}
