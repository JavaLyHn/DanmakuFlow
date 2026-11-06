# Web client implementation plan

> **For agentic workers:** Use superpowers:executing-plans for the integration, with bounded parallel UI and hosting work.

**Goal:** Replace inconvenient terminal interaction with the approved single-page room interface.
**Architecture:** Vue maintains one WebSocket session using the existing protocol. Vite builds static files served by Gin; no additional production process. Video remains a static placeholder.
**Tech Stack:** Vue 3, TypeScript, Vite, native WebSocket, scoped CSS, lossless-json, Vitest and serial Playwright.
**Spec:** ../specs/2026-11-06-terminal-ui-design.md

## Constraints and decisions

- Go remains active; Java stays paused. Preserve licenses and database startup protections.
- Fixed local WebSocket endpoint, default port 8080 / room 1001; UID is a decimal uint64 string, generated when blank; name equals UID.
- Exact `1` means one like; preserve all other input including empty text and whitespace. No optimistic messages/counts, no ACK, no automatic reconnect.
- Browser error details are limited to available events; never invent a network root cause.
- Keep implementation small: one page, one session composable, one protocol module. Ordinary scrollable list with batched appends; no virtual-list framework or message eviction in this first implementation. Long uninterrupted sessions retain their messages and therefore use increasing memory.
- Local validation uses serial frontend checks and Go compilation only. Browser regression runs in isolated CI with mock WebSocket; never launch the real application or connect existing databases.

## Review focus

Large uint64 values, IME Enter, stale events after reconnect, disconnect while messages are buffered, narrow screens and long content. Cover protocol/session cases in Vitest and UI cases in Playwright.

## Tasks

- [ ] Protocol/session: add `web/package.json`, Vite/TS configuration, `src/protocol.ts`, `src/useRoomSession.ts` and focused unit tests. Verify raw inputs, uint64, connection/error/close lifecycle and stale callbacks. Commit after unit checks.
- [ ] Page: add `src/App.vue`, `src/style.css`, entry HTML/TS. Use session API; implement approved layout, scrolling, draft retention, keyboard/IME and empty/error states. Verify type check/build. Separate focused commit.
- [ ] Hosting: add `internal/webui` and static registration in `cmd/server/main.go`; test assets, no directory listings/traversal, preserve `/ws`, allow terminal use without build assets. Compile locally, run HTTP-only tests in existing serial CI. Separate focused commit.
- [ ] Browser checks and documentation: add serial mock-backed Playwright/CI, inspect desktop/mobile screenshots, update run commands and development status. Review complete diff, fix material findings, verify CI and push focused commits without rewriting history.
