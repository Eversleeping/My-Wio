import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, test, vi } from "vitest";
import type { ProjectDetail } from "../../types";
import { ProjectDetailsDialog, type ProjectDetailsLabels } from "./ProjectDetailsDialog";
import type { DialogActionsSlotProps, DialogSlotProps, FieldSlotProps } from "./slots";

const labels: ProjectDetailsLabels = {
  title: "Project details", overview: "Overview", history: "History", name: "Name", description: "Description", defaultBranch: "Default branch", pinned: "Pinned", hidden: "Hidden", archived: "Archived", remote: "Remote", noRemote: "No remote", operation: "Operation", state: "State", time: "Time", result: "Result", noOperations: "No operations", cancel: "Cancel", save: "Save", saving: "Saving", loading: "Loading"
};
const legacyDetail: ProjectDetail = {
  project: {
    id: "project-1", name: "Legacy project", description: "", remote_url: "https://example.com/legacy.git", default_branch: "main", status: "ready", provision_error: "", updated_at: "2026-07-20T00:00:00Z", workspace_count: 1, import_status: "ready", import_message: "", import_server_id: "server-1", import_server_name: "Server", import_operation_id: "", pinned_at: null, hidden_at: null, archived_at: null
  },
  remotes: null,
  operations: null
};

function Dialog({ open, title, children }: DialogSlotProps) { return open ? <div role="dialog" aria-label={title}>{children}</div> : null; }
function Field({ label, children }: FieldSlotProps) { return <label><span>{label}</span>{children}</label>; }
function DialogActions({ children }: DialogActionsSlotProps) { return <div>{children}</div>; }

test("renders legacy null project collections as empty states", async () => {
  const user = userEvent.setup();
  render(<ProjectDetailsDialog open detail={legacyDetail} loading={false} busy={false} error="" labels={labels} slots={{ Dialog, Field, DialogActions }} onClose={vi.fn()} onSubmit={vi.fn()} />);

  expect(screen.getByText("No remote")).toBeInTheDocument();
  await user.click(screen.getByRole("tab", { name: "History" }));
  expect(screen.getByText("No operations")).toBeInTheDocument();
});
