# Development policy

- This is `JavaLyHn/DanmakuFlow`, the active Go development repository.
- `JavaLyHn/DanmakuFlow-Java` is paused. Do not automatically port changes or edit Java implementation until the user explicitly requests synchronization or Java work.
- Keep each commit focused and verified. Do not force push or rewrite existing history.
- Go testing and load testing remain paused under the user's earlier instruction. Do not start infrastructure, connect to the existing MySQL database, or run the application as a verification shortcut. Follow later explicit instructions when validation resumes, with limited resources and no unnecessary parallel processes.
- The current server and consumer both call database initialization that drops and recreates tables. Address this deliberately before any real-environment startup; do not assume Java's fixes already exist here.
- Preserve copyright and license notices. Describe present capabilities and measurements accurately; do not claim unmeasured throughput or original authorship of copied code.
- Keep secrets, machine-local configuration, test outputs and `.superpowers/` artifacts out of Git.
