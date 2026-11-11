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

package notifications

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

type sent struct {
	to, cc, bcc, replyTo []string
	subject, html        string
	attachments          []EmailAttachment
}

type recordingSender struct{ got []sent }

func (r *recordingSender) SendEmail(_ context.Context, to, cc, bcc, replyTo []string, subject, htmlBody string, attachments []EmailAttachment) error {
	r.got = append(r.got, sent{to, cc, bcc, replyTo, subject, htmlBody, attachments})
	return nil
}

// captureLogs swaps the default slog logger for one writing to a buffer.
// Tests that use it must not run in parallel.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestDebugRedirect(t *testing.T) {
	logs := captureLogs(t)
	inner := &recordingSender{}
	s := WithDebugRedirect(inner, []string{" dev1@wso2.com", "", "dev2@wso2.com "})

	att := []EmailAttachment{{ContentName: "a.txt", ContentType: "text/plain", Attachment: []byte("x")}}
	err := s.SendEmail(context.Background(),
		[]string{"buyer@wso2.com"}, []string{"cc.person@wso2.com"}, []string{"<evil>@wso2.com"},
		[]string{"contact@wso2.com"}, "Your order", "<html><body style=\"x\"><p>Hello</p></body></html>", att)
	if err != nil {
		t.Fatalf("SendEmail: %v", err)
	}
	if len(inner.got) != 1 {
		t.Fatalf("sent = %d, want 1", len(inner.got))
	}
	got := inner.got[0]
	if strings.Join(got.to, ",") != "dev1@wso2.com,dev2@wso2.com" {
		t.Errorf("to = %v, want only the debug addresses", got.to)
	}
	if len(got.cc) != 0 || len(got.bcc) != 0 {
		t.Errorf("cc/bcc = %v / %v, want dropped", got.cc, got.bcc)
	}
	if strings.Join(got.replyTo, ",") != "contact@wso2.com" {
		t.Errorf("replyTo = %v, want kept", got.replyTo)
	}
	if got.subject != "[Test copy] Your order" {
		t.Errorf("subject = %q", got.subject)
	}
	if len(got.attachments) != 1 {
		t.Errorf("attachments = %d, want kept", len(got.attachments))
	}
	bodyAt := strings.Index(got.html, `<body style="x">`)
	noticeAt := strings.Index(got.html, "[Test copy]")
	helloAt := strings.Index(got.html, "<p>Hello</p>")
	if bodyAt < 0 || noticeAt < bodyAt || helloAt < noticeAt {
		t.Errorf("notice must sit just inside <body>, before the content:\n%s", got.html)
	}
	for _, want := range []string{"buyer@wso2.com", "cc.person@wso2.com", "&lt;evil&gt;@wso2.com"} {
		if !strings.Contains(got.html, want) {
			t.Errorf("notice does not name %q:\n%s", want, got.html)
		}
	}
	if strings.Contains(got.html, "<evil>") {
		t.Error("intended recipients must be HTML-escaped in the notice")
	}

	line := logs.String()
	if !strings.Contains(line, "email redirected") || !strings.Contains(line, "originalRecipients=3") || !strings.Contains(line, "debugRecipients=2") {
		t.Errorf("log = %q, want counts", line)
	}
	if strings.Contains(line, "@") {
		t.Errorf("log names an address: %q", line)
	}
	if n := strings.Count(line, "\n"); n != 1 {
		t.Errorf("log lines = %d, want 1", n)
	}
}

func TestDebugRedirectWithoutBodyTag(t *testing.T) {
	inner := &recordingSender{}
	s := WithDebugRedirect(inner, []string{"dev@wso2.com"})
	captureLogs(t)
	if err := s.SendEmail(context.Background(), []string{"a@wso2.com"}, nil, nil, nil, "S", "<p>Hi</p>", nil); err != nil {
		t.Fatal(err)
	}
	h := inner.got[0].html
	if !strings.HasPrefix(h, "<p") || !strings.Contains(h, "[Test copy]") || !strings.HasSuffix(h, "<p>Hi</p>") {
		t.Errorf("html = %q, want the notice first", h)
	}
}

func TestDebugRedirectOffReturnsSender(t *testing.T) {
	inner := &recordingSender{}
	for _, debug := range [][]string{nil, {}, {" ", ""}} {
		if got := WithDebugRedirect(inner, debug); got != Sender(inner) {
			t.Errorf("debug %q: got a wrapper, want the sender unchanged", debug)
		}
	}
}
