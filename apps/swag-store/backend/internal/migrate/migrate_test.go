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

package migrate

import (
	"testing"
	"testing/fstest"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/migrations"
)

func TestFilesSortedAndFiltered(t *testing.T) {
	fsys := fstest.MapFS{
		"0002_b.sql":    {Data: []byte("SELECT 1;")},
		"0001_a.sql":    {Data: []byte("SELECT 1;")},
		"0010_c.sql":    {Data: []byte("SELECT 1;")},
		"embed.go":      {Data: []byte("package migrations")},
		"README.md":     {Data: []byte("x")},
		"0003_d.sql.gz": {Data: []byte("x")},
	}
	got, err := Files(fsys)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"0001_a.sql", "0002_b.sql", "0010_c.sql"}
	if len(got) != len(want) {
		t.Fatalf("files = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("files = %v, want %v", got, want)
		}
	}
	latest, err := Latest(fsys)
	if err != nil || latest != "0010_c.sql" {
		t.Fatalf("latest = %q, %v", latest, err)
	}
}

func TestFilesRejectsBadNames(t *testing.T) {
	for _, name := range []string{"1_a.sql", "00001_a.sql", "0001-a.sql", "0001_.sql"} {
		fsys := fstest.MapFS{name: {Data: []byte("SELECT 1;")}}
		if _, err := Files(fsys); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	fsys := fstest.MapFS{"0001_a.sql": {}, "0001_b.sql": {}}
	if _, err := Files(fsys); err == nil {
		t.Error("duplicate number accepted")
	}
}

func TestEmbeddedMigrationsListed(t *testing.T) {
	latest, err := Latest(migrations.FS)
	if err != nil || latest != "0002_product_photo_paths.sql" {
		t.Fatalf("latest = %q, %v", latest, err)
	}
}
