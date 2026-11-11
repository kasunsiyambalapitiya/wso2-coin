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

// Package apierror defines the typed error that upstream clients return for a
// non-2xx response, so handlers can map the upstream status to their own.
package apierror

import (
	"errors"
	"fmt"
)

// MaxBody is the longest upstream body excerpt kept in an Error.
const MaxBody = 256

// Error is returned when an upstream service answers with a non-2xx status.
// Body is an excerpt for logs only; it is never returned to a caller.
type Error struct {
	StatusCode int
	Body       string
}

func (e *Error) Error() string {
	return fmt.Sprintf("upstream returned %d: %s", e.StatusCode, e.Body)
}

// New builds an Error from a status and a raw body, cut to MaxBody bytes.
func New(status int, body []byte) *Error {
	if len(body) > MaxBody {
		body = body[:MaxBody]
	}
	return &Error{StatusCode: status, Body: string(body)}
}

// Status returns the upstream status carried by err, or 0 when err is not an
// *Error (a network error or a timeout).
func Status(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.StatusCode
	}
	return 0
}
