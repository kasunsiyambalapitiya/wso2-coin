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
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/gin-gonic/gin"
)

// Recovery turns a handler panic into a 500 with the fixed message. It is the
// innermost middleware, so Logger still records the 500. The panic value and
// stack go to the log only.
func Recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				slog.ErrorContext(c.Request.Context(), "panic in handler",
					"panic", fmt.Sprint(v), "stack", string(debug.Stack()))
				if !c.Writer.Written() {
					abortError(c, http.StatusInternalServerError, "An internal server error occurred. Please try again later.", "")
				} else {
					c.Abort()
				}
			}
		}()
		c.Next()
	}
}
