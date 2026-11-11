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

// Package middleware holds the Gin middleware chain, outermost first:
// SecurityHeaders → CORS → CorrelationID → Auth → Logger → Recovery.
package middleware

import (
	"github.com/gin-gonic/gin"
)

// SecurityHeaders sets the security headers on every response, including
// auth failures and 404s, because it runs first.
func SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", "upgrade-insecure-requests")
		h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		c.Next()
	}
}

// errorBody is the store's error payload. It matches handler.errorBody; the
// middleware keeps its own copy so it does not import the handler package.
type errorBody struct {
	Message   string `json:"message"`
	ErrorCode string `json:"errorCode,omitempty"`
}

func abortError(c *gin.Context, status int, message, code string) {
	c.AbortWithStatusJSON(status, errorBody{Message: message, ErrorCode: code})
}
