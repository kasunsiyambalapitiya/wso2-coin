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
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/sas"
)

// azuriteKey is the storage emulator's published development key.
const azuriteKey = "Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw=="

const testPhoto = "products/0192f3c4-7d1e-7a40-9b2c-5e8f1a2b3c4d/front-3f9a2c1d.jpg"

func newSharedKeyClient(t *testing.T, accountURL, container string) *Client {
	t.Helper()
	c, err := New(Config{AccountURL: accountURL, Container: container, AccountKey: azuriteKey})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func parsedQuery(t *testing.T, raw string) url.Values {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query()
}

func assertEq(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func at(s string) func() time.Time {
	tm, _ := time.Parse(time.RFC3339, s)
	return func() time.Time { return tm }
}

func TestReadSASShape(t *testing.T) {
	c := newSharedKeyClient(t, "http://127.0.0.1:10000/devstoreaccount1", "swag-store-assets")
	c.now = at("2026-11-13T23:59:59Z")
	u := *c.ReadURL(testPhoto)
	if !strings.HasPrefix(u, "http://127.0.0.1:10000/devstoreaccount1/swag-store-assets/"+testPhoto+"?") {
		t.Fatalf("url = %s", u)
	}
	q := parsedQuery(t, u)
	assertEq(t, q.Get("sr"), "b") // this blob only
	assertEq(t, q.Get("sp"), "r")
	assertEq(t, q.Get("spr"), "https,http")
	assertEq(t, q.Get("st"), "2026-11-12T23:45:00Z")
	assertEq(t, q.Get("se"), "2026-11-15T00:00:00Z")
	if q.Get("sig") == "" {
		t.Fatal("no signature")
	}
	c.now = at("2026-11-13T00:00:01Z")
	assertEq(t, *c.ReadURL(testPhoto), u) // the same all day
	other := "products/0192f3c4-7d1e-7a40-9b2c-5e8f1a2b3c4d/back-0a1b2c3d.png"
	if parsedQuery(t, *c.ReadURL(other)).Get("sig") == q.Get("sig") {
		t.Fatal("two photos share a signature: a URL must read only its own blob")
	}
	c.now = at("2026-11-14T00:00:00Z")
	if next := *c.ReadURL(testPhoto); next == u {
		t.Fatal("the SAS must change at midnight")
	}
	for _, bad := range []string{"products/x/../front-3f9a2c1d.jpg", "", testPhoto + "?a=b"} {
		if c.ReadURL(bad) != nil {
			t.Errorf("signed %q", bad)
		}
	}
}

func TestReadSASHTTPSOnlyOffLoopback(t *testing.T) {
	c := newSharedKeyClient(t, "https://acc.blob.core.windows.net", "photos")
	c.now = at("2026-11-13T10:00:00Z")
	u := *c.ReadURL(testPhoto)
	if !strings.HasPrefix(u, "https://acc.blob.core.windows.net/photos/"+testPhoto+"?") {
		t.Fatalf("url = %s", u)
	}
	assertEq(t, parsedQuery(t, u).Get("spr"), "https")
}

func TestDisabledClient(t *testing.T) {
	d := Disabled()
	if d.ReadURL(testPhoto) != nil {
		t.Fatal("disabled client signed a URL")
	}
	if err := d.Put(context.Background(), testPhoto, []byte{1}, "image/png"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("put = %v", err)
	}
	assertEq(t, d.Health(), "not_configured")
	d.Start(context.Background()) // no-op, must not panic
}

func TestDelegationKeyRenewal(t *testing.T) {
	signer := newSharedKeyClient(t, "https://acc.blob.core.windows.net", "photos").sign
	var calls []struct{ start, expiry time.Time }
	fail := false
	c := &Client{accountURL: "https://acc.blob.core.windows.net", container: "photos", protocol: sas.ProtocolHTTPS,
		fetchKey: func(_ context.Context, start, expiry time.Time) (signFunc, error) {
			calls = append(calls, struct{ start, expiry time.Time }{start, expiry})
			if fail {
				return nil, errors.New("no role")
			}
			return signer, nil
		}}
	c.now = at("2026-11-13T10:00:00Z")
	assertEq(t, c.Health(), "key_unavailable")
	if c.ReadURL(testPhoto) != nil {
		t.Fatal("signed without a key")
	}

	c.refreshKey(context.Background())
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	assertEq(t, calls[0].start.Format(time.RFC3339), "2026-11-12T23:30:00Z")
	assertEq(t, calls[0].expiry.Format(time.RFC3339), "2026-11-20T09:55:00Z")
	assertEq(t, c.Health(), "ok")
	if c.ReadURL(testPhoto) == nil {
		t.Fatal("no URL with a key")
	}

	c.now = at("2026-11-16T10:00:00Z") // 4 days left: keep the key
	c.refreshKey(context.Background())
	if len(calls) != 1 {
		t.Fatalf("renewed too early: %d", len(calls))
	}
	c.now = at("2026-11-17T11:00:00Z") // under 72 h left: renew
	c.refreshKey(context.Background())
	if len(calls) != 2 {
		t.Fatalf("not renewed: %d", len(calls))
	}

	fail = true
	c.now = at("2026-11-27T00:00:00Z") // key long expired and renewal fails
	c.refreshKey(context.Background())
	assertEq(t, c.Health(), "key_unavailable")
	if c.ReadURL(testPhoto) != nil {
		t.Fatal("signed with an expired key")
	}
}
