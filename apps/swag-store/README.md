# Swag Store

Employees spend O2C coins on WSO2 swag and collect it at their office.

- [backend/](backend/): the Go API (Gin, MySQL, Choreo).
- [docs/](docs/): design spec, backend and frontend specs, implementation plan, threat model.
- The UI is a feature of One WSO2 (`one-wso2/webapp/src/features/swag-store`).
- Payments go through this repo's [transaction-service](../../operations/transaction-service/).
