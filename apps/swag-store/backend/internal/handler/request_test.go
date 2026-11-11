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
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/apierror"
)

func init() { gin.SetMode(gin.TestMode) }

type sampleBody struct {
	Name string `json:"name"`
}

func (b sampleBody) validate() error {
	if b.Name == "" {
		return errors.New("name is required.")
	}
	return nil
}

func decodeRouter() *gin.Engine {
	r := gin.New()
	r.POST("/x", func(c *gin.Context) {
		var b sampleBody
		if !readJSON(c, &b) {
			return
		}
		c.String(http.StatusOK, b.Name)
	})
	return r
}

func post(r *gin.Engine, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body)))
	return rec
}

func TestReadJSON(t *testing.T) {
	r := decodeRouter()
	cases := []struct {
		body     string
		status   int
		contains string
	}{
		{`{"name":"hoodie"}`, 200, "hoodie"},
		{`{"name":"hoodie","toAddress":"0xevil"}`, 400, `toAddress`},
		{`{"name":`, 400, "message"},
		{``, 400, "message"},
		{`{"name":"a"} {"name":"b"}`, 400, "message"},
		{`{}`, 400, "name is required."},
		{`{"name":"` + strings.Repeat("a", 1<<20) + `"}`, 413, ErrMsgTooLarge},
	}
	for _, tc := range cases {
		rec := post(r, tc.body)
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.contains) {
			t.Errorf("body %.40q: got %d %s, want %d containing %q", tc.body, rec.Code, rec.Body.String(), tc.status, tc.contains)
		}
	}
}

func TestUUIDParam(t *testing.T) {
	r := gin.New()
	r.GET("/x/:id", func(c *gin.Context) {
		id, ok := uuidParam(c, "id")
		if !ok {
			return
		}
		c.String(http.StatusOK, id)
	})
	for path, want := range map[string]int{
		"/x/0192f3a4-5b6c-7d8e-9f01-23456789abcd": 200,
		"/x/not-a-uuid":                          400,
		"/x/0192f3a4-5b6c-7d8e-9f01-23456789abc": 400,
	} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != want {
			t.Errorf("%s: %d, want %d", path, rec.Code, want)
		}
		if want == 400 && !strings.Contains(rec.Body.String(), ErrMsgInvalidUUID) {
			t.Errorf("%s: body %s", path, rec.Body)
		}
	}
}

func TestMapUpstreamErrorGenericNeverEchoes(t *testing.T) {
	cases := map[string]struct {
		err  error
		want int
	}{
		"bad gateway":  {&apierror.Error{StatusCode: 502, Body: "secret upstream detail"}, 503},
		"unavailable":  {&apierror.Error{StatusCode: 503, Body: "secret upstream detail"}, 503},
		"timeout":      {&apierror.Error{StatusCode: 504, Body: "secret upstream detail"}, 503},
		"not found":    {&apierror.Error{StatusCode: 404, Body: "secret upstream detail"}, 404},
		"bad request":  {&apierror.Error{StatusCode: 400, Body: "secret upstream detail"}, 500},
		"network":      {fmt.Errorf("dial: secret upstream detail"), 500},
		"server error": {&apierror.Error{StatusCode: 500, Body: "secret upstream detail"}, 500},
	}
	for name, tc := range cases {
		r := gin.New()
		r.GET("/x", func(c *gin.Context) { mapUpstreamErrorGeneric(c, tc.err, "Fallback.") })
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
		if rec.Code != tc.want {
			t.Errorf("%s: status %d, want %d", name, rec.Code, tc.want)
		}
		if strings.Contains(rec.Body.String(), "secret") {
			t.Errorf("%s: upstream body echoed: %s", name, rec.Body)
		}
	}
}
