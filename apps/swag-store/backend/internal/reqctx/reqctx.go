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

// Package reqctx carries per-request values in a context.Context: the signed-in
// user, the correlation id and the raw user assertion. It has no dependencies,
// so middleware, handlers, upstream clients and the slog handler can all share
// it without import cycles or a dependency on Gin.
package reqctx

import "context"

// User is the caller's identity, taken from the verified x-jwt-assertion.
type User struct {
	Email     string // lower-cased
	UserID    string
	FirstName string
	LastName  string
	Groups    []string // the token's groups claim; the only source of admin roles
}

type (
	userKey        struct{}
	correlationKey struct{}
	assertionKey   struct{}
)

// WithUser returns a copy of ctx that carries u.
func WithUser(ctx context.Context, u *User) context.Context {
	return context.WithValue(ctx, userKey{}, u)
}

// UserFrom returns the user stored in ctx, or nil.
func UserFrom(ctx context.Context) *User {
	u, _ := ctx.Value(userKey{}).(*User)
	return u
}

// WithCorrelationID returns a copy of ctx that carries id.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, correlationKey{}, id)
}

// CorrelationID returns the correlation id stored in ctx, or "".
func CorrelationID(ctx context.Context) string {
	v, _ := ctx.Value(correlationKey{}).(string)
	return v
}

// WithUserAssertion returns a copy of ctx that carries the caller's verified
// x-jwt-assertion. The coins client forwards it as X-User-Assertion.
func WithUserAssertion(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, assertionKey{}, token)
}

// UserAssertion returns the assertion stored in ctx, or "".
func UserAssertion(ctx context.Context) string {
	v, _ := ctx.Value(assertionKey{}).(string)
	return v
}
