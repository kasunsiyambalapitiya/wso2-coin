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
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/coins"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/hr"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/reqctx"
)

// shopper is the default signed-in user in handler tests.
var shopper = &reqctx.User{Email: "you@wso2.com", UserID: "u-1", FirstName: "Your", LastName: "Name"}

// testRouter mounts h on method/path with user injected into the context.
func testRouter(user *reqctx.User, method, path string, h gin.HandlerFunc) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if user != nil {
			c.Request = c.Request.WithContext(reqctx.WithUser(c.Request.Context(), user))
		}
	})
	r.Handle(method, path, h)
	return r
}

// fakePhotos signs every photo with a fixed query, or nothing when off.
type fakePhotos struct {
	off     bool
	health  string // what Health reports; "" means ok
	puts    []string
	putErrs []error // returned in order by Put; nil once used up
}

func (f *fakePhotos) ReadURL(path string) *string {
	if f.off {
		return nil
	}
	u := "https://acc.blob.core.windows.net/c/" + path + "?sig=x"
	return &u
}

func (f *fakePhotos) Health() string {
	switch {
	case f.health != "":
		return f.health
	case f.off:
		return "not_configured"
	}
	return "ok"
}

func (f *fakePhotos) Put(_ context.Context, path string, _ []byte, _ string) error {
	f.puts = append(f.puts, path)
	if len(f.putErrs) > 0 {
		err := f.putErrs[0]
		f.putErrs = f.putErrs[1:]
		return err
	}
	return nil
}

func do(r *gin.Engine, method, path, body string, headers ...string) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return v
}

type fakeEmployees struct {
	emp   *hr.Employee
	err   error
	calls int
}

func (f *fakeEmployees) Lookup(context.Context, string) (*hr.Employee, error) {
	f.calls++
	return f.emp, f.err
}

type fakeWallets struct {
	wallets []coins.Wallet
	err     error
}

func (f *fakeWallets) Wallets(context.Context) ([]coins.Wallet, error) { return f.wallets, f.err }

func rat(s string) *big.Rat {
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		panic(s)
	}
	return r
}

var errDown = errors.New("upstream down")

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
