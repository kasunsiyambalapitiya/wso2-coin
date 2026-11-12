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

// Package testdb gives integration tests a fresh, migrated MySQL database.
// Every helper skips the test unless SWAG_TEST_DATABASE_URL is set to a
// go-sql-driver DSN whose user may create and drop databases.
package testdb

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/migrate"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/migrations"
)

// EnvVar names the DSN of the test server.
const EnvVar = "SWAG_TEST_DATABASE_URL"

// OpenEmpty creates an empty database for the test and drops it afterwards.
func OpenEmpty(t testing.TB) *sql.DB {
	t.Helper()
	raw := os.Getenv(EnvVar)
	if raw == "" {
		t.Skipf("%s not set; skipping integration test", EnvVar)
	}
	base, err := mysql.ParseDSN(raw)
	if err != nil {
		t.Fatalf("%s: %v", EnvVar, err)
	}
	var b [6]byte
	_, _ = rand.Read(b[:])
	name := "swag_test_" + hex.EncodeToString(b[:])

	admin := base.Clone()
	admin.DBName = ""
	adminDB, err := sql.Open("mysql", admin.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := adminDB.ExecContext(ctx, "CREATE DATABASE `"+name+"`"); err != nil {
		t.Fatalf("create test database: %v", err)
	}

	cfg := base.Clone()
	cfg.DBName = name
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	cfg.MultiStatements = true
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	cfg.Params["time_zone"] = "'+00:00'"
	cfg.Params["transaction_isolation"] = "'READ-COMMITTED'"
	// The production session mode (cmd/server/config.go), so strict-mode errors show up here.
	cfg.Params["sql_mode"] = "'STRICT_ALL_TABLES,NO_ZERO_IN_DATE,NO_ZERO_DATE,ERROR_FOR_DIVISION_BY_ZERO,NO_ENGINE_SUBSTITUTION,ONLY_FULL_GROUP_BY'"
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(40)
	dsns.Store(db, cfg.FormatDSN())
	t.Cleanup(func() {
		dsns.Delete(db)
		_ = db.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = adminDB.ExecContext(ctx, "DROP DATABASE `"+name+"`")
		_ = adminDB.Close()
	})
	return db
}

// dsns maps each open test pool to its DSN, for SecondPool.
var dsns sync.Map

// SecondPool opens another connection pool on db's test database, for code
// that keeps connections apart from the main pool (the job lock pool). It is
// closed before the database is dropped.
func SecondPool(t testing.TB, db *sql.DB) *sql.DB {
	t.Helper()
	dsn, ok := dsns.Load(db)
	if !ok {
		t.Fatal("testdb: SecondPool needs a pool from Open or OpenEmpty")
	}
	other, err := sql.Open("mysql", dsn.(string))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	return other
}

// Open returns a fresh database with every migration applied.
func Open(t testing.TB) *sql.DB {
	t.Helper()
	db := OpenEmpty(t)
	if _, err := migrate.Apply(context.Background(), db, migrations.FS); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	return db
}
