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

// Package orders holds the pure order rules: statuses, the state machine,
// order numbers and payment references. No SQL, no HTTP.
package orders

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Status is an order status, stored as the swag_orders.status enum.
type Status string

const (
	PendingPayment Status = "PENDING_PAYMENT"
	Confirmed      Status = "CONFIRMED"
	Failed         Status = "FAILED"
	ReadyForPickup Status = "READY_FOR_PICKUP"
	Collected      Status = "COLLECTED"
)

// ParseStatus accepts the stored spelling only.
func ParseStatus(s string) (Status, bool) {
	switch st := Status(s); st {
	case PendingPayment, Confirmed, Failed, ReadyForPickup, Collected:
		return st, true
	}
	return "", false
}

// Actor is who asks for a transition.
type Actor int

const (
	ActorSystem     Actor = iota + 1 // checkout and the payment reconciler's normal path
	ActorReconciler                  // the payment and treasury reconcilers
	ActorFulfilment                  // a Fulfilment Ops admin
)

func (a Actor) String() string {
	switch a {
	case ActorSystem:
		return "system"
	case ActorReconciler:
		return "reconciler"
	case ActorFulfilment:
		return "fulfilment"
	}
	return "unknown"
}

// ErrTransition means the state machine does not allow the change.
var ErrTransition = errors.New("order transition not allowed")

// Transition reports whether actor may move an order from one status to another
// (backend-spec §8.4).
func Transition(from, to Status, actor Actor) error {
	ok := false
	switch {
	case from == PendingPayment && (to == Confirmed || to == Failed):
		ok = actor == ActorSystem || actor == ActorReconciler
	case from == Failed && to == Confirmed:
		ok = actor == ActorReconciler
	case from == Confirmed && to == ReadyForPickup:
		ok = actor == ActorFulfilment
	case from == ReadyForPickup && to == Collected:
		ok = actor == ActorFulfilment
	}
	if !ok {
		return fmt.Errorf("%w: %s → %s by %s", ErrTransition, from, to, actor)
	}
	return nil
}

// Number formats an order number: SS-10482.
func Number(n int64) string { return fmt.Sprintf("SS-%d", n) }

const referencePrefix = "swag:ord:"

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// PaymentReference is the transaction-service reference of an order's payment.
// Every payment path keeps it, so a retry is idempotent upstream.
func PaymentReference(orderID string) string { return referencePrefix + orderID }

// OrderIDFromReference returns the order id inside a swag:ord: reference.
func OrderIDFromReference(ref string) (string, bool) {
	id, ok := strings.CutPrefix(ref, referencePrefix)
	if !ok || !uuidRe.MatchString(id) {
		return "", false
	}
	return id, true
}
