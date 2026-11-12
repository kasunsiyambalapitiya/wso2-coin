BINARY := server

.PHONY: all vet test test-integration build generate migrate seed fakes lint clean

all: build

vet:
	go vet ./...

# Unit tests only: no database, network or env vars needed.
test: vet
	go test -race ./...

# Integration tests on a real MySQL 8.4. SWAG_TEST_DATABASE_URL is a go-sql-driver DSN
# whose user may create and drop databases, e.g. root:pw@tcp(127.0.0.1:3306)/
test-integration:
	@test -n "$$SWAG_TEST_DATABASE_URL" || (echo "set SWAG_TEST_DATABASE_URL"; exit 1)
	go test -race -count=1 ./internal/repository/... ./internal/checkout/... ./internal/migrate/...

build: test
	go build -o $(BINARY) ./cmd/server

# Regenerates internal/store from migrations/ and queries/. The output is committed.
generate:
	sqlc generate

# Applies migrations/*.sql not yet in swag_applied_migrations. Needs schema-owner rights.
# Reads DATABASE_URL, or DB_HOST, DB_PORT, DB_USER, DB_PASSWORD, DB_NAME (DB_TLS=false locally).
migrate:
	go run ./cmd/migrate

seed:
	go run ./cmd/seed

fakes:
	go run ./cmd/fakes

lint:
	go vet ./...
	gosec -fmt=text ./...
	govulncheck ./...

clean:
	rm -f $(BINARY)
