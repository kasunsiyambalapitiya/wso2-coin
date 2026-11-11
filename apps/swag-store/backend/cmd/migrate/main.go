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

// Command migrate applies migrations/*.sql that are not yet recorded in
// swag_applied_migrations (make migrate). It needs schema-owner rights; the
// service's own database user does not have them.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/migrate"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/migrations"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
}

func run() error {
	dsn, err := migrationDSN()
	if err != nil {
		return err
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	applied, err := migrate.Apply(ctx, db, migrations.FS)
	for _, name := range applied {
		fmt.Println("applied", name)
	}
	if err != nil {
		return err
	}
	if len(applied) == 0 {
		fmt.Println("schema is current")
	}
	return nil
}

// migrationDSN reads DATABASE_URL, or DB_HOST, DB_PORT, DB_USER, DB_PASSWORD
// and DB_NAME, and enables multi-statement files.
func migrationDSN() (string, error) {
	var mc *mysql.Config
	if raw := os.Getenv("DATABASE_URL"); raw != "" {
		parsed, err := mysql.ParseDSN(raw)
		if err != nil {
			return "", fmt.Errorf("DATABASE_URL: %w", err)
		}
		mc = parsed
	} else {
		mc = mysql.NewConfig()
		host, port := os.Getenv("DB_HOST"), os.Getenv("DB_PORT")
		if port == "" {
			port = "3306"
		}
		if host == "" || os.Getenv("DB_USER") == "" || os.Getenv("DB_NAME") == "" {
			return "", fmt.Errorf("set DATABASE_URL, or DB_HOST, DB_USER, DB_PASSWORD and DB_NAME")
		}
		mc.Net, mc.Addr = "tcp", host+":"+port
		mc.User, mc.Passwd, mc.DBName = os.Getenv("DB_USER"), os.Getenv("DB_PASSWORD"), os.Getenv("DB_NAME")
		if os.Getenv("DB_TLS") != "false" {
			mc.TLSConfig = "true"
		}
	}
	mc.MultiStatements = true
	mc.ParseTime = true
	return mc.FormatDSN(), nil
}
