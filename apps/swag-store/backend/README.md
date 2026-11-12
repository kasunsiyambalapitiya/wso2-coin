# Swag Store backend

Go service for the WSO2 Swag Store: catalog, cart, coin checkout, office pickup and stock alerts.
One WSO2 is its only client. It runs on Choreo and owns the MySQL database `hris_swag_store`.

Spec: `wso2-coin/apps/swag-store/docs/backend-spec.md`.

## Architecture

```
One WSO2 ──► Choreo gateway ──x-jwt-assertion──► Gin router
                                                   │  SecurityHeaders → CORS → CorrelationID → Auth → Logger → Recovery
                                                   │  route(method, path, Perm*, [rate limit], handler)
                                                   ▼
                       handler ──► checkout ──► coins (transaction-service: wallets, payments, ledger)
                          │           │
                          │           └──► repository ──► MySQL (sqlc in internal/store)
                          └──► hr (HR Entity GraphQL, 1 h cache)
```

| Package | Role |
|---|---|
| `cmd/server` | Composition root: config, DB, clients, handlers, route table, shutdown |
| `cmd/migrate` | `make migrate`: applies `migrations/*.sql` and records them |
| `internal/middleware` | Gin middleware chain and per-user rate limits |
| `internal/handler` | One file per resource, the access guard, request/response helpers |
| `internal/checkout` | Quote, place order, retry, payment reconciliation, treasury guard |
| `internal/repository` | Transactions over sqlc; stock via conditional UPDATEs; admin audit rows |
| `internal/catalog`, `internal/orders` | Pure rules: availability, line checks, state machine |
| `internal/coins`, `internal/hr`, `internal/notifications` | Upstream clients |

### Payments and the treasury

transaction-service accepts any payee, so the store guarantees it (spec §8.7):

- `checkout.payRequest` is the only code that builds a payment; `toAddress` is always
  `STORE_TREASURY_WALLET_ADDRESS`. No request type has a payee field and the decoder refuses unknown fields.
- The pin is checked against `GET /wallets/master/balance` before the first payment and every 10 minutes.
  A mismatch or a failed check pauses payments (`503 payments_paused`, `canPlace: false`,
  `/health/dependencies` → `treasury`).
- An order confirms only when the reply's `toAddress`, `amount` and `reference` match. Otherwise it fails
  with `attention_reason = payment_mismatch`.

## Developer guide

Requires Go 1.27.4 (the toolchain downloads it) and MySQL 8.4.

```bash
cp .env.example .env
docker run -d --name swag-mysql -e MYSQL_ROOT_PASSWORD=pw -e MYSQL_DATABASE=hris_swag_store -p 3306:3306 mysql:8.4
DATABASE_URL='root:pw@tcp(127.0.0.1:3306)/hris_swag_store' make migrate
go run ./cmd/server   # or: make build && ./server
curl -H "x-jwt-assertion: $JWT" http://localhost:8080/users/me
```

The token needs `email`, `userid` and (for admin) `groups` claims. With
`AUTH_TOKEN_VALIDATOR_ENABLED=false` any JWT shape is decoded without a signature check; startup refuses
that unless every upstream is loopback.

| Command | What it does |
|---|---|
| `make test` | `go vet` + unit tests with `-race`. No services needed |
| `make test-integration` | Integration tests on real MySQL (`SWAG_TEST_DATABASE_URL`, a user that may create databases) |
| `make generate` | `sqlc generate` (output in `internal/store` is committed) |
| `make lint` | `go vet`, `gosec`, `govulncheck` |

## Configuration reference

| Var | Required | Default | Description |
|---|---|---|---|
| `PORT` | No | `8080` | Listen port |
| `REST_READ_TIMEOUT` · `REST_WRITE_TIMEOUT` | No | `60s` | Server timeouts |
| `UPSTREAM_TIMEOUT` | No | `25s` | Every upstream call; `POST /payments` also has a 10 s limit |
| `CORS_ALLOWED_ORIGINS` | No | none | Comma list; local only (the gateway does CORS) |
| `JOBS_ENABLED` | No | `true` | `false` turns the jobs off on this replica |
| `AUTH_JWKS_ENDPOINT` · `AUTH_ISSUER` · `AUTH_AUDIENCE` | Yes | — | Token checks. Audience is a comma list |
| `AUTH_TOKEN_VALIDATOR_ENABLED` | No | `true` | `false` = decode only; loopback upstreams only |
| `AUTH_ALLOWED_EMAIL_DOMAINS` | No | `wso2.com` | Other domains get 403 `not_wso2` |
| `AUTH_CATALOG_MANAGER_GROUPS` · `AUTH_FULFILMENT_OPS_GROUPS` | No (warns) | none | Token `groups` that grant each role |
| `DATABASE_URL` or `DB_HOST` · `DB_PORT` · `DB_USER` · `DB_PASSWORD` · `DB_NAME` | Yes | port `3306` | MySQL. **(confirm)** the names Choreo injects for a database connection |
| `DB_TLS` · `DB_CA_CERT` | No | `true` · none | TLS to MySQL; PEM for a private CA (`\n` allowed) |
| `DB_MAX_OPEN_CONNS` | No | `10` | Pool size; idle = half |
| `OAUTH2_CLIENT_ID` · `OAUTH2_CLIENT_SECRET` · `OAUTH2_TOKEN_URL` | Yes | — | Shared client-credentials app (secret in a Choreo secret) |
| `TRANSACTION_SERVICE_BASE_URL` · `_SCOPES` | URL yes | — | wso2-coin transaction-service |
| `HR_ENTITY_BASE_URL` · `_SCOPES` | URL yes | — | HR Entity GraphQL endpoint |
| `HR_ENTITY_TOKEN_URL` · `_CLIENT_ID` · `_CLIENT_SECRET` | No | shared app | HR's own app, if it needs one |
| `EMAIL_BASE_URL` · `EMAIL_SCOPES` | No | none | email-service; unset = emails wait in the outbox |
| `EMAIL_FROM_ADDRESS` | With `EMAIL_BASE_URL` | — | Fixed From address |
| `EMAIL_DEBUG_RECIPIENTS` | No | none | Every email goes only here as "[Test copy]". Set in every non-production environment |
| `STORE_TREASURY_WALLET_ADDRESS` | Yes | — | The only payee a payment can name |
| `STORE_CONTACT_EMAIL` · `STORE_WEB_URL` | Yes | — | Reply-To and "Something wrong?" · base URL for email links |
| `IMAGE_URL_HOSTS` | No | `*.wso2.com,wso2.cachefly.net,*.blob.core.windows.net` | Allowed product image hosts (https) |
| `HR_CACHE_TTL` | No | `1h` | HR answer cache |
| `LOW_STOCK_THRESHOLD` · `PICKUP_REMINDER_DAYS` · `UNCOLLECTED_FLAG_DAYS` | No | `5` · `7` · `30` | Store rules |
| `STORE_TIMEZONE` | No | `Asia/Colombo` | Digest schedule and dates in emails |

Every base URL must be `https` (loopback `http` is allowed). Malformed values stop startup and are named.

## API surface

`openapi.yaml` documents every route; `TestEveryRegisteredRouteIsInOpenAPI` keeps it complete and
`TestRoutePermissionMatrix` checks every route against every role.

- **Shopper** (`PermAuthenticated`): `/users/me`, `/wallets`, `/categories`, `/products`, `/products/{slug}`,
  `/cart`, `/cart/summary`, `/cart/lines`, `/pickup-offices`, `/quotes`, `/orders`, `/orders/{orderNo}`,
  `/orders/{orderNo}/payments`, `/stock-alerts`.
- **Fulfilment Ops**: `/admin/pickup-offices…`.
- `GET /health` and `GET /health/dependencies` skip auth.

## Open items (confirm with the platform teams)

- The Choreo assertion for One WSO2 carries `email`, `userid` and `groups`.
- The transaction-service gateway passes `X-User-Assertion`, and its `USER_JWT_*` config accepts the store's assertion.
- The shared app's client id is mapped to the treasury wallet in `client_master_wallet`.
- The Choreo Go buildpack offers Go 1.27 and builds `./cmd/server`.
- email-service answers `GET /health` (used by `/health/dependencies`).
