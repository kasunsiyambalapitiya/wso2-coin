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
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/reqctx"
)

// contextHandler adds correlationID and userID from the context to every
// record logged with a *Context method. It never adds the email or name.
type contextHandler struct{ inner slog.Handler }

// NewContextHandler wraps inner with the correlationID and userID attributes.
func NewContextHandler(inner slog.Handler) slog.Handler { return contextHandler{inner: inner} }

func (h contextHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := reqctx.CorrelationID(ctx); id != "" {
		r.AddAttrs(slog.String("correlationID", id))
	}
	if u := reqctx.UserFrom(ctx); u != nil {
		r.AddAttrs(slog.String("userID", u.UserID))
	}
	return h.inner.Handle(ctx, r)
}

func (h contextHandler) WithAttrs(a []slog.Attr) slog.Handler {
	return contextHandler{inner: h.inner.WithAttrs(a)}
}

func (h contextHandler) WithGroup(n string) slog.Handler {
	return contextHandler{inner: h.inner.WithGroup(n)}
}

// ConfigureLogger makes text slog on stderr the default logger. It writes to
// os.Stderr directly: wrapping the old default handler would loop through
// log.std after slog.SetDefault. Call it once, first thing in main.
func ConfigureLogger() {
	slog.SetDefault(slog.New(NewContextHandler(slog.NewTextHandler(os.Stderr, nil))))
}

// Logger writes one "request completed" line per request. It runs after Auth,
// so the line carries both correlationID and userID.
func Logger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		slog.InfoContext(c.Request.Context(), "request completed",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"elapsed", time.Since(start).String(),
		)
	}
}
