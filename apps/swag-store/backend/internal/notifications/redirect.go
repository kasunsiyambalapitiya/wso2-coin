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
	"log/slog"
	"slices"
	"strings"
)

// WithDebugRedirect returns s unchanged when debug holds no address. Otherwise
// it returns debug mail mode (EMAIL_DEBUG_RECIPIENTS): every email goes only
// to the debug addresses, Cc and Bcc are dropped, the subject gets "[Test
// copy] " in front, and the body gets a notice at the top naming whom it was
// meant for. Set it in every non-production environment so real data can be
// exercised without reaching real people.
func WithDebugRedirect(s Sender, debug []string) Sender {
	d := cleanAddresses(debug)
	if len(d) == 0 {
		return s
	}
	return &redirected{inner: s, debug: d}
}

type redirected struct {
	inner Sender
	debug []string
}

func (r *redirected) SendEmail(ctx context.Context, to, cc, bcc, replyTo []string, subject, htmlBody string, attachments []EmailAttachment) error {
	// The log line is the audit trail of a redirect. The addresses stay in the
	// notice and out of the logs.
	slog.InfoContext(ctx, "email redirected",
		"originalRecipients", len(to)+len(cc)+len(bcc), "debugRecipients", len(r.debug))
	return r.inner.SendEmail(ctx, slices.Clone(r.debug), nil, nil, replyTo,
		"[Test copy] "+subject, withTestCopyNotice(htmlBody, to, cc, bcc), attachments)
}

// cleanAddresses trims the addresses and drops blanks, so a trailing comma in
// config never becomes an empty recipient.
func cleanAddresses(in []string) []string {
	var out []string
	for _, a := range in {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out
}

// withTestCopyNotice puts the notice just inside <body> when there is one, so
// a full document keeps its head, and at the very start otherwise.
func withTestCopyNotice(htmlBody string, to, cc, bcc []string) string {
	var parts []string
	for _, l := range []struct {
		label string
		addrs []string
	}{{"To", to}, {"Cc", cc}, {"Bcc", bcc}} {
		if len(l.addrs) > 0 {
			parts = append(parts, l.label+": "+strings.Join(l.addrs, ", "))
		}
	}
	intended := "nobody"
	if len(parts) > 0 {
		intended = strings.Join(parts, "; ")
	}
	notice := `<p style="margin:0; padding:12px 16px; background-color:#fff4e5; color:#7a4100; ` +
		`font-family:Arial, Helvetica, sans-serif; font-size:13px; line-height:18px;">` +
		`<b>[Test copy]</b> Debug mail mode is on, so this email was not sent to its real recipients. ` +
		`It was meant for ` + escapeHTML(intended) + `.</p>` + "\n"
	if i := strings.Index(strings.ToLower(htmlBody), "<body"); i >= 0 {
		if j := strings.Index(htmlBody[i:], ">"); j >= 0 {
			at := i + j + 1
			return htmlBody[:at] + "\n" + notice + htmlBody[at:]
		}
	}
	return notice + htmlBody
}
