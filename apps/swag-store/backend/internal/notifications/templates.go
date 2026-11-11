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
	"embed"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Template names, as stored in swag_notification_outbox.template.
const (
	TemplateOrderConfirmed = "order_confirmed"
	TemplatePaymentFailed  = "payment_failed"
	TemplateReadyForPickup = "ready_for_pickup"
	TemplatePickupReminder = "pickup_reminder"
	TemplateCollected      = "collected"
	TemplateBackInStock    = "back_in_stock"
	TemplateProductRemoved = "product_removed"
	TemplateLowStockDigest = "low_stock_digest"
)

// Common is what every email needs besides its payload.
type Common struct {
	StoreURL     string         // STORE_WEB_URL, e.g. https://one.example; links are StoreURL + "/store/..."
	ContactEmail string         // STORE_CONTACT_EMAIL, shown under "Something wrong?"
	Timezone     *time.Location // STORE_TIMEZONE, for dates; nil means UTC
}

// EmailLine is one order line in an order email.
type EmailLine struct {
	Name      string `json:"name"`
	Options   string `json:"options"`
	Quantity  int    `json:"quantity"`
	UnitPrice int64  `json:"unitPrice"`
}

// OrderEmail is the payload of order_confirmed, payment_failed,
// ready_for_pickup, pickup_reminder and collected.
type OrderEmail struct {
	OrderNo         string      `json:"orderNo"`
	FirstName       string      `json:"firstName"`
	OfficeName      string      `json:"officeName"`
	CollectionPoint string      `json:"collectionPoint"`
	Hours           string      `json:"hours"`
	LeadTimeDays    int         `json:"leadTimeDays"`
	Total           int64       `json:"total"`
	Lines           []EmailLine `json:"lines"`
	PlacedAt        time.Time   `json:"placedAt"`
}

// AlertEmail is the payload of back_in_stock and product_removed.
type AlertEmail struct {
	FirstName   string `json:"firstName"`
	ProductName string `json:"productName"`
	Options     string `json:"options"`
	ProductSlug string `json:"productSlug"`
}

// LowStockItem is one variant in the low-stock digest.
type LowStockItem struct {
	SKU     string `json:"sku"`
	Name    string `json:"name"`
	Options string `json:"options"`
	Free    int    `json:"free"`
}

// LowStockDigest is the payload of low_stock_digest.
type LowStockDigest struct {
	Threshold int            `json:"threshold"`
	Items     []LowStockItem `json:"items"`
}

// Rendered is a filled-in email.
type Rendered struct{ Subject, HTML string }

// Render decodes payload (JSON of the template's payload type) and fills the
// template. An unknown template or bad JSON is an error.
func Render(template string, payload []byte, common Common) (Rendered, error) {
	switch template {
	case TemplateOrderConfirmed, TemplatePaymentFailed, TemplateReadyForPickup, TemplatePickupReminder, TemplateCollected:
		var d OrderEmail
		if err := decodePayload(template, payload, &d); err != nil {
			return Rendered{}, err
		}
		return orderRenderers[template](d, common), nil
	case TemplateBackInStock, TemplateProductRemoved:
		var d AlertEmail
		if err := decodePayload(template, payload, &d); err != nil {
			return Rendered{}, err
		}
		if template == TemplateBackInStock {
			return RenderBackInStockEmail(d, common), nil
		}
		return RenderProductRemovedEmail(d, common), nil
	case TemplateLowStockDigest:
		var d LowStockDigest
		if err := decodePayload(template, payload, &d); err != nil {
			return Rendered{}, err
		}
		return RenderLowStockDigestEmail(d, common), nil
	default:
		return Rendered{}, fmt.Errorf("notifications: unknown template %q", template)
	}
}

func decodePayload(template string, payload []byte, v any) error {
	if err := json.Unmarshal(payload, v); err != nil {
		return fmt.Errorf("notifications: decode %s payload: %w", template, err)
	}
	return nil
}

var orderRenderers = map[string]func(OrderEmail, Common) Rendered{
	TemplateOrderConfirmed: RenderOrderConfirmedEmail,
	TemplatePaymentFailed:  RenderPaymentFailedEmail,
	TemplateReadyForPickup: RenderReadyForPickupEmail,
	TemplatePickupReminder: RenderPickupReminderEmail,
	TemplateCollected:      RenderCollectedEmail,
}

// RenderOrderConfirmedEmail: the lines, the total and when it will be ready.
func RenderOrderConfirmedEmail(d OrderEmail, c Common) Rendered {
	return orderPage(TemplateOrderConfirmed, d, c, "Your Swag Store order "+d.OrderNo+" is confirmed", "Order confirmed")
}

// RenderPaymentFailedEmail says the payment failed and no coins were taken.
func RenderPaymentFailedEmail(d OrderEmail, c Common) Rendered {
	return orderPage(TemplatePaymentFailed, d, c, "Payment for your Swag Store order "+d.OrderNo+" did not go through", "Payment failed")
}

// RenderReadyForPickupEmail says where and when to collect the order.
func RenderReadyForPickupEmail(d OrderEmail, c Common) Rendered {
	return orderPage(TemplateReadyForPickup, d, c, "Your Swag Store order "+d.OrderNo+" is ready for pickup", "Ready for pickup")
}

// RenderPickupReminderEmail repeats the pickup details of an uncollected order.
func RenderPickupReminderEmail(d OrderEmail, c Common) Rendered {
	return orderPage(TemplatePickupReminder, d, c, "Reminder: your Swag Store order "+d.OrderNo+" is waiting for pickup", "Pickup reminder")
}

// RenderCollectedEmail confirms the order was collected.
func RenderCollectedEmail(d OrderEmail, c Common) Rendered {
	return orderPage(TemplateCollected, d, c, "Your Swag Store order "+d.OrderNo+" has been collected", "Order collected")
}

func orderPage(name string, d OrderEmail, c Common, subject, heading string) Rendered {
	return fill(name, c, page{
		subject:   subject,
		heading:   heading,
		link:      c.link("/store/orders/" + url.PathEscape(d.OrderNo)),
		linkLabel: "View your order",
		text: []string{
			"<!-- [FIRST_NAME] -->", d.FirstName,
			"<!-- [ORDER_NO] -->", d.OrderNo,
			"<!-- [PLACED_ON] -->", d.PlacedAt.In(c.location()).Format("2 Jan 2006"),
			"<!-- [OFFICE] -->", d.OfficeName,
			"<!-- [LEAD_TIME] -->", plural(int64(max(d.LeadTimeDays, 1)), "day", "days"),
		},
		html: []string{
			"<!-- [ORDER_LINES] -->", orderLinesTable(d),
			"<!-- [PICKUP_DETAILS] -->", pickupDetails(d),
		},
	})
}

// RenderBackInStockEmail says the item is back and links to it.
func RenderBackInStockEmail(d AlertEmail, c Common) Rendered {
	return alertPage(TemplateBackInStock, d, c, "Back in stock: "+d.ProductName, "Back in stock", "View the item")
}

// RenderProductRemovedEmail says the item is no longer sold and the alert is closed.
func RenderProductRemovedEmail(d AlertEmail, c Common) Rendered {
	return alertPage(TemplateProductRemoved, d, c, "No longer available: "+d.ProductName, "No longer available", "Open the store")
}

func alertPage(name string, d AlertEmail, c Common, subject, heading, linkLabel string) Rendered {
	return fill(name, c, page{
		subject:   subject,
		heading:   heading,
		link:      c.link("/store/products/" + url.PathEscape(d.ProductSlug)),
		linkLabel: linkLabel,
		text:      []string{"<!-- [FIRST_NAME] -->", d.FirstName},
		html:      []string{"<!-- [ITEM] -->", itemName(d.ProductName, d.Options)},
	})
}

// RenderLowStockDigestEmail lists the variants at or below the threshold.
func RenderLowStockDigestEmail(d LowStockDigest, c Common) Rendered {
	count := "No variants are"
	switch n := len(d.Items); {
	case n == 1:
		count = "1 variant is"
	case n > 1:
		count = strconv.Itoa(n) + " variants are"
	}
	return fill(TemplateLowStockDigest, c, page{
		subject:   fmt.Sprintf("Swag Store low stock: %s at or below %d", plural(int64(len(d.Items)), "variant", "variants"), d.Threshold),
		heading:   "Low stock digest",
		link:      c.link("/store/admin/products"),
		linkLabel: "Manage products",
		text: []string{
			"<!-- [ITEM_COUNT] -->", count,
			"<!-- [THRESHOLD] -->", strconv.Itoa(d.Threshold),
		},
		html: []string{"<!-- [DIGEST_TABLE] -->", digestTable(d.Items)},
	})
}

// page is one email's values. text values are plain and escaped by fill; html
// values are markup already built from escaped values.
type page struct {
	subject, heading, link, linkLabel string
	text, html                        []string
}

func fill(name string, c Common, p page) Rendered {
	subject := strings.Join(strings.Fields(p.subject), " ") // no line breaks in a header
	pairs := []string{
		"<!-- [SUBJECT] -->", escapeHTML(subject),
		"<!-- [HEADING] -->", escapeHTML(p.heading),
		"<!-- [LINK_URL] -->", escapeHTML(p.link),
		"<!-- [LINK_LABEL] -->", escapeHTML(p.linkLabel),
		"<!-- [STORE_HOME] -->", escapeHTML(c.link("/store")),
		"<!-- [CONTACT_EMAIL] -->", escapeHTML(c.ContactEmail),
		"<!-- [YEAR] -->", strconv.Itoa(time.Now().In(c.location()).Year()),
	}
	for i := 0; i+1 < len(p.text); i += 2 {
		pairs = append(pairs, p.text[i], escapeHTML(p.text[i+1]))
	}
	pairs = append(pairs, p.html...)
	return Rendered{Subject: subject, HTML: strings.NewReplacer(pairs...).Replace(templates[name])}
}

func (c Common) link(path string) string { return strings.TrimRight(c.StoreURL, "/") + path }

func (c Common) location() *time.Location {
	if c.Timezone == nil {
		return time.UTC
	}
	return c.Timezone
}

const (
	cellStyle  = `padding:8px 10px; border-bottom:1px solid #e8eaed;`
	headStyle  = `padding:8px 10px; border-bottom:2px solid #e8eaed; color:#8a8f98; font-size:12px; font-weight:bold; text-transform:uppercase;`
	tableOpen  = `<table border="0" cellpadding="0" cellspacing="0" width="100%" style="margin:0 0 20px; border-collapse:collapse; font-size:14px; line-height:20px;">`
	mutedStyle = `color:#8a8f98; font-size:12px;`
)

// orderLinesTable is the lines with their amounts and the order total.
func orderLinesTable(d OrderEmail) string {
	var b strings.Builder
	b.WriteString(tableOpen)
	fmt.Fprintf(&b, `<tr><td style="%s">Item</td><td align="right" style="%s">Qty</td><td align="right" style="%s">Amount</td></tr>`,
		headStyle, headStyle, headStyle)
	for _, l := range d.Lines {
		fmt.Fprintf(&b, `<tr><td style="%s">%s</td><td align="right" style="%s">%d</td><td align="right" style="%s">%s</td></tr>`,
			cellStyle, itemName(l.Name, l.Options), cellStyle, l.Quantity, cellStyle, escapeHTML(coins(l.UnitPrice*int64(l.Quantity))))
	}
	fmt.Fprintf(&b, `<tr><td colspan="2" style="padding:10px; font-weight:bold;">Total</td><td align="right" style="padding:10px; font-weight:bold;">%s</td></tr>`,
		escapeHTML(coins(d.Total)))
	b.WriteString(`</table>`)
	return b.String()
}

// pickupDetails is where and when to collect the order.
func pickupDetails(d OrderEmail) string {
	var b strings.Builder
	b.WriteString(`<table border="0" cellpadding="0" cellspacing="0" width="100%" style="margin:0 0 20px; background-color:#fff7f0; border:1px solid #ffd9bf; font-size:14px; line-height:20px;">`)
	for _, row := range [][2]string{{"Office", d.OfficeName}, {"Collect from", d.CollectionPoint}, {"Hours", d.Hours}} {
		fmt.Fprintf(&b, `<tr><td style="padding:8px 14px; width:110px; color:#8a8f98; vertical-align:top;">%s</td><td style="padding:8px 14px; font-weight:bold;">%s</td></tr>`,
			row[0], escapeHTML(row[1]))
	}
	b.WriteString(`</table>`)
	return b.String()
}

// digestTable is one row per low-stock variant, or nothing when there is none.
func digestTable(items []LowStockItem) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(tableOpen)
	fmt.Fprintf(&b, `<tr><td style="%s">SKU</td><td style="%s">Item</td><td align="right" style="%s">Free</td></tr>`,
		headStyle, headStyle, headStyle)
	for _, it := range items {
		fmt.Fprintf(&b, `<tr><td style="%s font-family:'Courier New', monospace; font-size:13px;">%s</td><td style="%s">%s</td><td align="right" style="%s">%d</td></tr>`,
			cellStyle, escapeHTML(it.SKU), cellStyle, itemName(it.Name, it.Options), cellStyle, it.Free)
	}
	b.WriteString(`</table>`)
	return b.String()
}

// itemName is the escaped product name, with its options in small print.
func itemName(name, options string) string {
	s := escapeHTML(name)
	if options != "" {
		s += `<br /><span style="` + mutedStyle + `">` + escapeHTML(options) + `</span>`
	}
	return s
}

// coins formats an amount as "<n> coins".
func coins(n int64) string { return plural(n, "coin", "coins") }

func plural(n int64, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.FormatInt(n, 10) + " " + many
}

// wso2LogoURL is WSO2's own public asset host. Mail clients strip data: URI
// images, so the logo must be a URL.
const wso2LogoURL = "https://wso2.cachefly.net/wso2/sites/all/image_resources/logos/WSO2-Logo-White.png"

//go:embed templates/*.html
var templateFS embed.FS

// templates holds each email as a full document: the layout with the logo
// baked in and the template's own content in place of <!-- [CONTENT] -->.
var templates = loadTemplates()

func loadTemplates() map[string]string {
	layout := strings.Replace(readTemplate("layout"), "<!-- [LOGO_SRC] -->", wso2LogoURL, 1)
	out := make(map[string]string)
	for _, name := range []string{
		TemplateOrderConfirmed, TemplatePaymentFailed, TemplateReadyForPickup, TemplatePickupReminder,
		TemplateCollected, TemplateBackInStock, TemplateProductRemoved, TemplateLowStockDigest,
	} {
		out[name] = strings.Replace(layout, "<!-- [CONTENT] -->", strings.TrimSpace(readTemplate(name)), 1)
	}
	return out
}

// readTemplate reads an embedded file without its licence header comment,
// which has no place in a sent email.
func readTemplate(name string) string {
	b, err := templateFS.ReadFile("templates/" + name + ".html")
	if err != nil {
		panic(fmt.Sprintf("notifications: missing template %s: %v", name, err))
	}
	s := string(b)
	if strings.HasPrefix(s, "<!--") {
		if end := strings.Index(s, "-->"); end >= 0 && strings.Contains(s[:end], "Copyright") {
			s = strings.TrimLeft(s[end+len("-->"):], "\r\n")
		}
	}
	return s
}

// escapeHTML HTML-escapes s and writes every non-ASCII rune as a numeric
// character reference. email-service does not reliably keep non-ASCII bytes
// (they arrive as "?"); an entity is pure ASCII and every mail client decodes it.
func escapeHTML(s string) string {
	var b strings.Builder
	for _, r := range html.EscapeString(s) {
		if r > 127 {
			b.WriteString("&#")
			b.WriteString(strconv.Itoa(int(r)))
			b.WriteByte(';')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
