import { render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { DataProvider } from "../data/resources";
import { I18nProvider } from "../i18n";
import DashboardPage from "./DashboardPage";
afterEach(() => { vi.unstubAllGlobals(); localStorage.clear(); });
for (const engine of ["gemini", "claude"] as const) test(`shows total sessions and refreshes on ${engine} revisions`, async () => {
  localStorage.setItem("wio_language","en");
  let total = 6;
  vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({counts:{online:1,servers:1,projects:1,threads:total,deployments:0,alerts:0},deployments:[],alerts:[]}))));
  const view = (revision: number) => <I18nProvider><DataProvider revisions={{[engine]:revision}}><DashboardPage onNavigate={vi.fn()} /></DataProvider></I18nProvider>;
  const rendered = render(view(0));
  await screen.findByText("6 sessions");
  expect(screen.queryByText(/Codex sessions/)).not.toBeInTheDocument();
  total=7; rendered.rerender(view(1));
  await waitFor(() => expect(screen.getByText("7 sessions")).toBeInTheDocument(), {timeout:3000});
});
