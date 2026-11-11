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
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// maxBodyBytes caps every request body (larger → 413).
const maxBodyBytes = 1 << 20

// rawBodyKey holds the decoded body's bytes in the Gin context, for handlers
// that must tell a JSON null from an absent field.
const rawBodyKey = "swag.rawBody"

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// validator is implemented by request bodies with required fields. The error
// message names the field and is shown to the caller.
type validator interface{ validate() error }

// readJSON decodes the body into dst strictly: unknown fields, trailing data
// and bodies over 1 MiB are refused. It writes the 400/413 itself and returns
// false when the handler must stop.
func readJSON(c *gin.Context, dst any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBodyBytes)
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(c, http.StatusRequestEntityTooLarge, ErrMsgTooLarge)
			return false
		}
		writeError(c, http.StatusBadRequest, ErrMsgBadRequest)
		return false
	}
	c.Set(rawBodyKey, raw)
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(c, http.StatusBadRequest, decodeMessage(err))
		return false
	}
	if dec.More() {
		writeError(c, http.StatusBadRequest, ErrMsgBadRequest)
		return false
	}
	if v, ok := dst.(validator); ok {
		if err := v.validate(); err != nil {
			writeError(c, http.StatusBadRequest, err.Error())
			return false
		}
	}
	return true
}

// decodeMessage names the offending field for an unknown or mistyped field
// and falls back to the generic message otherwise.
func decodeMessage(err error) string {
	msg := err.Error()
	if strings.HasPrefix(msg, "json: unknown field ") {
		return "Unknown field " + strings.TrimPrefix(msg, "json: unknown field ") + "."
	}
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) && typeErr.Field != "" {
		return fmt.Sprintf("Field %q has the wrong type.", typeErr.Field)
	}
	return ErrMsgBadRequest
}

// uuidParam returns the named path param when it is a UUID; otherwise it writes 400.
func uuidParam(c *gin.Context, name string) (string, bool) {
	v := c.Param(name)
	if !uuidRe.MatchString(v) {
		writeError(c, http.StatusBadRequest, ErrMsgInvalidUUID)
		return "", false
	}
	return strings.ToLower(v), true
}

// paging reads limit (1..100, default 20) and offset (>= 0) from the query.
func paging(c *gin.Context) (limit, offset int, ok bool) {
	limit, offset = 20, 0
	if s := c.Query("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 100 {
			writeError(c, http.StatusBadRequest, "limit must be between 1 and 100.")
			return 0, 0, false
		}
		limit = n
	}
	if s := c.Query("offset"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			writeError(c, http.StatusBadRequest, "offset must be 0 or more.")
			return 0, 0, false
		}
		offset = n
	}
	return limit, offset, true
}
