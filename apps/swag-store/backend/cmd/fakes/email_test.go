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
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

type sentJSON struct {
	Subject string   `json:"subject"`
	To      []string `json:"to"`
	HTML    string   `json:"html"`
}

func TestSendEmailIsKept(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	srv := newTestServer(t)
	// encoding/json sends the []byte template as base64, as the real client does.
	status, body := call(t, srv, http.MethodPost, "/send-email", map[string]any{
		"to": []string{"a@wso2.com", "b@wso2.com"}, "cc": []string{"c@wso2.com"}, "from": "store@wso2.com",
		"subject": "Order SS-1", "template": []byte("<p>Hello</p>"),
	}, "")
	if status != http.StatusOK {
		t.Fatalf("send = %d %s", status, body)
	}
	if line := logs.String(); !strings.Contains(line, "Order SS-1") || !strings.Contains(line, "recipients=3") || strings.Contains(line, "@") {
		t.Errorf("log = %q, want subject and count only", line)
	}

	_, body = call(t, srv, http.MethodGet, "/sent", nil, "")
	got := decode[[]sentJSON](t, body)
	if len(got) != 1 || got[0].Subject != "Order SS-1" || got[0].HTML != "<p>Hello</p>" || strings.Join(got[0].To, ",") != "a@wso2.com,b@wso2.com" {
		t.Fatalf("sent = %s", body)
	}

	for i := range 60 {
		call(t, srv, http.MethodPost, "/send-email", map[string]any{
			"to": []string{"a@wso2.com"}, "from": "s@wso2.com", "subject": fmt.Sprint("n", i), "template": []byte("x"),
		}, "")
	}
	_, body = call(t, srv, http.MethodGet, "/sent", nil, "")
	got = decode[[]sentJSON](t, body)
	if len(got) != 50 || got[0].Subject != "n10" || got[49].Subject != "n59" {
		t.Errorf("kept %d, first %q, last %q; want the last 50", len(got), got[0].Subject, got[len(got)-1].Subject)
	}
}

func TestSendEmailRejectsBadBody(t *testing.T) {
	srv := newTestServer(t)
	if status, _ := call(t, srv, http.MethodPost, "/send-email", map[string]any{"to": []string{}, "subject": "S"}, ""); status != http.StatusBadRequest {
		t.Errorf("no recipients = %d, want 400", status)
	}
}

func TestSentStartsEmpty(t *testing.T) {
	srv := newTestServer(t)
	if _, body := call(t, srv, http.MethodGet, "/sent", nil, ""); strings.TrimSpace(string(body)) != "[]" {
		t.Errorf("= %s, want []", body)
	}
}
