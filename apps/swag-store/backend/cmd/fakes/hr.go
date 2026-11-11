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

package main

import (
	"net/http"
	"strings"
)

type employee struct {
	WorkEmail      string `json:"workEmail"`
	FirstName      string `json:"firstName"`
	LastName       string `json:"lastName"`
	EmployeeStatus string `json:"employeeStatus"`
	Location       string `json:"location"`
}

// handleGraphQL is the HR Entity GraphQL fake. It does not parse GraphQL:
// a query naming __typename is the health check, anything else is the
// employee lookup by variables.email.
func handleGraphQL(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query     string `json:"query"`
		Variables struct {
			Email string `json:"email"`
		} `json:"variables"`
	}
	if err := readJSON(r, 1<<20, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"errors": []map[string]string{{"message": "invalid request body"}}})
		return
	}
	if strings.Contains(req.Query, "__typename") {
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]string{"__typename": "Query"}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]*employee{"employee": fakeEmployee(req.Variables.Email)}})
}

// fakeEmployee returns an employee for any @wso2.com email, nil otherwise.
// The status comes from the local part: "leaver…" is Marked leaver, "left…"
// is Left, anything else Active.
func fakeEmployee(email string) *employee {
	email = strings.ToLower(strings.TrimSpace(email))
	local, ok := strings.CutSuffix(email, "@wso2.com")
	if !ok || local == "" || strings.Contains(local, "@") {
		return nil
	}
	status := "Active"
	switch {
	case strings.HasPrefix(local, "leaver"):
		status = "Marked leaver"
	case strings.HasPrefix(local, "left"):
		status = "Left"
	}
	names := strings.FieldsFunc(local, func(r rune) bool { return r == '.' || r == '_' || r == '-' || r == '+' })
	for i, n := range names {
		names[i] = strings.ToUpper(n[:1]) + n[1:]
	}
	e := &employee{WorkEmail: email, EmployeeStatus: status, Location: "Colombo"}
	if len(names) > 0 {
		e.FirstName = names[0]
		e.LastName = strings.Join(names[1:], " ")
	}
	return e
}
