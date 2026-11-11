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

package middleware

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/reqctx"
)

func init() { gin.SetMode(gin.TestMode) }

func serve(r *gin.Engine, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	r := gin.New()
	r.Use(SecurityHeaders())
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	for _, path := range []string{"/x", "/missing"} {
		rec := serve(r, httptest.NewRequest(http.MethodGet, path, nil))
		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s: X-Content-Type-Options = %q", path, got)
		}
		if got := rec.Header().Get("Content-Security-Policy"); got != "upgrade-insecure-requests" {
			t.Errorf("%s: Content-Security-Policy = %q", path, got)
		}
		if got := rec.Header().Get("Strict-Transport-Security"); got != "max-age=31536000; includeSubDomains" {
			t.Errorf("%s: Strict-Transport-Security = %q", path, got)
		}
	}
}

func TestCorrelationIDGeneratedEchoedAndStored(t *testing.T) {
	r := gin.New()
	r.Use(CorrelationID())
	var seen string
	r.GET("/x", func(c *gin.Context) {
		seen = reqctx.CorrelationID(c.Request.Context())
		c.Status(http.StatusNoContent)
	})

	rec := serve(r, httptest.NewRequest(http.MethodGet, "/x", nil))
	echoed := rec.Header().Get(CorrelationIDHeader)
	if len(echoed) != 36 || seen != echoed {
		t.Fatalf("generated id: echoed %q, in context %q", echoed, seen)
	}

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(CorrelationIDHeader, "abc-123")
	rec = serve(r, req)
	if got := rec.Header().Get(CorrelationIDHeader); got != "abc-123" || seen != "abc-123" {
		t.Fatalf("incoming id not kept: echoed %q, context %q", got, seen)
	}
}

func TestCorrelationIDRejectsUnsafeValue(t *testing.T) {
	r := gin.New()
	r.Use(CorrelationID())
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(CorrelationIDHeader, "bad value\nlevel=ERROR")
	rec := serve(r, req)
	if got := rec.Header().Get(CorrelationIDHeader); strings.ContainsAny(got, " \n") || len(got) != 36 {
		t.Fatalf("unsafe id was kept: %q", got)
	}
}

func TestCORSFailsClosed(t *testing.T) {
	r := gin.New()
	r.Use(CORS(nil))
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec := serve(r, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("empty allow list set Access-Control-Allow-Origin = %q", got)
	}

	pre := httptest.NewRequest(http.MethodOptions, "/x", nil)
	pre.Header.Set("Origin", "https://evil.example")
	pre.Header.Set("Access-Control-Request-Method", "POST")
	rec = serve(r, pre)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("preflight from unknown origin = %d, want 403", rec.Code)
	}
}

func TestCORSAllowedOrigin(t *testing.T) {
	r := gin.New()
	r.Use(CORS([]string{"http://localhost:3000"}))
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	pre := httptest.NewRequest(http.MethodOptions, "/x", nil)
	pre.Header.Set("Origin", "http://localhost:3000")
	pre.Header.Set("Access-Control-Request-Method", "POST")
	rec := serve(r, pre)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:3000" {
		t.Fatalf("Access-Control-Allow-Origin = %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Fatalf("Access-Control-Allow-Credentials must never be set, got %q", got)
	}
	if !strings.Contains(strings.ToLower(rec.Header().Get("Access-Control-Allow-Headers")), "idempotency-key") {
		t.Fatalf("Idempotency-Key not allowed: %q", rec.Header().Get("Access-Control-Allow-Headers"))
	}
}

func TestLoggerWritesOneLinePerRequestWithIDs(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(NewContextHandler(slog.NewTextHandler(&buf, nil))))
	defer slog.SetDefault(prev)

	r := gin.New()
	r.Use(CorrelationID(), func(c *gin.Context) {
		ctx := reqctx.WithUser(c.Request.Context(), &reqctx.User{Email: "you@wso2.com", UserID: "u-1"})
		c.Request = c.Request.WithContext(ctx)
	}, Logger())
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusTeapot) })

	serve(r, httptest.NewRequest(http.MethodGet, "/x", nil))
	out := buf.String()
	for _, want := range []string{"request completed", "method=GET", "path=/x", "status=418", "userID=u-1", "correlationID="} {
		if !strings.Contains(out, want) {
			t.Errorf("log line missing %q: %s", want, out)
		}
	}
	if strings.Contains(out, "you@wso2.com") {
		t.Errorf("log line leaked the email: %s", out)
	}
}

func TestRecoveryReturnsFixedJSON500(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	r := gin.New()
	r.Use(Recovery())
	r.GET("/x", func(c *gin.Context) { panic("boom secret detail") })

	rec := serve(r, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "boom") || !strings.Contains(rec.Body.String(), `"message"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if !strings.Contains(buf.String(), "panic") {
		t.Fatalf("panic not logged: %s", buf.String())
	}
}

func TestRateLimitPerUser(t *testing.T) {
	l := NewLimiter()
	r := gin.New()
	user := "u-1"
	r.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(reqctx.WithUser(c.Request.Context(), &reqctx.User{UserID: user}))
	})
	r.POST("/orders", l.Limit("orders", 2), func(c *gin.Context) { c.Status(http.StatusCreated) })

	codes := []int{}
	for i := 0; i < 3; i++ {
		codes = append(codes, serve(r, httptest.NewRequest(http.MethodPost, "/orders", nil)).Code)
	}
	if codes[0] != 201 || codes[1] != 201 || codes[2] != http.StatusTooManyRequests {
		t.Fatalf("codes = %v, want [201 201 429]", codes)
	}
	rec := serve(r, httptest.NewRequest(http.MethodPost, "/orders", nil))
	if !strings.Contains(rec.Body.String(), `"errorCode":"rate_limited"`) {
		t.Fatalf("429 body = %s", rec.Body.String())
	}

	user = "u-2"
	if code := serve(r, httptest.NewRequest(http.MethodPost, "/orders", nil)).Code; code != 201 {
		t.Fatalf("another user was limited: %d", code)
	}
}

func TestContextHandlerAddsIDs(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(NewContextHandler(slog.NewTextHandler(&buf, nil)))
	ctx := reqctx.WithCorrelationID(context.Background(), "cid-1")
	ctx = reqctx.WithUser(ctx, &reqctx.User{UserID: "u-9"})
	log.InfoContext(ctx, "hello")
	if !strings.Contains(buf.String(), "correlationID=cid-1") || !strings.Contains(buf.String(), "userID=u-9") {
		t.Fatalf("ids missing: %s", buf.String())
	}
}
