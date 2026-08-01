import { fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import userEvent from "@testing-library/user-event";
import { expect, test, vi } from "vitest";
import { Dialog, DialogActions } from "./Dialog";

test("does not render while closed and keeps the existing styling hooks", () => {
  const { rerender } = render(<Dialog open={false} title="Preferences" onClose={vi.fn()}><p>Content</p></Dialog>);
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();

  rerender(<Dialog open title="Preferences" onClose={vi.fn()} wide className="preferences-dialog" closeLabel="Dismiss preferences"><p>Content</p><DialogActions><button>Save</button></DialogActions></Dialog>);
  const dialog = screen.getByRole("dialog", { name: "Preferences" });
  expect(dialog).toHaveAttribute("aria-modal", "true");
  expect(dialog).toHaveAttribute("aria-labelledby");
  expect(dialog).toHaveClass("dialog", "wide", "preferences-dialog");
  expect(screen.getByRole("heading", { name: "Preferences" })).toHaveAttribute("id", dialog.getAttribute("aria-labelledby"));
  expect(screen.getByRole("button", { name: "Dismiss preferences" })).toHaveAttribute("title", "Dismiss preferences");
  expect(screen.getByRole("button", { name: "Save" }).parentElement).toHaveClass("dialog-actions");
});

test("closes from the close button, Escape, and a backdrop click but not a dialog click", async () => {
  const user = userEvent.setup();
  const onClose = vi.fn();
  render(<Dialog open title="Delete item" onClose={onClose}><button>Keep item</button></Dialog>);

  await user.click(screen.getByRole("button", { name: "Close" }));
  await user.keyboard("{Escape}");
  await user.click(document.querySelector(".dialog-backdrop")!);
  await user.click(screen.getByRole("button", { name: "Keep item" }));

  expect(onClose).toHaveBeenCalledTimes(3);
});

test("initially focuses autofocus content before the first available control", () => {
  render(<Dialog open title="Account" onClose={vi.fn()}><button>First control</button><input aria-label="Account name" autoFocus /></Dialog>);
  expect(screen.getByRole("textbox", { name: "Account name" })).toHaveFocus();
});

test("focuses the first control and traps Tab and Shift+Tab within the dialog", async () => {
  const user = userEvent.setup();
  render(<><button>Outside</button><Dialog open title="Account" onClose={vi.fn()}><button>First control</button><button>Last control</button></Dialog></>);

  const close = screen.getByRole("button", { name: "Close" });
  const last = screen.getByRole("button", { name: "Last control" });
  expect(close).toHaveFocus();

  last.focus();
  await user.tab();
  expect(close).toHaveFocus();
  await user.tab({ shift: true });
  expect(last).toHaveFocus();
});

test("uses the dialog container as a safe focus fallback when controls are unavailable", async () => {
  const user = userEvent.setup();
  render(<Dialog open title="Information" onClose={vi.fn()}><p>No interactive content</p></Dialog>);

  const dialog = screen.getByRole("dialog", { name: "Information" });
  (screen.getByRole("button", { name: "Close" }) as HTMLButtonElement).disabled = true;
  await user.tab();
  expect(dialog).toHaveFocus();
});

function FocusRestoreExample() {
  const [open, setOpen] = useState(false);
  return <><button onClick={() => setOpen(true)}>Open account</button><Dialog open={open} title="Account" onClose={() => setOpen(false)}><button>Save account</button></Dialog></>;
}

test("restores focus to the opener after closing", async () => {
  const user = userEvent.setup();
  render(<FocusRestoreExample />);

  const opener = screen.getByRole("button", { name: "Open account" });
  await user.click(opener);
  await user.keyboard("{Escape}");
  expect(opener).toHaveFocus();
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
});

test("only the topmost nested dialog responds to Escape and backdrop dismissal", () => {
  const outerClose = vi.fn();
  const innerClose = vi.fn();
  render(<Dialog open title="Outer" onClose={outerClose}><Dialog open title="Inner" onClose={innerClose}><p>Inner content</p></Dialog></Dialog>);

  fireEvent.keyDown(document, { key: "Escape" });
  const backdrops = document.querySelectorAll(".dialog-backdrop");
  fireEvent.mouseDown(backdrops[1]);

  expect(innerClose).toHaveBeenCalledTimes(2);
  expect(outerClose).not.toHaveBeenCalled();
});
