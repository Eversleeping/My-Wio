import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { UserInputPrompt } from "./components/UserInputPrompt";
import { I18nProvider } from "./i18n";
import type { Approval } from "./types";

afterEach(() => { vi.unstubAllGlobals(); window.localStorage.clear(); });
test("submits selected and custom answers, preserves failed input, and prevents resubmission", async () => {
  window.localStorage.setItem("wio_language", "en");
  const item: Approval = { id: "input", thread_id: "thread", request_id: "7", kind: "item/tool/requestUserInput", status: "pending", title: "Question", expires_at: "2099-01-01", detail: { questions: [
    { id: "choice", header: "Choice", question: "Pick a choice", options: [{ label: "A", description: "First option" }, { label: "B", description: "Second option" }] },
    { id: "text", header: "Details", question: "Add details", isOther: true, isSecret: true }
  ] } };
  const fetch = vi.fn().mockResolvedValueOnce(new Response(JSON.stringify({ error: "Try again" }), { status: 500 })).mockResolvedValueOnce(new Response("{}", { status: 202 }));
  vi.stubGlobal("fetch", fetch);
  const done = vi.fn();
  render(<I18nProvider><UserInputPrompt item={item} onDecided={done} /></I18nProvider>);
  const user = userEvent.setup();
  expect(screen.getByRole("button", { name: "Submit answers" })).toBeDisabled();
  await user.click(screen.getByRole("radio", { name: "A First option" }));
  const input = screen.getByLabelText("Your answer");
  expect(input).toHaveAttribute("type", "password");
  await user.type(input, "custom reply");
  await user.click(screen.getByRole("button", { name: "Submit answers" }));
  expect(await screen.findByRole("alert")).toBeInTheDocument();
  expect(input).toHaveValue("custom reply");
  await user.click(screen.getByRole("button", { name: "Submit answers" }));
  await waitFor(() => expect(done).toHaveBeenCalledOnce());
  expect(screen.getByRole("button", { name: "Answers sent" })).toBeDisabled();
  expect(JSON.parse(fetch.mock.calls[1][1].body)).toEqual({ decision: "answered", answers: { choice: { answers: ["A"] }, text: { answers: ["custom reply"] } } });
});
