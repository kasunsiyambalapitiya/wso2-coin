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

package migrate_test

import (
	"context"
	"testing"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/migrate"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/testdb"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/migrations"
)

func TestMigrationsApplyOnEmptyDB(t *testing.T) {
	db := testdb.OpenEmpty(t)
	ctx := context.Background()

	if err := migrate.CheckCurrent(ctx, db, migrations.FS); err == nil {
		t.Fatal("empty schema passed the startup check")
	}
	applied, err := migrate.Apply(ctx, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) == 0 {
		t.Fatal("nothing applied")
	}
	again, err := migrate.Apply(ctx, db, migrations.FS)
	if err != nil || len(again) != 0 {
		t.Fatalf("re-run applied %v, %v", again, err)
	}
	if err := migrate.CheckCurrent(ctx, db, migrations.FS); err != nil {
		t.Fatalf("current schema failed the check: %v", err)
	}
}
