# Safe Database Startup Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Preserve existing MySQL data on every normal server/consumer startup.

**Architecture:** Separate connection, read-only schema checks and explicit create-only initialization. Runtime processes fail before other services are initialized when database setup is missing.

**Tech Stack:** Go 1.25, existing GORM/MySQL driver, go-sqlmock for isolated SQL-boundary tests.

**Spec:** `docs/superpowers/specs/2026-11-06-safe-db-startup.md`

## Global Constraints

- Go only; Java and frontend implementation stay paused.
- Single-process lightweight tests and compilation only; no real database or infrastructure connections.
- No default credentials, automatic DDL, data reset, force push or history rewriting.

## Review Focus

- Missing/invalid DSN must fail before dialing and must not expose its password.
- Repeated normal startup must execute only connection and read-only checks.
- Missing tables/columns must stop both entrypoints before Kafka/Redis startup.
- Repeated or partially failed initialization must preserve existing tables and stop on errors.
- Connection/query failures must return errors and release owned SQL connections.

## Task 1: Remove destructive startup and configure the connection

Files: `internal/infra/db.go`, `internal/infra/db_test.go`, `cmd/server/main.go`, `cmd/consumer/main.go`, `go.mod`, `go.sum`.

Interfaces: `OpenDB() (*gorm.DB, error)` reads configuration; private `connectDB(gorm.Dialector) (*gorm.DB, error)` shares production and mock SQL paths. No schema migration takes place during connection.

- [ ] Write tests for missing/malformed DSN, datetime configuration, read-only connection, repeated connection and initialization failure.
- [ ] Verify a regression test rejects the old destructive initialization using a mock SQL connection, never a live database.
- [ ] Replace the old initializer and update both entrypoints to handle errors and close connections.
- [ ] Run targeted tests with `GOMAXPROCS=2 go test -p 1 -parallel 1 ./internal/infra`, check the diff, commit.

## Task 2: Explicit create-only schema initialization and runtime checks

Files: `internal/infra/schema.go`, `internal/infra/schema_test.go`, `cmd/migrate/main.go`, its tests, and both runtime entrypoints.

Interfaces: `CheckSchema(*gorm.DB) error` checks required columns with SELECT/LIMIT 0; `MigrateSchema(*gorm.DB) error` executes fixed create-only statements then checks the result. `cmd/migrate` uses `OpenDB` without runtime schema preconditions.

- [ ] Add tests for missing schema, no runtime writes, repeated create-only initialization, partial failure and command error handling.
- [ ] Implement checks and the standalone command; call checks before initializing other services in both runtime entrypoints.
- [ ] Run affected tests, full serial suite, build and vet; check the diff, commit.

## Task 3: Operations documentation and final review

Files: `README.md`, `docs/DATABASE.md`, `docs/DEVELOPMENT.md`, `AGENTS.md`.

- [ ] Document required environment configuration, explicit initialization, existing-schema handling, reset removal, account privileges and validation limits.
- [ ] Record the user's limited test authorization and the unchanged frontend/Java priorities.
- [ ] Obtain independent review; fix material issues and repeat only relevant validation.
- [ ] Commit documentation, fast-forward the main checkout and push each reviewed commit separately; verify remote HEAD.
