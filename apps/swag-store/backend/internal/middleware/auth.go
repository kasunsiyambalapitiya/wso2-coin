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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/reqctx"
)

// JWTAssertionHeader is the header the Choreo gateway sets with the caller's token.
const JWTAssertionHeader = "x-jwt-assertion"

// errMsgUnauthorized is the one message every 401 carries, whatever the reason.
const errMsgUnauthorized = "You are not authorized to perform this action. Please try again."

// validMethods are the signing algorithms the store accepts. "none" and the
// HMAC family are never accepted.
var validMethods = []string{"RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384", "ES512"}

// AuthConfig holds the token checks.
type AuthConfig struct {
	Issuer           string
	Audiences        []string // the token must carry at least one
	ClockSkew        time.Duration
	ValidatorEnabled bool // false = decode only, for local runs (startup enforces loopback)
}

type jwtClaims struct {
	Email     string     `json:"email"`
	UserID    string     `json:"userid"`
	FirstName string     `json:"given_name"`
	LastName  string     `json:"family_name"`
	Groups    stringList `json:"groups"`
	jwt.RegisteredClaims
}

// stringList accepts a claim given as one string or as an array of strings.
type stringList []string

func (l *stringList) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		if one == "" {
			*l = nil
		} else {
			*l = []string{one}
		}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return fmt.Errorf("claim must be a string or an array of strings: %w", err)
	}
	*l = many
	return nil
}

// NewJWKSKeyfunc loads the JWKS at endpoint through x5cStrippingTransport and
// keeps it refreshed. It is called once at startup; an error is fatal there.
func NewJWKSKeyfunc(ctx context.Context, endpoint string) (jwt.Keyfunc, error) {
	client := &http.Client{
		Timeout:   10 * time.Second,
		Transport: &x5cStrippingTransport{base: http.DefaultTransport},
	}
	k, err := keyfunc.NewDefaultOverrideCtx(ctx, []string{endpoint}, keyfunc.Override{Client: client})
	if err != nil {
		return nil, fmt.Errorf("load JWKS from %s: %w", endpoint, err)
	}
	return k.Keyfunc, nil
}

// Auth validates x-jwt-assertion on every request except GET /health and
// GET /health/dependencies. It stores the user and the raw assertion in the
// request context. Every 401 carries the same message; the reason is logged.
func Auth(cfg AuthConfig, keyFunc jwt.Keyfunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method == http.MethodGet &&
			(c.Request.URL.Path == "/health" || c.Request.URL.Path == "/health/dependencies") {
			c.Next()
			return
		}
		token := strings.TrimSpace(c.GetHeader(JWTAssertionHeader))
		if token == "" {
			abortError(c, http.StatusUnauthorized, errMsgUnauthorized, "")
			return
		}
		user, err := parseToken(token, cfg, keyFunc)
		if err != nil {
			slog.WarnContext(c.Request.Context(), "auth: token validation failed", "err", err)
			abortError(c, http.StatusUnauthorized, errMsgUnauthorized, "")
			return
		}
		ctx := reqctx.WithUser(c.Request.Context(), user)
		ctx = reqctx.WithUserAssertion(ctx, token)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

func parseToken(token string, cfg AuthConfig, keyFunc jwt.Keyfunc) (*reqctx.User, error) {
	var c jwtClaims
	if !cfg.ValidatorEnabled {
		if _, _, err := jwt.NewParser().ParseUnverified(token, &c); err != nil {
			return nil, fmt.Errorf("decode token: %w", err)
		}
	} else {
		if keyFunc == nil {
			return nil, errors.New("no key function configured")
		}
		t, err := jwt.ParseWithClaims(token, &c, keyFunc,
			jwt.WithValidMethods(validMethods),
			jwt.WithIssuer(cfg.Issuer),
			jwt.WithLeeway(cfg.ClockSkew),
			jwt.WithExpirationRequired(),
		)
		if err != nil {
			return nil, fmt.Errorf("validate token: %w", err)
		}
		if !t.Valid {
			return nil, errors.New("invalid token")
		}
		if !hasAnyAudience(c.Audience, cfg.Audiences) {
			return nil, errors.New("token audience not accepted")
		}
	}
	email := strings.ToLower(strings.TrimSpace(c.Email))
	if email == "" {
		return nil, errors.New("token missing email claim")
	}
	if strings.TrimSpace(c.UserID) == "" {
		return nil, errors.New("token missing userid claim")
	}
	return &reqctx.User{
		Email:     email,
		UserID:    c.UserID,
		FirstName: c.FirstName,
		LastName:  c.LastName,
		Groups:    []string(c.Groups),
	}, nil
}

func hasAnyAudience(got jwt.ClaimStrings, want []string) bool {
	for _, a := range got {
		if slices.Contains(want, a) {
			return true
		}
	}
	return false
}

// x5cStrippingTransport removes "x5c" from every key of a JWKS response before
// jwkset parses it. Verification needs only n/e (or the EC equivalents), and
// some IdPs (Asgardeo included) publish certificates with a negative serial,
// which crypto/x509 rejects; that would fail the whole key set.
type x5cStrippingTransport struct{ base http.RoundTripper }

func (t *x5cStrippingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return resp, err
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("read JWKS response body: %w", err)
	}
	var doc struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return resp, nil
	}
	for _, k := range doc.Keys {
		delete(k, "x5c")
	}
	clean, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("marshal sanitized JWKS: %w", err)
	}
	resp.Body = io.NopCloser(bytes.NewReader(clean))
	resp.ContentLength = int64(len(clean))
	resp.Header.Set("Content-Length", strconv.Itoa(len(clean)))
	return resp, nil
}
