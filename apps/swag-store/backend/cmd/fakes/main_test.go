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

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestListenAddr(t *testing.T) {
	got, err := listenAddr(env(nil))
	if err != nil || got != "127.0.0.1:8081" {
		t.Fatalf("default = %q, %v; want 127.0.0.1:8081", got, err)
	}
	for _, ok := range []string{"127.0.0.1:9000", "127.0.0.2:9000", "localhost:8081", "[::1]:8081"} {
		if got, err := listenAddr(env(map[string]string{"FAKES_ADDR": ok})); err != nil || got != ok {
			t.Errorf("%q: got %q, %v; want accepted", ok, got, err)
		}
	}
}

func TestListenAddrRefusesNonLoopback(t *testing.T) {
	for _, bad := range []string{":8081", "0.0.0.0:8081", "[::]:8081", "10.0.0.5:8081", "192.168.1.2:80", "example.com:8081", "127.0.0.1", "nonsense"} {
		if got, err := listenAddr(env(map[string]string{"FAKES_ADDR": bad})); err == nil {
			t.Errorf("%q: accepted as %q, want refused", bad, got)
		}
	}
}

func TestTreasuryAddress(t *testing.T) {
	if got := treasuryAddress(env(nil)); got != "0xfa4e00000000000000000000000000000000beef" {
		t.Errorf("default = %q", got)
	}
	if got := treasuryAddress(env(map[string]string{"FAKE_TREASURY_ADDRESS": "0xabc"})); got != "0xabc" {
		t.Errorf("from env = %q", got)
	}
}

func TestTokenEndpoint(t *testing.T) {
	srv := newTestServer(t)
	resp, err := srv.Client().Post(srv.URL+"/token", "application/x-www-form-urlencoded", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("status %d, Content-Type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	status, body := call(t, srv, http.MethodPost, "/token", nil, "")
	tok := decode[map[string]any](t, body)
	if status != http.StatusOK || tok["access_token"] != "fake-token" || tok["token_type"] != "Bearer" || tok["expires_in"] != float64(3600) {
		t.Errorf("token = %s", body)
	}
}

func TestHealth(t *testing.T) {
	srv := newTestServer(t)
	if status, _ := call(t, srv, http.MethodGet, "/health", nil, ""); status != http.StatusOK {
		t.Errorf("status = %d", status)
	}
}
