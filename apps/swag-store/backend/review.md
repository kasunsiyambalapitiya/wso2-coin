# Pre-PR checklist — swag-store/backend

Walk this against the diff before every commit and PR. Call out anything that matches.

## Cross-cutting (from csm-portal)

- [ ] No secret, token, `.env` or real email/name in the diff, tests or docs. Use placeholders.
- [ ] No PII (email, name, balance) in a log line.
- [ ] No upstream or driver error body returned to a caller.
- [ ] Every SQL value is a parameter.
- [ ] A new route has the narrowest `Perm*`, a row in `expectedPerms`, an `openapi.yaml` operation with `403`,
      and no Gin param-name conflict with its neighbours.
- [ ] New behaviour has a test that failed first.
- [ ] Config stays flat, single-line env vars; a new role variable has no default.
- [ ] `go vet`, `gosec -fmt=text ./...` and `govulncheck ./...` are clean. Bump the `go` patch for a stdlib CVE.
- [ ] `make test-integration` passed on MySQL 8.4 if `repository`, `checkout` or `migrations` changed.
- [ ] `CLAUDE.md` updated for a new convention or gotcha.

## Store

- [ ] A stock change goes through a conditional UPDATE and writes a movement row.
- [ ] A payment path keeps the `swag:ord:<orderId>` reference.
- [ ] No code sets a payment's `toAddress` except `checkout.payRequest`, from `STORE_TREASURY_WALLET_ADDRESS`.
- [ ] A payment is confirmed only after the reply check.
- [ ] No email shows a balance or wallet address.
- [ ] A new admin write has its `swag_admin_actions` row in the same transaction.
- [ ] A new email is queued in the same transaction as its change, with a dedupe key.
