# Development policy

- This is `JavaLyHn/DanmakuFlow`, the active Go development repository.
- `JavaLyHn/DanmakuFlow-Java` is paused. Do not automatically port changes or edit Java implementation until the user explicitly requests synchronization or Java work.
- Keep each commit focused and verified. Do not force push or rewrite existing history.
- For the safe database startup fix, the user explicitly authorized lightweight single-process Go tests and compilation. Keep validation serial and resource-limited, using mock SQL connections with the real GORM MySQL dialect. This authorization does not resume benchmarks, load tests, live database integration tests or infrastructure startup. Never connect to the user's existing MySQL database or run the application as a verification shortcut.
- Server and consumer startup must use `OpenDB` and `CheckSchema` without DDL or data writes. Require `DANMAKUFLOW_MYSQL_DSN`; never restore built-in credentials. Schema initialization belongs only in `cmd/migrate`, which creates missing tables without altering existing ones. Do not add destructive reset behavior or assume Java's fixes exist here.
- Preserve copyright and license notices. Describe present capabilities and measurements accurately; do not claim unmeasured throughput or original authorship of copied code.
- Keep secrets, machine-local configuration, test outputs and `.superpowers/` artifacts out of Git.
- For the subsequently authorized Docker migration, the user requested stopping the old project services and full single-instance functional verification using Docker. This permits the Docker setup and functional checks, not benchmarks. Preserve the native MySQL data and Kafka backup; do not mount or reset them. Any required Windows reboot remains user-controlled.
