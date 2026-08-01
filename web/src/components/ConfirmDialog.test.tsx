import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, test, vi } from "vitest";
import { ConfirmDialog } from "./ConfirmDialog";

const labels = {
  title: "Delete deployment target",
  description: "This action cannot be undone.",
  confirmLabel: "Delete target",
  cancelLabel: "Keep target"
};

function renderDialog({ busy = false }: { busy?: boolean } = {}) {
  const onConfirm = vi.fn();
  const onClose = vi.fn();
  render(<ConfirmDialog open {...labels} busy={busy} onConfirm={onConfirm} onClose={onClose} />);
  return { onConfirm, onClose };
}

test("confirms with the supplied accessible action name", async () => {
  const user = userEvent.setup();
  const { onConfirm } = renderDialog();

  const confirm = screen.getByRole("button", { name: "Delete target" });
  await user.click(confirm);

  expect(confirm).toHaveAccessibleName("Delete target");
  expect(onConfirm).toHaveBeenCalledTimes(1);
});

test("cancels from its default keyboard focus and Escape", async () => {
  const user = userEvent.setup();
  const { onClose } = renderDialog();

  expect(screen.getByRole("button", { name: "Keep target" })).toHaveFocus();
  await user.keyboard("{Escape}");
  await user.click(screen.getByRole("button", { name: "Keep target" }));

  expect(onClose).toHaveBeenCalledTimes(2);
});

test("makes dangerous impact scope prominent", () => {
  render(<ConfirmDialog open {...labels} danger impact={<span>Removes 3 containers and 2 project volumes.</span>} onConfirm={vi.fn()} onClose={vi.fn()} />);

  expect(screen.getByRole("button", { name: "Delete target" })).toHaveClass("primary-button", "danger");
  expect(screen.getByRole("alert")).toHaveClass("confirm-dialog-impact", "error-banner");
  expect(screen.getByText("Removes 3 containers and 2 project volumes.")).toBeInTheDocument();
});

test("blocks confirmation and every dismissal route while busy", async () => {
  const user = userEvent.setup();
  const { onConfirm, onClose } = renderDialog({ busy: true });

  const confirm = screen.getByRole("button", { name: "Delete target" });
  expect(confirm).toBeDisabled();
  expect(confirm).toHaveAttribute("aria-busy", "true");
  expect(screen.getByRole("button", { name: "Keep target" })).toBeDisabled();
  expect(screen.getByRole("button", { name: "Close" })).toBeDisabled();

  await user.click(confirm);
  await user.click(screen.getByRole("button", { name: "Close" }));
  await user.keyboard("{Escape}");
  fireEvent.mouseDown(document.querySelector(".dialog-backdrop")!);

  expect(onConfirm).not.toHaveBeenCalled();
  expect(onClose).not.toHaveBeenCalled();
});

test("associates the visible impact with the dialog description", () => {
  render(<ConfirmDialog open {...labels} danger impact="Removes the production target." onConfirm={vi.fn()} onClose={vi.fn()} />);

  const dialog = screen.getByRole("dialog");
  const description = document.getElementById(dialog.getAttribute("aria-describedby") ?? "");
  expect(description).toHaveTextContent("This action cannot be undone.");
  expect(description).toHaveTextContent("Removes the production target.");
});
