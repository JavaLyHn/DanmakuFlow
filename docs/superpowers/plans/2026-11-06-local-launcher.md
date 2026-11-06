# Local Backend Start/Stop Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Open the existing room page from a Windows shortcut, start its local dependencies and Go processes, and stop only processes created by this launcher after draining accepted work.

**Architecture:** A loopback Go launcher serves the built Vue page and authenticated, fixed start/stop operations. Persisted process identities distinguish owned and external services; Go children communicate readiness and cooperative stop results through private files. Protocol probes and process adapters are injected for isolated tests.

**Tech Stack:** Go 1.25, existing Gin/Sarama/go-redis/GORM dependencies, Windows APIs through existing x/sys, PowerShell, Vue 3/TypeScript/Vite.

**Spec:** `docs/superpowers/specs/2026-11-06-local-launcher-design.md`.

## Global Constraints

- Go repository only; Java remains paused. No destructive schema changes, implicit initialization, built-in credentials, load tests, or live database verification.
- Launcher `127.0.0.1:4174`, business server `8080`, MySQL `3306`, Redis `6379`, Kafka `9092`; one instance of each. Go `GOMAXPROCS=2`.
- Start/stop are serialized. External processes are never stopped. Timeout is not permission to kill. Browser refresh does not start or stop anything.
- MySQL configuration and secrets remain local; DSN uses `DANMAKUFLOW_MYSQL_DSN`. No secret in API, logs, command line or Git.
- Builds/tests are serial and limited. Local Go executable tests were blocked by Windows application control: compile locally, execute fake-service tests in CI; do not bypass that policy or claim local RED/GREEN execution.
- Tests are authored before behavior. For Go regressions that cannot run locally, CI mutation checks demonstrate the test detects the removed invariant, then run the restored suite.
- Work in the existing isolated worktree, feature branch. Root coordinates all Git commits, pushes, builds and tests; agents edit only their assigned files.

## Review Focus

1. Stale PID/port replacement must never allow stopping an unrelated process (Tasks 2/5).
2. Repeated POST requests and multiple tabs must never create duplicate services (Tasks 5/6).
3. Shutdown racing a WebSocket callback or consumer rebalance must not deadlock or skip accepted work (Tasks 3/4).
4. A rejected/failed batch must not be hidden by a later partition offset (Task 4).
5. Missing configuration, lost launcher and partial startup must leave an actionable page without secrets (Tasks 5/6/7).

## Shared interfaces

`internal/runcontrol` owns the private file protocol. `Request` has `ID string`, `Action string`, `Targets map[int32]int64`, and `TimeoutMillis int64`. `Status` has `PID int`, `RunID`, `Component`, `Phase`, `Error`, `RequestID` strings. Child environment: `DANMAKUFLOW_RUN_DIR`, `DANMAKUFLOW_RUN_ID`; no control without both. `FromEnv(component string) (*Child,error)`, `(*Child).Report(phase string, err error)`, `(*Child).Serve(ctx context.Context, stop func(context.Context, Request) error) error`. Nil child is supported. `Serve` accepts only fresh `stop` requests for its run, publishes stopping/failure/stopped, and returns nil only after a successful callback and persisted result. Readiness reports cannot override stopping/stopped. Parent `WriteRequest(dir,runID,request) error`, `ReadStatus(dir,runID) (Status,error)`.

`internal/localproc` owns native processes. `Identity {PID int; Created uint64; Executable string}`; `StartOptions {Executable string; Args []string; Dir string; Env []string; Console bool; Stdout,Stderr *os.File}`; `Start(StartOptions) (Identity,error)`, `Inspect(pid int) (Identity,error)`, `Matches(Identity) bool`, `Listeners(port int) ([]Identity,error)`, `StopConsole(ctx context.Context, id Identity) error`, `ConsoleHelper(encodedIdentity string) error`, `SecureDir(path string) error`. Child processes are detached from launcher cancellation. Launch the same executable with `-console-stop` for the transient helper. Unsupported OS adapters fail clearly; pure state-machine tests use fakes.

### Task 1: Private Go child control

Files: create `internal/runcontrol/control.go`, `control_test.go`.

- [ ] Write tests for absent/malformed env, foreign run IDs/PIDs, atomic state, duplicate requests, bounded timeout, retry after stop failure, cancellation without implicit stop, and readiness not overriding stopping.
- [ ] Implement the shared interface using bounded JSON files in a restricted run directory, atomic replacement, a single polling worker and idempotent request IDs.
- [ ] Compile locally; validate behavior and a duplicate-command mutation in isolated CI. Commit only the package and tests.

### Task 2: Windows process identity and console control

Files: create `internal/localproc/process.go`, `process_windows.go`, `process_other.go`, tests; `.github/workflows/windows.yml` later integrated by root.

- [ ] Write fake/isolated-process tests for creation time mismatch, executable mismatch, listener PID resolution, independent hidden console, unknown console refusal, and timeout without termination.
- [ ] Implement shared interface with Windows process handles, creation times, full executable paths and native listener lookup. Use CREATE_NEW_CONSOLE + hidden window only for owned MySQL/Kafka; no broad process-name kills. Dedicated helper validates console membership before Ctrl+C.
- [ ] Cross-compile and run short fake processes in serial Windows CI. No real infrastructure. Commit adapter independently.

### Task 3: Drain server work before exiting

Files: `cmd/server/main.go`, `internal/ws/{manager,client,router}.go`, new lifecycle/test files, `internal/api/chat_handler.go`, necessary Redis/Kafka wrappers in `internal/infra`.

- [ ] Write tests for quiescing registration/dispatch, pending message drain, last-room likes, producer flush errors and concurrent unregister/shutdown.
- [ ] Add `Manager.Shutdown(context.Context) error` and an admission gate. Stop inputs before closing work queues; explicitly close/wait WebSockets, join workers/subscriptions/stat tasks, flush likes and producer, collect errors. Keep normal protocol behavior.
- [ ] Use explicit `http.Server`/listener; publish runcontrol ready after listening. Route private stop and OS signals to the same bounded lifecycle; report failures without false success.
- [ ] Compile, isolated CI tests/mutation, then focused commit. Do not alter consumer files or database startup guards.

### Task 4: Consumer drain and commit correctness

Files: `cmd/consumer/main.go`, `internal/consumer/handler.go` (remove obsolete commented implementation), new handler tests, `internal/repo/message_repo.go` context-aware write.

- [ ] Write tests for no mark before DB success, failed batch cannot be bypassed by later offsets, ordered ignored packets, session queue replacement, cancellation drain, stop target offsets and commit failure.
- [ ] Preserve batching. Queue message metadata with rows; mark only contiguous successfully handled records after transaction success; retain failed work/backpressure. Use per-session channels and bounded final flush contexts.
- [ ] Expose ready when assigned partitions/worker are active. On runcontrol stop, consume through provided fixed per-partition next-offset targets; verify committed offsets with broker responses, then leave the group and close SQL. Timeout leaves a truthful stop failure and does not kill dependencies.
- [ ] Compile and isolated CI tests/mutation, then focused commit. Do not edit server or `internal/infra/kafka.go` owned by Task 3.

### Task 5: Launcher orchestration and HTTP security

Files: create `cmd/launcher/main.go`, `internal/launcher/{config,manager,runtime,http}.go` and corresponding tests.

- [ ] Write tests against fake process/probe/control adapters covering ordered starts, reused services, stale ownership, unknown consumers, serialized operations, partial failure, retained dependencies, graceful-stop result verification and retries.
- [ ] Load strict local JSON config (paths for MySQL EXE/config, Redis EXE/config/workdir, Java/Kafka home/config; build dir and repo root). Validate complete configuration before spawning. Use bounded per-component append logs and persist instance identity/run records.
- [ ] Implement start probes: MySQL OpenDB+CheckSchema, Redis PING, Kafka metadata/topic leaders/group identity. Never migrate/format. Spawn prebuilt Go children with DSN only where required. Cache status, periodically recheck active instances, remove ready on failure.
- [ ] Implement stop: validate ownership; server drain/result/exit; snapshot Kafka next offsets; consumer drain/result/exit; owned Kafka/Redis/MySQL normal stop, preserving external dependencies. Redis SHUTDOWN uses an already verified connection with reconnect/retries disabled.
- [ ] Serve static page plus `/api/local/session`, `/api/local/status`, `/api/local/start`, `/api/local/stop` and fixed component log-tail lookup. Exact Host/Origin, no CORS, random token header on mutating calls; no commands/paths supplied by browser. GET has no lifecycle side effects.
- [ ] Serial compile/CI fake tests and focused commits for orchestration and HTTP boundary.

### Task 6: Existing page integration

Files: create `web/src/useLocalBackend.ts`, `LocalBackendPanel.vue`, unit tests; modify `App.vue`, `style.css`, add `web/e2e/launcher.spec.ts`.

- [ ] Write failing Vitest tests for no implicit start, token-bearing explicit actions, status updates/failures, retry, disposal and unavailable capability. Run serial npm test and inspect RED.
- [ ] Implement one compact panel: start, stop owned services, five statuses, progress, bounded error detail, external-instance labels, Redis persistence notice. Same-origin feature detection only; absent endpoint hides panel on ordinary server/Vite pages.
- [ ] Pause room connection/sending only when the selected business port is being stopped. Retain actual WebSocket-driven disconnection state and all current room semantics.
- [ ] Verify serial unit/build and mocked browser flows including narrow layout, two tabs/deduplicated starts, partial failure/stop retry, external-instance protection. Commit page changes independently.

### Task 7: Windows entry, build, configuration and delivery

Files: `Start-DanmakuFlow.cmd`, `scripts/{start-local,setup-local,build-local}.ps1`, `config/launcher.example.json`, `.gitignore`, workflow changes, README/development/design status updates.

- [ ] First setup discovers installed tool paths, requests DSN privately, persists DPAPI current-user secret under restricted `.local/`; no database operation. Repeated launch loads current config/secret, starts hidden launcher once and opens 4174. Missing artifacts give clear build instructions.
- [ ] Build script uses serial npm build and Go `-p 1` builds with cache paths when available; no startup and no benchmark. Use prebuilt executables for daily startup.
- [ ] Add serial Windows fake-process checks and Linux regression/mutation tests; retain existing DB guard mutations and Web tests. All infrastructure remains mocked/absent.
- [ ] Fresh final review of ownership, shutdown and frontend; fix material findings with regression tests, verify CI at exact code SHA, then merge/push focused commits without rewriting history. Build/copy ignored local artifacts and setup entry into main checkout without starting infrastructure.
- [ ] Document one-time local setup/build, daily two-button operation, failure/retry, preserved external services, Redis ephemeral data and actual test evidence/remaining live-environment limitation.
