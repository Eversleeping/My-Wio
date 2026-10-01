import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { useCodexModels } from "./codexModels";
import { CodexModelPicker } from "./components/CodexModelPicker";
import { I18nProvider } from "./i18n";

afterEach(() => { vi.unstubAllGlobals(); window.localStorage.clear(); });
function Picker({ workspaceID }: { workspaceID: string }) {
  const catalogue = useCodexModels(workspaceID);
  const [value, setValue] = useState("");
  return <CodexModelPicker value={value} onChange={setValue} models={catalogue.models} loading={catalogue.loading} error={catalogue.error} onRefresh={catalogue.refresh} allowServerDefault />;
}
function response(body: unknown, status = 200) { return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }); }

test("loads server models, refreshes after CLI changes, and isolates workspaces", async () => {
  window.localStorage.setItem("wio_language", "en");
  let version = "new-model";
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.endsWith("/refresh")) return response({ operation_id: "op" }, 202);
    const model = url.includes("/workspaces/second/") ? "second-server-model" : version;
    return response({ status: "succeeded", supported: true, data: [{ id: "picker-id", model, displayName: model }] });
  });
  vi.stubGlobal("fetch", fetchMock);
  const user = userEvent.setup();
  const { rerender } = render(<I18nProvider><Picker workspaceID="first" /></I18nProvider>);
  expect(await screen.findByRole("option", { name: "new-model" })).toBeInTheDocument();
  expect(screen.queryByRole("option", { name: /GPT-5.6/ })).not.toBeInTheDocument();
  await user.selectOptions(screen.getByLabelText("Model override"), "new-model");
  version = "upgraded-model";
  await user.click(screen.getByRole("button", { name: "Refresh models" }));
  expect(await screen.findByRole("option", { name: "upgraded-model" })).toBeInTheDocument();
  expect(screen.getByLabelText("Model override")).toHaveValue("new-model");
  rerender(<I18nProvider><Picker workspaceID="second" /></I18nProvider>);
  expect(await screen.findByRole("option", { name: "second-server-model" })).toBeInTheDocument();
  expect(screen.queryByRole("option", { name: "upgraded-model" })).not.toBeInTheDocument();
  expect(fetchMock.mock.calls.some(([url]) => String(url).endsWith("/workspaces/second/codex/models/refresh"))).toBe(true);
});

test("waits for snapshot completion and keeps custom input usable when unavailable", async () => {
  window.localStorage.setItem("wio_language", "en");
  let polls = 0;
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    if (String(input).endsWith("/refresh")) return response({}, 202);
    polls++;
    return response(polls === 1 ? { status: "refreshing", supported: true, data: [] } : { status: "unsupported", supported: false, reason: "model/list unsupported" });
  }));
  const user = userEvent.setup();
  render(<I18nProvider><Picker workspaceID="old-agent" /></I18nProvider>);
  await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("model/list unsupported"));
  await user.selectOptions(screen.getByLabelText("Model override"), "__custom__");
  await user.type(screen.getByLabelText("Custom model name"), "provider-model");
  expect(screen.getByLabelText("Custom model name")).toHaveValue("provider-model");
  await user.selectOptions(screen.getByLabelText("Model override"), "");
  expect(screen.getByLabelText("Model override")).toHaveValue("");
});
