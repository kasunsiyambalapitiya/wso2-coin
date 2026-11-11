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

// Package repository is the MySQL layer over the sqlc code in internal/store.
// Every write that spans tables runs in one transaction (inTx); stock changes
// go through conditional UPDATEs; admin writes record a swag_admin_actions row
// in the same transaction. The package holds no business rules: callers pass
// them in (for example NewOrder.Check).
package repository

import (
	"errors"
	"time"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/orders"
)

// Sentinel errors. Callers map them to HTTP responses.
var (
	ErrNotFound      = errors.New("not found")
	ErrDuplicateKey  = errors.New("an order with this idempotency key already exists")
	ErrCartEmpty     = errors.New("cart is empty")
	ErrOutOfStock    = errors.New("not enough free stock")
	ErrNotAvailable  = errors.New("product or variant not active")
	ErrStateConflict = errors.New("state changed in the meantime")
	ErrInUse         = errors.New("row is still referenced")
	ErrConflict      = errors.New("unique value already taken")
)

// CartItem is one cart line with everything the quote rules and the cart view need.
type CartItem struct {
	LineID        string
	VariantID     string
	ProductID     string
	Slug          string
	Name          string
	SKU           string
	Options       string // "Black · M"
	Quantity      int
	UnitPrice     int64 // product price + variant delta
	Free          int   // on hand − reserved
	ProductStatus string
	VariantActive bool
	RestockPolicy string
	MaxPerUser    *int
	PaidUnits     int // units of this product already ordered within the limit window
	RestockETA    *time.Time
	ImageURL      string
	AddedAt       time.Time
}

// Office is a pickup office.
type Office struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Address         string   `json:"address"`
	Country         string   `json:"country"`
	CollectionPoint string   `json:"collectionPoint"`
	Hours           string   `json:"hours"`
	LeadTimeDays    int      `json:"leadTimeDays"`
	HRLocations     []string `json:"hrLocations"`
	Active          bool     `json:"active"`
	Sort            int      `json:"sort"`
}

// Order is an order row.
type Order struct {
	ID               string
	OrderNo          string
	UserEmail        string
	UserID           string
	UserName         string
	Status           orders.Status
	OfficeID         string
	OfficeName       string
	WalletAddress    string
	Total            int64
	PaymentReference string
	IdempotencyKey   string
	FailureCode      string
	AttentionReason  string
	PaymentChecks    int
	PlacedAt         time.Time
	ConfirmedAt      *time.Time
	FailedAt         *time.Time
	ReadyAt          *time.Time
	CollectedAt      *time.Time
	CollectedBy      string
}

// OrderLine is a snapshot of what was bought.
type OrderLine struct {
	ID        string
	ProductID string
	VariantID string
	SKU       string
	Name      string
	Options   string
	UnitPrice int64
	Quantity  int
}

// OrderEvent is one status change.
type OrderEvent struct {
	From      string
	To        string
	Note      string
	Actor     string
	CreatedAt time.Time
}

// OrderNote is a fulfilment note.
type OrderNote struct {
	Note      string
	Actor     string
	CreatedAt time.Time
}

// OrderDetail is an order with its lines, timeline, notes and office.
type OrderDetail struct {
	Order  Order
	Lines  []OrderLine
	Events []OrderEvent
	Notes  []OrderNote
	Office *Office
}

// NewOrder is the input of CreatePendingOrder. Check re-runs the quote rules on
// the cart as read inside the transaction and returns the total, or an error
// that aborts the order.
type NewOrder struct {
	ID             string
	UserEmail      string
	UserID         string
	UserName       string
	OfficeID       string
	OfficeName     string
	WalletAddress  string
	IdempotencyKey string
	Check          func(items []CartItem) (int64, error)
}

// Failure says why an order failed.
type Failure struct {
	Code      string // failure_code, e.g. insufficient_funds
	Attention string // attention_reason, e.g. payment_mismatch; empty for a plain refusal
	Notify    bool   // queue the "payment failed" email (no coins were taken)
}

// Actor is who made an admin change.
type Actor struct {
	Email  string
	UserID string
}

// SystemActor is used for changes made by jobs and checkout.
var SystemActor = Actor{Email: "system", UserID: "system"}
