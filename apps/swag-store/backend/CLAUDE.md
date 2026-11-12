# Swag Store backend — conventions

Go 1.27.4, Gin, slog, golang-jwt v5 + keyfunc v3, x/oauth2 client credentials, MySQL 8.4 with sqlc.
Style follows `cs-tools/apps/csm-portal/backend`, except the router (Gin, user decision B15) and the
store having its own database and in-process jobs. Spec: `wso2-coin/apps/swag-store/docs/backend-spec.md`.

## Layout and layering

`cmd/server` → `handler` → `checkout` / `repository` / `coins` / `hr` / `notifications` → `apierror`.
Handlers depend on small interfaces declared next to them, never on concrete clients. `catalog` and
`orders` are pure (no SQL, no HTTP). `reqctx` holds the user, correlation id and user assertion in the
request context so slog and the clients never import Gin.

## Rules that are easy to break

- **Payee.** `checkout.payRequest` is the only code that builds a `coins.PayRequest`, and `toAddress` is
  always the treasury pin. Never add a payee field to a request type. A payment is confirmed only after the
  reply check (`Service.matches`).
- **References.** Every payment path uses `orders.PaymentReference(orderID)` (`swag:ord:<id>`), so retries
  are idempotent upstream.
- **Stock.** Change stock only through the conditional UPDATEs in `queries/catalog.sql`
  (`ReserveStock`, `CommitStock`, `ReleaseStock`, `AddStockOnHand`), each with a `swag_inventory_movements` row.
- **Admin writes** run in `inTx` together with an `audit(...)` row.
- **Emails** are queued with `enqueue` in the same transaction as the change, with a dedupe key. The outbox
  job sends them. No email shows a wallet balance or address.
- **Errors.** Clients branch on `errorCode`. Messages are fixed `ErrMsg*` constants. Upstream bodies are
  logged, never returned (`mapUpstreamErrorGeneric`).
- **Logs.** Never log emails, names, balances, bodies or tokens. `userID` and `correlationID` are added
  by the slog handler.
- **SQL.** Every value is a parameter. Dynamic lists (`SearchProducts`, `SearchOrders`) build fixed
  fragments and pass values as `?`; sorts come from a whitelist.
- **Requests.** Decode with `readJSON` (1 MiB cap, unknown fields refused). Validate UUID params with
  `uuidParam`; order numbers with `orderNoParam`.
- **Another user's row** answers 404, never 403.

## How to add an endpoint

1. Add the handler method (and its interface methods) in `internal/handler/<resource>.go`.
2. Add a row to `routeTable` in `cmd/server/routes.go` with the narrowest `Perm*` and a rate limit if it writes.
   Use the same Gin param name as neighbouring paths (Gin panics on `:id` vs `:slug` in one segment).
3. Add the row to `expectedPerms` in `cmd/server/routes_test.go` (backend-spec §6).
4. Document the operation in `openapi.yaml` with a `403` response.
5. If it writes, add the audit row and, where needed, the outbox row in the repository transaction.

## Gotchas

- sqlc with MySQL ignores `emit_pointers_for_null_types`; nullable columns are `sql.Null*`. Enum constants
  keep the `Swag…` prefix (`store.SwagOrdersStatusCONFIRMED`); compare with `string(x)` when simpler.
- `CURSOR` is a MySQL reserved word: the job cursor column is `job_cursor`.
- `swag_stock_alerts.pending_variant_id` is a generated column; the unique key on it allows one pending
  alert per user and variant.
- Integration tests skip unless `SWAG_TEST_DATABASE_URL` is set. Run them before every PR that touches
  `repository`, `checkout` or `migrations`.
- After changing `migrations/` or `queries/`, run `make generate` and commit `internal/store`.
