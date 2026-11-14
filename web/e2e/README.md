# Playwright E2E

Run `npm run e2e` from `web/`. The suite starts Vite on `127.0.0.1:4174` and
runs Chromium in desktop and 390x844 mobile projects.

All `/api` requests and `/api/ws` are intercepted in `support/mock-api.ts`.
No Go server, production endpoint, browser session, or real credential is
used. Failure artefacts are written under the gitignored `../outputs/playwright/`.

If Chromium is not installed yet, run `npx playwright install chromium` once.


Run `npm run e2e:real` from `web/` for the Linux real service smoke suite. First
run `npm --prefix web run build` from the repository root; Go embeds that output.
Go and Git must be on `PATH`. Playwright global setup builds the control plane
and the standalone P0-07 fake engine, then starts the real built-in Agent on a
dynamically assigned loopback port. It uses an isolated temporary SQLite WAL
database, home, workspace and Agent state; the runtime environment contains only
test settings. The WAL setting follows P0-07's isolation of the tracked SQLite
stream cancellation issue.

The browser creates an administrator, signs in, sees the built-in server online,
then creates a session and checks a visible reply for Codex, Claude and Gemini.
All HTTP and WebSocket traffic reaches the real control plane. The engine
executables are substitutes; this does not validate paid models or real CLI
compatibility. Teardown sends SIGTERM, waits for a clean exit and removes the
temporary directory; a shutdown timeout fails the run and kills its process group.

The suites have separate report and result directories under
`../outputs/playwright/`: `report` / `test-results` for mocked tests,
`real-report` / `real-test-results` for the smoke test, plus
`real-controlplane.log`. CI uploads these diagnostics on failure. The original
`e2e` command excludes the `real` directory and keeps its existing coverage.
