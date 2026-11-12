import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { MessageImages } from "./components/MessageImages";
import MarkdownContent from "./MarkdownContent";
import { I18nProvider } from "./i18n";
import { WorkspaceImageContext } from "./components/WorkspaceImage";

afterEach(() => { vi.unstubAllGlobals(); window.localStorage.clear(); document.body.style.overflow = ""; });

test("opens images outside the clipped conversation and restores focus and scrolling", async () => {
  window.localStorage.setItem("wio_language", "en");
  document.body.style.overflow = "auto";
  const user = userEvent.setup();
  const { container, unmount } = render(<I18nProvider><div style={{ contain: "layout paint style", overflow: "hidden", height: 100 }}><MessageImages sources={["data:image/png;base64,AAAA"]} /></div></I18nProvider>);
  const thumbnail = screen.getByTitle("Open image");
  await user.click(thumbnail);
  const dialog = screen.getByRole("dialog");
  expect(container).not.toContainElement(dialog);
  expect(dialog.parentElement?.parentElement).toBe(document.body);
  expect(document.body.style.overflow).toBe("hidden");
  const close = within(dialog).getByRole("button", { name: "Close" });
  expect(close).toHaveFocus();
  await user.click(within(dialog).getByRole("img"));
  expect(dialog).toBeInTheDocument();
  await user.tab();
  expect(close).toHaveFocus();
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(thumbnail).toHaveFocus();
  expect(document.body.style.overflow).toBe("auto");
  await user.click(thumbnail);
  await user.click(screen.getByRole("dialog").parentElement!);
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  await user.click(thumbnail);
  await user.click(screen.getByRole("button", { name: "Close" }));
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  await user.click(thumbnail);
  unmount();
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(document.body.style.overflow).toBe("auto");
});

function preventNavigation() {
  const prevent = (event: MouseEvent) => event.preventDefault();
  document.addEventListener("click", prevent);
  return () => document.removeEventListener("click", prevent);
}

test("offers remote message images as links to their host without loading them", async () => {
  window.localStorage.setItem("wio_language", "en");
  const user = userEvent.setup();
  const restoreNavigation = preventNavigation();
  const { container } = render(<I18nProvider><MessageImages sources={["https://attacker.example/pixel.png?secret=1", "data:image/png;base64,AAAA"]} /></I18nProvider>);
  const link = screen.getByRole("link", { name: "Remote image attacker.example" });
  expect(link).toHaveAttribute("href", "https://attacker.example/pixel.png?secret=1");
  expect(link).toHaveAttribute("target", "_blank");
  expect(link).toHaveAttribute("rel", "noopener noreferrer");
  expect(link).toHaveAttribute("title", "Open the remote image from attacker.example in a new tab");
  await user.click(link);
  restoreNavigation();
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(Array.from(container.querySelectorAll("img"), image => image.getAttribute("src"))).toEqual(["data:image/png;base64,AAAA"]);
});

test("renders remote markdown images as placeholders and keeps embedded images inline", async () => {
  window.localStorage.setItem("wio_language", "en");
  const { container } = render(<I18nProvider><MarkdownContent text={"Chart ![build chart](https://attacker.example/c.png?d=secret) and ![inline](data:image/png;base64,AAAA)"} workspaceRoot="/srv/project" /></I18nProvider>);
  const link = await screen.findByRole("link", { name: "build chart attacker.example" });
  expect(link).toHaveAttribute("href", "https://attacker.example/c.png?d=secret");
  expect(link).toHaveAttribute("target", "_blank");
  expect(link).toHaveAttribute("rel", "noopener noreferrer");
  expect(Array.from(container.querySelectorAll("img"), image => image.getAttribute("src"))).toEqual(["data:image/png;base64,AAAA"]);
  expect(container.innerHTML).not.toContain("<img src=\"https://attacker.example");
});

test("opens embedded Markdown screenshots in a dialog", async () => {
  window.localStorage.setItem("wio_language", "en");
  const user = userEvent.setup();
  const view = render(<I18nProvider><MarkdownContent text="![Screenshot](data:image/png;base64,AAAA)" workspaceRoot="/repo" /></I18nProvider>);
  await user.click(screen.getByRole("button", { name: "Screenshot" }));
  const dialog = screen.getByRole("dialog", { name: "Screenshot" });
  expect(within(dialog).getByRole("img")).toHaveAttribute("src", "data:image/png;base64,AAAA");
  view.rerender(<I18nProvider><MarkdownContent text="![Screenshot](data:image/png;base64,AAAA)" workspaceRoot="/repo" onOpenFile={vi.fn()} /></I18nProvider>);
  expect(screen.getByRole("dialog", { name: "Screenshot" })).toBe(dialog);
  await user.click(within(dialog).getByRole("button", { name: "Original size" }));
  expect(within(dialog).getByRole("img").parentElement).toHaveClass("original");
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
});

test("loads multiple local Markdown images without changing the file panel snapshot", async () => {
  window.localStorage.setItem("wio_language", "en");
  const reads: string[] = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url === "/api/workspaces/workspace/image-preview" && init?.method === "POST") {
      const path = JSON.parse(String(init.body)).path as string;
      reads.push(path);
      return new Response(JSON.stringify({ operation_id: path }));
    }
    if (url.startsWith("/api/operations/")) return new Response(JSON.stringify({ status: "succeeded", result_data: JSON.stringify({ path: decodeURIComponent(url.slice("/api/operations/".length)), content: "AAAA", mime_type: "image/png", encoding: "base64" }) }));
    throw new Error(`Unexpected image request: ${url}`);
  }));
  render(<I18nProvider><WorkspaceImageContext.Provider value={{ workspaceID: "workspace", workspaceRoot: "/repo" }}><MarkdownContent text="![First](../screens/first.png) ![Second](/repo/screens/second.png) ![Blocked](../../outside.png)" workspaceRoot="/repo" filePath="docs/guide.md" /></WorkspaceImageContext.Provider></I18nProvider>);
  expect(await screen.findByRole("img", { name: "First" })).toHaveAttribute("src", "data:image/png;base64,AAAA");
  expect(await screen.findByRole("img", { name: "Second" })).toBeInTheDocument();
  await waitFor(() => expect(reads.sort()).toEqual(["screens/first.png", "screens/second.png"]));
  expect(screen.queryByRole("img", { name: "Blocked" })).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Second" }));
  expect(screen.getByRole("dialog", { name: "Second" })).toBeInTheDocument();
});
