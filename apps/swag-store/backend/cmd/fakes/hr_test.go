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
	"testing"
)

type employeeJSON struct {
	WorkEmail      string `json:"workEmail"`
	FirstName      string `json:"firstName"`
	LastName       string `json:"lastName"`
	EmployeeStatus string `json:"employeeStatus"`
	Location       string `json:"location"`
}

const employeeQuery = `query employee($email: String!) {
  employee(email: $email) { workEmail firstName lastName employeeStatus location } }`

func lookup(t *testing.T, email string) *employeeJSON {
	t.Helper()
	srv := newTestServer(t)
	status, body := call(t, srv, http.MethodPost, "/graphql",
		map[string]any{"query": employeeQuery, "variables": map[string]any{"email": email}}, "")
	if status != http.StatusOK {
		t.Fatalf("graphql = %d %s", status, body)
	}
	return decode[struct {
		Data struct {
			Employee *employeeJSON `json:"employee"`
		} `json:"data"`
	}](t, body).Data.Employee
}

func TestHRStatusRules(t *testing.T) {
	for email, want := range map[string]string{
		"jane.doe@wso2.com":     "Active",
		"leaver.bob@wso2.com":   "Marked leaver",
		"leaver@wso2.com":       "Marked leaver",
		"left.alice@wso2.com":   "Left",
		"lefty.k@wso2.com":      "Left",
		"Leaver.Upper@WSO2.com": "Marked leaver",
	} {
		e := lookup(t, email)
		if e == nil || e.EmployeeStatus != want || e.Location != "Colombo" {
			t.Errorf("%s = %+v, want status %q in Colombo", email, e, want)
		}
	}
	for _, email := range []string{"someone@example.com", "jane@wso2.com.evil.io", "@wso2.com", ""} {
		if e := lookup(t, email); e != nil {
			t.Errorf("%q = %+v, want null", email, e)
		}
	}
}

func TestHRNames(t *testing.T) {
	e := lookup(t, "jane.doe-smith@wso2.com")
	if e.WorkEmail != "jane.doe-smith@wso2.com" || e.FirstName != "Jane" || e.LastName != "Doe Smith" {
		t.Errorf("= %+v", e)
	}
	if e := lookup(t, "nimal@wso2.com"); e.FirstName != "Nimal" {
		t.Errorf("single-part = %+v", e)
	}
}

func TestHRHealthQuery(t *testing.T) {
	srv := newTestServer(t)
	status, body := call(t, srv, http.MethodPost, "/graphql", map[string]any{"query": "query { __typename }"}, "")
	if status != http.StatusOK || string(body) != `{"data":{"__typename":"Query"}}`+"\n" {
		t.Errorf("= %d %q", status, body)
	}
}
