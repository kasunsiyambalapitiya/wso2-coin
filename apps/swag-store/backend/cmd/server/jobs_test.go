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
	"context"
	"testing"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/checkout"
)

type fixedTreasury checkout.TreasuryState

func (f fixedTreasury) Refresh(context.Context) checkout.TreasuryState {
	return checkout.TreasuryState(f)
}

func TestTreasuryCheckFailsUnlessTheTreasuryMatches(t *testing.T) {
	for state, wantErr := range map[checkout.TreasuryState]bool{
		checkout.TreasuryOK:       false,
		checkout.TreasuryMismatch: true,
		checkout.TreasuryError:    true,
	} {
		err := treasuryCheck(fixedTreasury(state))(context.Background())
		if (err != nil) != wantErr {
			t.Errorf("state %s: err = %v, want error %v", state, err, wantErr)
		}
	}
}
