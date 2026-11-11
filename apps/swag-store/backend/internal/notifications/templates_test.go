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
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// colombo is a fixed +05:30 zone, so the tests need no tzdata.
var colombo = time.FixedZone("Colombo", 5*3600+1800)

var testCommon = Common{StoreURL: "https://one.example/", ContactEmail: "swag@wso2.com", Timezone: colombo}

// 20:00 UTC on 10 Nov is 01:30 on 11 Nov in Colombo.
const orderJSON = `{
  "orderNo": "SS-10482", "firstName": "Nimal", "officeName": "Colombo Trace",
  "collectionPoint": "Reception, Level 3", "hours": "Mon-Fri 9:00-17:00", "leadTimeDays": 3,
  "total": 180,
  "lines": [
    {"name": "WSO2 Hoodie", "options": "Size M / Black", "quantity": 2, "unitPrice": 60},
    {"name": "Mug", "options": "", "quantity": 1, "unitPrice": 60}
  ],
  "placedAt": "2026-11-10T20:00:00Z"
}`

const alertJSON = `{"firstName": "Nimal", "productName": "WSO2 Hoodie", "options": "Size M / Black", "productSlug": "wso2-hoodie"}`

const digestJSON = `{"threshold": 5, "items": [
  {"sku": "HOOD-M-BLK", "name": "WSO2 Hoodie", "options": "Size M / Black", "free": 2},
  {"sku": "MUG-01", "name": "Mug", "options": "", "free": 0}
]}`

var allTemplates = []struct{ name, payload string }{
	{TemplateOrderConfirmed, orderJSON},
	{TemplatePaymentFailed, orderJSON},
	{TemplateReadyForPickup, orderJSON},
	{TemplatePickupReminder, orderJSON},
	{TemplateCollected, orderJSON},
	{TemplateBackInStock, alertJSON},
	{TemplateProductRemoved, alertJSON},
	{TemplateLowStockDigest, digestJSON},
}

func mustRender(t *testing.T, name, payload string) Rendered {
	t.Helper()
	r, err := Render(name, []byte(payload), testCommon)
	if err != nil {
		t.Fatalf("Render(%s): %v", name, err)
	}
	return r
}

func assertContains(t *testing.T, html string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(html, w) {
			t.Errorf("html does not contain %q", w)
		}
	}
}

const orderLink = `href="https://one.example/store/orders/SS-10482"`

func TestRenderOrderConfirmedEmail(t *testing.T) {
	r := mustRender(t, TemplateOrderConfirmed, orderJSON)
	if r.Subject != "Your Swag Store order SS-10482 is confirmed" {
		t.Errorf("subject = %q", r.Subject)
	}
	assertContains(t, r.HTML, "Nimal", "SS-10482", "11 Nov 2026", "WSO2 Hoodie", "Size M / Black", "Mug",
		"120 coins", "180 coins", "ready in about 3 days at Colombo Trace", orderLink)
}

func TestRenderPaymentFailedEmail(t *testing.T) {
	r := mustRender(t, TemplatePaymentFailed, orderJSON)
	if !strings.Contains(r.Subject, "SS-10482") {
		t.Errorf("subject = %q", r.Subject)
	}
	assertContains(t, r.HTML, "Nimal", "No coins were taken", "WSO2 Hoodie", orderLink)
}

func TestRenderReadyForPickupEmail(t *testing.T) {
	r := mustRender(t, TemplateReadyForPickup, orderJSON)
	if r.Subject != "Your Swag Store order SS-10482 is ready for pickup" {
		t.Errorf("subject = %q", r.Subject)
	}
	assertContains(t, r.HTML, "ready for pickup", "Colombo Trace", "Reception, Level 3", "Mon-Fri 9:00-17:00", orderLink)
}

func TestRenderPickupReminderEmail(t *testing.T) {
	r := mustRender(t, TemplatePickupReminder, orderJSON)
	if !strings.HasPrefix(r.Subject, "Reminder:") || !strings.Contains(r.Subject, "SS-10482") {
		t.Errorf("subject = %q", r.Subject)
	}
	assertContains(t, r.HTML, "still waiting", "Colombo Trace", "Reception, Level 3", "Mon-Fri 9:00-17:00", orderLink)
}

func TestRenderCollectedEmail(t *testing.T) {
	r := mustRender(t, TemplateCollected, orderJSON)
	if r.Subject != "Your Swag Store order SS-10482 has been collected" {
		t.Errorf("subject = %q", r.Subject)
	}
	assertContains(t, r.HTML, "was collected", "Colombo Trace", "WSO2 Hoodie", orderLink)
}

func TestRenderBackInStockEmail(t *testing.T) {
	r := mustRender(t, TemplateBackInStock, alertJSON)
	if r.Subject != "Back in stock: WSO2 Hoodie" {
		t.Errorf("subject = %q", r.Subject)
	}
	assertContains(t, r.HTML, "Nimal", "is back in stock", "WSO2 Hoodie", "Size M / Black",
		`href="https://one.example/store/products/wso2-hoodie"`)
}

func TestRenderProductRemovedEmail(t *testing.T) {
	r := mustRender(t, TemplateProductRemoved, alertJSON)
	if r.Subject != "No longer available: WSO2 Hoodie" {
		t.Errorf("subject = %q", r.Subject)
	}
	assertContains(t, r.HTML, "Nimal", "no longer sold", "alert is closed", "WSO2 Hoodie",
		`href="https://one.example/store/products/wso2-hoodie"`)
}

func TestRenderLowStockDigestEmail(t *testing.T) {
	r := mustRender(t, TemplateLowStockDigest, digestJSON)
	if r.Subject != "Swag Store low stock: 2 variants at or below 5" {
		t.Errorf("subject = %q", r.Subject)
	}
	assertContains(t, r.HTML, "HOOD-M-BLK", "WSO2 Hoodie", "Size M / Black", "MUG-01", ">2<", ">0<",
		`href="https://one.example/store/admin/products"`)

	empty := mustRender(t, TemplateLowStockDigest, `{"threshold": 5, "items": []}`)
	assertContains(t, empty.HTML, "No variants are at or below")
}

// TestEveryEmail checks what every template shares: the layout, the store
// link, the contact line, no wallet details, no unfilled placeholder.
func TestEveryEmail(t *testing.T) {
	for _, tc := range allTemplates {
		t.Run(tc.name, func(t *testing.T) {
			r := mustRender(t, tc.name, tc.payload)
			h := r.HTML
			if !strings.HasPrefix(h, "<!DOCTYPE") {
				t.Errorf("html starts with %.40q, want the doctype", h)
			}
			assertContains(t, h, wso2LogoURL, `href="https://one.example/store"`,
				"Something wrong? Contact", `href="mailto:swag@wso2.com"`)
			if strings.Contains(h, "<!-- [") {
				t.Error("an unfilled placeholder is left")
			}
			if strings.Contains(h, "Dissemination") {
				t.Error("the source licence header leaked into the email")
			}
			if strings.Contains(strings.ToLower(h+r.Subject), "balance") || strings.Contains(h+r.Subject, "0x") {
				t.Error("an email must not mention a wallet balance or address")
			}
			if strings.Contains(h, "<style") {
				t.Error("inline CSS only")
			}
			if r.Subject == "" {
				t.Error("empty subject")
			}
		})
	}
}

func TestEmailTemplatesEscape(t *testing.T) {
	order, _ := json.Marshal(OrderEmail{
		OrderNo: `SS-1"><script>`, FirstName: `Zoë <script>alert("x")</script>`, OfficeName: "Café <i>",
		CollectionPoint: `"desk"`, Hours: "<img src=x onerror=alert(1)>", LeadTimeDays: 2, Total: 5,
		Lines:    []EmailLine{{Name: `<script>alert("x")</script>`, Options: `Café "large" <img src=x>`, Quantity: 1, UnitPrice: 5}},
		PlacedAt: time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC),
	})
	alert, _ := json.Marshal(AlertEmail{FirstName: "Zoë", ProductName: `<script>alert("x")</script>`,
		Options: `Café "large"`, ProductSlug: `a"b<c>`})
	digest, _ := json.Marshal(LowStockDigest{Threshold: 3, Items: []LowStockItem{
		{SKU: `<script>`, Name: `Café "x"`, Options: "<img src=x>", Free: 1}}})
	payloads := map[string][]byte{}
	for _, tc := range allTemplates {
		payloads[tc.name] = order
	}
	payloads[TemplateBackInStock], payloads[TemplateProductRemoved], payloads[TemplateLowStockDigest] = alert, alert, digest

	for name, p := range payloads {
		t.Run(name, func(t *testing.T) {
			r, err := Render(name, p, testCommon)
			if err != nil {
				t.Fatal(err)
			}
			h := r.HTML
			for _, bad := range []string{"<script>", "<img src=x", `"x"`, `"large"`, `"desk"`, `SS-1"`, "Café", "Zoë", "<i>"} {
				if strings.Contains(h, bad) {
					t.Errorf("html contains unescaped %q", bad)
				}
			}
			assertContains(t, h, "&lt;script&gt;", "Caf&#233;")
			for i := 0; i < len(h); i++ {
				if h[i] > 127 {
					t.Fatalf("html has a non-ASCII byte at %d: %q", i, h[max(0, i-20):min(len(h), i+20)])
				}
			}
		})
	}

	// A link built from a user value stays one attribute.
	r, _ := Render(TemplateBackInStock, alert, testCommon)
	assertContains(t, r.HTML, `href="https://one.example/store/products/a%22b%3Cc%3E"`)
}

func TestRenderSubjectHasNoLineBreaks(t *testing.T) {
	alert, _ := json.Marshal(AlertEmail{ProductName: "Hoodie\r\nBcc: someone@example.com"})
	r, err := Render(TemplateBackInStock, alert, testCommon)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(r.Subject, "\r\n") {
		t.Errorf("subject = %q, want no line breaks", r.Subject)
	}
}

func TestRenderErrors(t *testing.T) {
	if _, err := Render("nope", []byte(`{}`), testCommon); err == nil {
		t.Error("unknown template: want error")
	}
	for _, tc := range allTemplates {
		if _, err := Render(tc.name, []byte(`{not json`), testCommon); err == nil {
			t.Errorf("%s: bad JSON: want error", tc.name)
		}
	}
}

func TestRenderNilTimezoneIsUTC(t *testing.T) {
	c := testCommon
	c.Timezone = nil
	r, err := Render(TemplateOrderConfirmed, []byte(orderJSON), c)
	if err != nil {
		t.Fatal(err)
	}
	assertContains(t, r.HTML, "10 Nov 2026")
}
