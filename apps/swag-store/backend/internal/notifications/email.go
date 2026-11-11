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

// Package notifications sends the store's emails: the email-service client,
// the HTML templates the outbox job renders, and the debug redirect that keeps
// non-production mail away from real recipients.
package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/apierror"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/reqctx"
)

// tokenFetchTimeout bounds a token-endpoint call.
var tokenFetchTimeout = 10 * time.Second

// EmailConfig holds the email-service base URL and the shared OAuth2 app.
type EmailConfig struct {
	BaseURL      string
	TokenURL     string
	ClientID     string
	ClientSecret string
	Scopes       []string

	// FromAddress is the fixed From address on every email
	// (EMAIL_FROM_ADDRESS). It is config, not a SendEmail argument, so every
	// store email comes from one pre-approved sender.
	FromAddress string

	Timeout time.Duration // UPSTREAM_TIMEOUT; 0 means 25 s
}

// EmailAttachment is one file attached to an outgoing email.
type EmailAttachment struct {
	ContentName string `json:"contentName"` // file name, e.g. "invoice.pdf"
	ContentType string `json:"contentType"` // MIME type
	Attachment  []byte `json:"attachment"`  // raw content; encoding/json base64-encodes it
}

// Sender is what the outbox job sends through: an *EmailClient, or the
// debug redirect wrapped around one.
type Sender interface {
	SendEmail(ctx context.Context, to, cc, bcc, replyTo []string, subject, htmlBody string, attachments []EmailAttachment) error
}

// EmailClient calls infra email-service. Tokens are fetched and refreshed by
// x/oauth2. NewEmailClient never fails and contacts nothing, so a missing or
// wrong config only shows up on the first call.
type EmailClient struct {
	http        *http.Client
	baseURL     string
	fromAddress string
}

// NewEmailClient builds the client.
func NewEmailClient(cfg EmailConfig) *EmailClient {
	cc := clientcredentials.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		TokenURL:     cfg.TokenURL,
		Scopes:       cfg.Scopes,
	}
	tokenCtx := context.WithValue(context.Background(), oauth2.HTTPClient, &http.Client{Timeout: tokenFetchTimeout})
	hc := cc.Client(tokenCtx)
	hc.Timeout = cfg.Timeout
	if hc.Timeout == 0 {
		hc.Timeout = 25 * time.Second
	}
	return &EmailClient{http: hc, baseURL: strings.TrimRight(cfg.BaseURL, "/"), fromAddress: cfg.FromAddress}
}

// sendEmailRequest is the wire body of POST /send-email.
type sendEmailRequest struct {
	To          []string          `json:"to"`
	CC          []string          `json:"cc,omitempty"`
	BCC         []string          `json:"bcc,omitempty"`
	ReplyTo     []string          `json:"replyTo,omitempty"`
	From        string            `json:"from"`
	Subject     string            `json:"subject"`
	Template    []byte            `json:"template"` // the HTML; encoding/json base64-encodes it
	Attachments []EmailAttachment `json:"attachments,omitempty"`
}

// SendEmail sends one HTML email. The From address is always the configured
// FromAddress.
func (c *EmailClient) SendEmail(ctx context.Context, to, cc, bcc, replyTo []string, subject, htmlBody string, attachments []EmailAttachment) error {
	if len(to) == 0 {
		return errors.New("notifications: at least one recipient (to) is required")
	}
	if subject == "" {
		return errors.New("notifications: subject is required")
	}
	body, err := json.Marshal(sendEmailRequest{
		To:          to,
		CC:          cc,
		BCC:         bcc,
		ReplyTo:     replyTo,
		From:        c.fromAddress,
		Subject:     subject,
		Template:    []byte(htmlBody),
		Attachments: attachments,
	})
	if err != nil {
		return fmt.Errorf("notifications: encode send-email request: %w", err)
	}
	return c.do(ctx, http.MethodPost, "/send-email", body)
}

// Health calls GET /health. It is used by /health/dependencies only.
func (c *EmailClient) Health(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/health", nil)
}

// do sends one request. A non-2xx answer becomes an *apierror.Error.
func (c *EmailClient) do(ctx context.Context, method, path string, body []byte) error {
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("notifications: build request %s %s: %w", method, path, err)
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if id := reqctx.CorrelationID(ctx); id != "" {
		req.Header.Set("X-Correlation-ID", id)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("notifications: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("notifications: read response: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return apierror.New(resp.StatusCode, respBody)
	}
	return nil
}
