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
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
)

// keepSent is how many sent emails GET /sent shows.
const keepSent = 50

// mailbox is the email-service fake. It sends nothing; it keeps the last
// emails for GET /sent.
type mailbox struct {
	mu   sync.Mutex
	sent []sentEmail // oldest first
}

type sentEmail struct {
	Subject string   `json:"subject"`
	To      []string `json:"to"`
	HTML    string   `json:"html"`
}

func newMailbox() *mailbox { return &mailbox{} }

func (m *mailbox) register(mux *http.ServeMux) {
	mux.HandleFunc("POST /send-email", m.handleSend)
	mux.HandleFunc("GET /sent", m.handleSent)
}

func (m *mailbox) handleSend(w http.ResponseWriter, r *http.Request) {
	var req struct {
		To          []string          `json:"to"`
		CC          []string          `json:"cc"`
		BCC         []string          `json:"bcc"`
		ReplyTo     []string          `json:"replyTo"`
		From        string            `json:"from"`
		Subject     string            `json:"subject"`
		Template    []byte            `json:"template"` // base64 on the wire
		Attachments []json.RawMessage `json:"attachments"`
	}
	if err := readJSON(r, 32<<20, &req); err != nil {
		writeMessage(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if len(req.To) == 0 || req.Subject == "" {
		writeMessage(w, http.StatusBadRequest, "to and subject are required.")
		return
	}
	slog.InfoContext(r.Context(), "fake email sent", "subject", req.Subject, "recipients", len(req.To)+len(req.CC)+len(req.BCC))

	m.mu.Lock()
	m.sent = append(m.sent, sentEmail{Subject: req.Subject, To: req.To, HTML: string(req.Template)})
	if len(m.sent) > keepSent {
		m.sent = append([]sentEmail(nil), m.sent[len(m.sent)-keepSent:]...)
	}
	m.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
}

func (m *mailbox) handleSent(w http.ResponseWriter, _ *http.Request) {
	m.mu.Lock()
	out := append([]sentEmail{}, m.sent...)
	m.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}
