# Safe database startup

The user approved separating normal database connection, schema initialization and test resets, then requested the complete fix. Only Go changes are authorized. The frontend remains a later task.

## Decisions

- Normal server and consumer startup must never execute DDL or delete data. Both connect and check required tables/columns before starting Redis, Kafka or accepting traffic.
- Require `DANMAKUFLOW_MYSQL_DSN`; do not fall back to a built-in password or database. Reject missing/invalid configuration without printing credentials. Enable datetime parsing and bounded connection timeouts.
- Provide an explicit `cmd/migrate` command. It creates missing tables with fixed `CREATE TABLE IF NOT EXISTS` statements matching current models, then checks required columns. It never alters or replaces existing tables. A partial initialization can safely be retried.
- Normal startup fails if required tables/columns cannot be read. An incompatible existing schema requires a separately reviewed migration; no automatic repair is attempted.
- Remove data-reset behavior entirely. No destructive reset command is needed for this fix.

## Approaches

Removing only `DropTable` would leave automatic schema changes on every restart. Moving `AutoMigrate` to a command separates execution, but still permits alterations to existing columns. Fixed, create-only initialization is selected for this small existing schema because the immediate goal is preserving existing data. A general versioned migration framework is outside this fix.

## Validation and delivery

The user explicitly allowed lightweight single-process tests and compilation for this fix. No live MySQL connections, infrastructure startup, integration tests, benchmarks or Java changes. Use the real GORM MySQL dialect with a mock SQL connection to assert the SQL boundary, error propagation, repeated startup and repeated initialization. Report this limit rather than claiming real-MySQL validation.

Keep commits focused and individually verified. Existing authorization includes pushing the completed commits to `JavaLyHn/DanmakuFlow`.
