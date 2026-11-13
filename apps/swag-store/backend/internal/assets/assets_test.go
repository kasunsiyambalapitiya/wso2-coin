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

package assets

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

type recorded struct {
	method, path string
	header       http.Header
	body         []byte
}

// fakeBlob stands in for Blob storage: it records every request and answers
// with the status the test set for that path (201 by default).
func fakeBlob(t *testing.T, status map[string]int) (*httptest.Server, func() []recorded) {
	t.Helper()
	var mu sync.Mutex
	var reqs []recorded
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		reqs = append(reqs, recorded{r.Method, r.URL.Path, r.Header.Clone(), b})
		mu.Unlock()
		code := http.StatusCreated
		if s, ok := status[r.URL.Path]; ok {
			code = s
		}
		switch code {
		case http.StatusConflict:
			w.Header().Set("x-ms-error-code", "BlobAlreadyExists")
		case http.StatusInternalServerError:
			w.Header().Set("x-ms-error-code", "InternalError")
		}
		w.WriteHeader(code)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []recorded {
		mu.Lock()
		defer mu.Unlock()
		return append([]recorded(nil), reqs...)
	}
}

func TestPhotoUploadPut(t *testing.T) {
	const id = "0192f3c4-7d1e-7a40-9b2c-5e8f1a2b3c4d"
	clash := "products/" + id + "/back-00000001.webp"
	broken := "products/" + id + "/back-00000002.webp"
	srv, reqs := fakeBlob(t, map[string]int{
		"/devstoreaccount1/swag-store-assets/" + clash:  http.StatusConflict,
		"/devstoreaccount1/swag-store-assets/" + broken: http.StatusInternalServerError,
	})
	c := newSharedKeyClient(t, srv.URL+"/devstoreaccount1", "swag-store-assets")
	body := []byte("RIFF\x00\x00\x00\x00WEBPVP8 photo")

	path := "products/" + id + "/back-3f9a2c1d.webp"
	if err := c.Put(context.Background(), path, body, "image/webp"); err != nil {
		t.Fatal(err)
	}
	got := reqs()
	if len(got) != 1 {
		t.Fatalf("requests = %d", len(got))
	}
	r := got[0]
	assertEq(t, r.method, http.MethodPut)
	assertEq(t, r.path, "/devstoreaccount1/swag-store-assets/"+path)
	assertEq(t, r.header.Get("If-None-Match"), "*")
	assertEq(t, r.header.Get("x-ms-blob-type"), "BlockBlob")
	assertEq(t, r.header.Get("x-ms-blob-content-type"), "image/webp")
	assertEq(t, r.header.Get("x-ms-blob-cache-control"), "public, max-age=86400")
	if !bytes.Equal(r.body, body) {
		t.Fatal("body changed on the way")
	}

	if err := c.Put(context.Background(), clash, body, "image/webp"); !errors.Is(err, ErrExists) {
		t.Fatalf("clash = %v", err)
	}
	err := c.Put(context.Background(), broken, body, "image/webp")
	if err == nil || errors.Is(err, ErrExists) {
		t.Fatalf("500 = %v", err)
	}
	if strings.Contains(err.Error(), "?") || strings.Contains(err.Error(), "sig=") || strings.Contains(err.Error(), srv.URL) {
		t.Fatalf("error leaks the request URL: %v", err)
	}
	if n := len(reqs()); n != 3 {
		t.Fatalf("retried: %d requests", n)
	}
}

func TestEnsureContainer(t *testing.T) {
	srv, reqs := fakeBlob(t, nil)
	c := newSharedKeyClient(t, srv.URL+"/devstoreaccount1", "swag-store-assets")
	if err := c.EnsureContainer(context.Background()); err != nil {
		t.Fatal(err)
	}
	r := reqs()[0]
	assertEq(t, r.method, http.MethodPut)
	assertEq(t, r.path, "/devstoreaccount1/swag-store-assets")
}

func TestDescribeKeepsReasonsNotQueries(t *testing.T) {
	ue := &url.Error{Op: "Put", URL: "https://acc.blob.core.windows.net/c/p.jpg?sv=1&sig=SECRET",
		Err: errors.New("dial tcp: lookup acc.blob.core.windows.net: no such host")}
	got := describe("upload", ue).Error()
	if !strings.Contains(got, "no such host") || strings.Contains(got, "SECRET") || strings.Contains(got, "sig=") {
		t.Fatalf("url error = %q", got)
	}
	auth := errors.New("ClientSecretCredential authentication failed\nPOST https://login.microsoftonline.com/t/oauth2/v2.0/token?a=b\nAADSTS7000215: Invalid client secret provided.")
	got = describe("delegation key", auth).Error()
	if !strings.Contains(got, "AADSTS7000215") || strings.Contains(got, "?a=b") {
		t.Fatalf("auth error = %q", got)
	}
	timeout := describe("upload", &url.Error{Op: "Put", URL: "https://x/y?sig=SECRET", Err: context.DeadlineExceeded})
	if !errors.Is(timeout, context.DeadlineExceeded) || strings.Contains(timeout.Error(), "SECRET") {
		t.Fatalf("timeout = %v", timeout)
	}
}
