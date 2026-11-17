import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { I18nProvider } from "../i18n";
import { CollaborationSettings } from "./CollaborationSettings";

afterEach(() => { vi.unstubAllGlobals(); localStorage.clear(); });

test("saves collaboration once for all sessions with engine permissions and display choices", async () => {
 localStorage.setItem("wio_language", "en");
 let grant = { enabled:false, target_modes:{} };
 const saved: unknown[] = [];
 vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, options: RequestInit = {}) => {
  expect(String(input)).toBe("/api/settings/collaboration");
  if (options.method === "PUT") { grant = JSON.parse(String(options.body)); saved.push(grant); }
  return new Response(JSON.stringify(grant));
 }));
 const notify = vi.fn();
 render(<I18nProvider><CollaborationSettings notify={notify} /></I18nProvider>);
 const user = userEvent.setup();
 await waitFor(() => expect(screen.getByRole("checkbox", {name:"Allow creating and managing child sessions"})).toBeEnabled());
 await user.click(screen.getByRole("checkbox", {name:"Allow creating and managing child sessions"}));
 await user.click(screen.getByRole("checkbox", {name:"Allow creating separate worktrees"}));
 await user.selectOptions(screen.getByRole("combobox", {name:"claude collaboration permissions"}),"default");
 await user.click(screen.getByRole("checkbox", {name:"Show unread results"}));
 await user.click(screen.getByRole("button", {name:"Save"}));
 await waitFor(() => expect(saved).toEqual([expect.objectContaining({enabled:true,worktree:true,show_unread_results:true,target_modes:{claude:"default"},workspace_ids:[],session_refs:[]})]));
 await waitFor(() => expect(notify).toHaveBeenCalledWith("Collaboration settings applied to all sessions"));
 expect(screen.getByRole("checkbox", {name:"Allow creating and managing child sessions"})).toBeChecked();
});

test("keeps a failed global save editable and displays the error", async () => {
 localStorage.setItem("wio_language", "en");
 vi.stubGlobal("fetch", vi.fn(async (_input: RequestInfo | URL, options: RequestInit = {}) => options.method === "PUT" ? new Response(JSON.stringify({error:"Could not save"}),{status:500}) : new Response(JSON.stringify({enabled:false}))));
 const notify = vi.fn();
 render(<I18nProvider><CollaborationSettings notify={notify}/></I18nProvider>);
 const user = userEvent.setup();
 await waitFor(() => expect(screen.getByRole("button", {name:"Save"})).toBeEnabled());
 await user.click(screen.getByRole("checkbox", {name:"Allow creating and managing child sessions"}));
 await user.click(screen.getByRole("button", {name:"Save"}));
 expect(await screen.findByRole("alert")).toHaveTextContent("Could not save");
 expect(screen.getByRole("checkbox", {name:"Allow creating and managing child sessions"})).toBeChecked();
 expect(notify).not.toHaveBeenCalled();
});
