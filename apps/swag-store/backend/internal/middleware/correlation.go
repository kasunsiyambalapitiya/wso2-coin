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
	"regexp"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/reqctx"
)

// CorrelationIDHeader is read from the request, echoed on the response and
// forwarded to every upstream.
const CorrelationIDHeader = "X-Correlation-ID"

// safeCorrelationID bounds what a caller may send, so the value cannot break
// a log line or a header.
var safeCorrelationID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// CorrelationID reads X-Correlation-ID, or makes a UUIDv4 when it is missing or
// unsafe, stores it in the request context and echoes it on the response.
func CorrelationID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(CorrelationIDHeader)
		if !safeCorrelationID.MatchString(id) {
			id = uuid.NewString()
		}
		c.Writer.Header().Set(CorrelationIDHeader, id)
		c.Request = c.Request.WithContext(reqctx.WithCorrelationID(c.Request.Context(), id))
		c.Next()
	}
}
