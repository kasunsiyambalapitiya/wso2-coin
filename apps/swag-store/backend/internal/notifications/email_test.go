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
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/apierror"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/reqctx"
)

type emailCall struct {
	method, path, auth, contentType, correlation string
	body                                         []byte
}

type fakeEmailService struct {
	srv    *httptest.Server
	mu     sync.Mutex
	calls  []emailCall
	status int
	reply  string
}

func newFakeEmailService(t *testing.T) *fakeEmailService {
	t.Helper()
	f := &fakeEmailService{status: http.StatusOK, reply: `{"status":"sent"}`}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"svc-token","token_type":"Bearer","expires_in":3600}`)
			return
		}
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.calls = append(f.calls, emailCall{r.Method, r.URL.Path, r.Header.Get("Authorization"),
			r.Header.Get("Content-Type"), r.Header.Get("X-Correlation-ID"), b})
		status, reply := f.status, f.reply
		f.mu.Unlock()
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeEmailService) client() *EmailClient {
	return NewEmailClient(EmailConfig{
		BaseURL: f.srv.URL + "/", TokenURL: f.srv.URL + "/token", ClientID: "c", ClientSecret: "s",
		FromAddress: "store@wso2.com", Timeout: 5 * time.Second,
	})
}

func (f *fakeEmailService) answer(status int, reply string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status, f.reply = status, reply
}

func (f *fakeEmailService) only(t *testing.T) emailCall {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(f.calls))
	}
	return f.calls[0]
}

func TestSendEmailWireBody(t *testing.T) {
	f := newFakeEmailService(t)
	ctx := reqctx.WithCorrelationID(context.Background(), "corr-1")
	att := []EmailAttachment{{ContentName: "a.txt", ContentType: "text/plain", Attachment: []byte("hi")}}
	err := f.client().SendEmail(ctx, []string{"to@wso2.com"}, []string{"cc@wso2.com"}, []string{"bcc@wso2.com"},
		[]string{"contact@wso2.com"}, "Subject", "<p>Hello</p>", att)
	if err != nil {
		t.Fatalf("SendEmail: %v", err)
	}
	c := f.only(t)
	if c.method != http.MethodPost || c.path != "/send-email" {
		t.Errorf("request = %s %s, want POST /send-email", c.method, c.path)
	}
	if c.auth != "Bearer svc-token" {
		t.Errorf("Authorization = %q", c.auth)
	}
	if c.contentType != "application/json" {
		t.Errorf("Content-Type = %q", c.contentType)
	}
	if c.correlation != "corr-1" {
		t.Errorf("X-Correlation-ID = %q, want corr-1", c.correlation)
	}
	var got struct {
		To          []string          `json:"to"`
		CC          []string          `json:"cc"`
		BCC         []string          `json:"bcc"`
		ReplyTo     []string          `json:"replyTo"`
		From        string            `json:"from"`
		Subject     string            `json:"subject"`
		Template    []byte            `json:"template"`
		Attachments []EmailAttachment `json:"attachments"`
	}
	if err := json.Unmarshal(c.body, &got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(got.To) != 1 || got.To[0] != "to@wso2.com" || len(got.CC) != 1 || len(got.BCC) != 1 || len(got.ReplyTo) != 1 {
		t.Errorf("recipients = %+v", got)
	}
	if got.From != "store@wso2.com" || got.Subject != "Subject" {
		t.Errorf("from/subject = %q / %q", got.From, got.Subject)
	}
	if string(got.Template) != "<p>Hello</p>" {
		t.Errorf("template = %q (must be the HTML as base64 bytes)", got.Template)
	}
	if len(got.Attachments) != 1 || string(got.Attachments[0].Attachment) != "hi" || got.Attachments[0].ContentName != "a.txt" {
		t.Errorf("attachments = %+v", got.Attachments)
	}
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(c.body, &raw)
	if string(raw["template"]) != `"PHA+SGVsbG88L3A+"` {
		t.Errorf("template on the wire = %s, want base64", raw["template"])
	}
}

func TestSendEmailOmitsEmptyOptionalFields(t *testing.T) {
	f := newFakeEmailService(t)
	if err := f.client().SendEmail(context.Background(), []string{"to@wso2.com"}, nil, nil, nil, "S", "<p>x</p>", nil); err != nil {
		t.Fatalf("SendEmail: %v", err)
	}
	c := f.only(t)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(c.body, &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, k := range []string{"cc", "bcc", "replyTo", "attachments"} {
		if _, ok := raw[k]; ok {
			t.Errorf("%s present, want omitted", k)
		}
	}
	for _, k := range []string{"to", "from", "subject", "template"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("%s missing", k)
		}
	}
	if c.correlation != "" {
		t.Errorf("X-Correlation-ID = %q, want none", c.correlation)
	}
}

func TestSendEmailNon2xxIsAPIError(t *testing.T) {
	f := newFakeEmailService(t)
	f.answer(http.StatusBadGateway, `{"message":"smtp down"}`)
	err := f.client().SendEmail(context.Background(), []string{"to@wso2.com"}, nil, nil, nil, "S", "<p>x</p>", nil)
	var ae *apierror.Error
	if !errors.As(err, &ae) {
		t.Fatalf("err = %v, want *apierror.Error", err)
	}
	if ae.StatusCode != http.StatusBadGateway || ae.Body != `{"message":"smtp down"}` {
		t.Errorf("apierror = %+v", ae)
	}
}

func TestSendEmailValidates(t *testing.T) {
	f := newFakeEmailService(t)
	c := f.client()
	if err := c.SendEmail(context.Background(), nil, nil, nil, nil, "S", "<p>x</p>", nil); err == nil {
		t.Error("no recipients: want error")
	}
	if err := c.SendEmail(context.Background(), []string{"to@wso2.com"}, nil, nil, nil, "", "<p>x</p>", nil); err == nil {
		t.Error("no subject: want error")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) != 0 {
		t.Errorf("calls = %d, want 0", len(f.calls))
	}
}

func TestEmailHealth(t *testing.T) {
	f := newFakeEmailService(t)
	if err := f.client().Health(context.Background()); err != nil {
		t.Fatalf("Health: %v", err)
	}
	c := f.only(t)
	if c.method != http.MethodGet || c.path != "/health" {
		t.Errorf("request = %s %s, want GET /health", c.method, c.path)
	}

	f.answer(http.StatusServiceUnavailable, "down")
	if got := apierror.Status(f.client().Health(context.Background())); got != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", got)
	}
}

func TestNewEmailClientDefaultTimeout(t *testing.T) {
	c := NewEmailClient(EmailConfig{})
	if c.http.Timeout != 25*time.Second {
		t.Errorf("timeout = %v, want 25s", c.http.Timeout)
	}
}
