import { render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { I18nProvider } from "../i18n";
import { SessionIsolation } from "./SessionIsolation";

afterEach(() => localStorage.clear());

test("distinguishes unreported, disabled and enabled isolation", () => {
  localStorage.setItem("wio_language", "en");
  const { rerender } = render(<I18nProvider><SessionIsolation /></I18nProvider>);
  expect(screen.getByText("Isolation not reported")).toBeInTheDocument();
  rerender(<I18nProvider><SessionIsolation status={{ mode: "disabled" }} /></I18nProvider>);
  expect(screen.getByText("Isolation off")).toBeInTheDocument();
  rerender(<I18nProvider><SessionIsolation status={{ mode: "enabled" }} /></I18nProvider>);
  expect(screen.getByText("Session isolated")).toBeInTheDocument();
});

test("shows the unsupported reason and keeps the conversation indicator compact", () => {
  localStorage.setItem("wio_language", "en");
  const status = { mode: "unsupported" as const, reason: "Required kernel support is unavailable" };
  const { rerender, container } = render(<I18nProvider><SessionIsolation status={status} /></I18nProvider>);
  expect(screen.getByText(status.reason)).toBeInTheDocument();
  rerender(<I18nProvider><SessionIsolation status={status} compact /></I18nProvider>);
  expect(screen.getByText("Isolation unavailable").closest(".session-isolation")).toHaveAttribute("title", status.reason);
  expect(screen.queryByText(status.reason)).not.toBeInTheDocument();
  rerender(<I18nProvider><SessionIsolation status={{ mode: "disabled" }} compact /></I18nProvider>);
  expect(container).toBeEmptyDOMElement();
});

test("includes Chinese isolation state text", () => {
  localStorage.setItem("wio_language", "zh-CN");
  render(<I18nProvider><SessionIsolation status={{ mode: "enabled" }} /></I18nProvider>);
  expect(screen.getByText("会话已隔离")).toBeInTheDocument();
});
