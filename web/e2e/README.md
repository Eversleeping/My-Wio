# Playwright E2E

Run `npm run e2e` from `web/`. The suite starts Vite on `127.0.0.1:4174` and
runs Chromium in desktop and 390x844 mobile projects.

All `/api` requests and `/api/ws` are intercepted in `support/mock-api.ts`.
No Go server, production endpoint, browser session, or real credential is
used. Failure artefacts are written under the gitignored `../outputs/playwright/`.

If Chromium is not installed yet, run `npx playwright install chromium` once.
