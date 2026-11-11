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

package orders

import (
	"errors"
	"testing"
)

func TestTransition(t *testing.T) {
	cases := []struct {
		from, to Status
		actor    Actor
		ok       bool
	}{
		{PendingPayment, Confirmed, ActorSystem, true},
		{PendingPayment, Failed, ActorSystem, true},
		{PendingPayment, Confirmed, ActorReconciler, true},
		{PendingPayment, Failed, ActorReconciler, true},
		{Failed, Confirmed, ActorReconciler, true},
		{Failed, Confirmed, ActorSystem, false},
		{Confirmed, ReadyForPickup, ActorFulfilment, true},
		{ReadyForPickup, Collected, ActorFulfilment, true},
		{Confirmed, Collected, ActorFulfilment, false},
		{Confirmed, ReadyForPickup, ActorSystem, false},
		{PendingPayment, ReadyForPickup, ActorFulfilment, false},
		{Collected, ReadyForPickup, ActorFulfilment, false},
		{Failed, ReadyForPickup, ActorFulfilment, false},
		{Confirmed, Failed, ActorFulfilment, false},
	}
	for _, tc := range cases {
		err := Transition(tc.from, tc.to, tc.actor)
		if (err == nil) != tc.ok {
			t.Errorf("%s → %s by %v: err = %v, want ok=%v", tc.from, tc.to, tc.actor, err, tc.ok)
		}
		if err != nil && !errors.Is(err, ErrTransition) {
			t.Errorf("error is not ErrTransition: %v", err)
		}
	}
}

func TestNumbersAndReferences(t *testing.T) {
	if Number(10482) != "SS-10482" {
		t.Fatal(Number(10482))
	}
	ref := PaymentReference("0192f3a4-5b6c-7d8e-9f01-23456789abcd")
	if ref != "swag:ord:0192f3a4-5b6c-7d8e-9f01-23456789abcd" || len(ref) > 66 {
		t.Fatal(ref)
	}
	id, ok := OrderIDFromReference(ref)
	if !ok || id != "0192f3a4-5b6c-7d8e-9f01-23456789abcd" {
		t.Fatalf("id = %q %v", id, ok)
	}
	for _, bad := range []string{"swag:ord:", "other:ord:x", "swag:ord:not-a-uuid", "0x1234"} {
		if _, ok := OrderIDFromReference(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestParseStatus(t *testing.T) {
	if s, ok := ParseStatus("READY_FOR_PICKUP"); !ok || s != ReadyForPickup {
		t.Fatal(s, ok)
	}
	if _, ok := ParseStatus("SHIPPED"); ok {
		t.Fatal("unknown status parsed")
	}
}
