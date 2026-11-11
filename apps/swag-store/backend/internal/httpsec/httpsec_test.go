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

package httpsec

import "testing"

func TestRequireSecureURL(t *testing.T) {
	cases := []struct {
		url string
		ok  bool
	}{
		{"https://api.example.com/base", true},
		{"https://api.example.com", true},
		{"http://localhost:9090", true},
		{"http://127.0.0.1:9090/x", true},
		{"http://[::1]:9090", true},
		{"http://api.example.com", false},
		{"http://localhost.example.com", false},
		{"ftp://api.example.com", false},
		{"https://", false},
		{"", false},
		{"not a url", false},
		{"https://user:pass@api.example.com", false},
	}
	for _, tc := range cases {
		err := RequireSecureURL(tc.url)
		if (err == nil) != tc.ok {
			t.Errorf("RequireSecureURL(%q) err = %v, want ok=%v", tc.url, err, tc.ok)
		}
	}
}

func TestIsLoopbackURL(t *testing.T) {
	cases := map[string]bool{
		"http://localhost:8081":   true,
		"http://127.0.0.1:8081":   true,
		"https://[::1]/x":         true,
		"https://api.example.com": false,
		"":                        false,
	}
	for url, want := range cases {
		if got := IsLoopbackURL(url); got != want {
			t.Errorf("IsLoopbackURL(%q) = %v, want %v", url, got, want)
		}
	}
}
