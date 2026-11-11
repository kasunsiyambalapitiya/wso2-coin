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

package repository

import (
	"testing"
	"time"
)

func TestLimitSinceNeverZeroDate(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	if got := limitSince(now, nil); got.Year() < 1970 {
		t.Fatalf("no window = %v; MySQL strict mode rejects a zero date", got)
	}
	days := 30
	if got := limitSince(now, &days); !got.Equal(now.AddDate(0, 0, -30)) {
		t.Fatalf("30-day window = %v", got)
	}
}
