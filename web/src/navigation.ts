import type { Engine } from "./sessions/types";
import {
  LayoutDashboard,
  ListChecks,
  Server as ServerIcon,
  GitBranch,
  MessageSquare,
  Rocket,
  ScrollText,
  Activity,
  Settings
} from "lucide-react";

export type View = "dashboard" | "servers" | "projects" | "sessions" | "tasks" | "deployments" | "operations" | "monitoring" | "settings";

export const navigation: Array<{ id: View; labelKey: string; icon: typeof LayoutDashboard }> = [
  { id: "dashboard", labelKey: "nav.overview", icon: LayoutDashboard },
  { id: "servers", labelKey: "nav.servers", icon: ServerIcon },
  { id: "projects", labelKey: "nav.projects", icon: GitBranch },
  { id: "sessions", labelKey: "nav.sessions", icon: MessageSquare },
  { id: "tasks", labelKey: "nav.tasks", icon: ListChecks },
  { id: "deployments", labelKey: "nav.deployments", icon: Rocket },
  { id: "operations", labelKey: "nav.operations", icon: ScrollText },
  { id: "monitoring", labelKey: "nav.monitoring", icon: Activity },
  { id: "settings", labelKey: "nav.settings", icon: Settings }
];

export type TaskLocation = { projectID?: string; taskID?: string; create?: boolean };
export function tasksLocation({ projectID, taskID, create }: TaskLocation = {}) {
  const params = new URLSearchParams({ view: "tasks" });
  if (projectID) params.set("project_id", projectID);
  if (taskID) params.set("task", taskID);
  if (create) params.set("create", "1");
  return `${window.location.pathname}?${params}${window.location.hash}`;
}

export type SessionEntry = { workspaceID: string; create: boolean };

export function workspaceSessionsLocation(workspaceID: string, create = false) {
  const params = new URLSearchParams({view:"sessions",workspace:workspaceID});
  if (create) params.set("create", "1");
  return `${window.location.pathname}?${params}${window.location.hash}`;
}

export function readLocationState(): { view: View; threadID: string; engine?: Engine; initialFilter?: Engine; entry?: SessionEntry; task?: TaskLocation } {
  const params = new URLSearchParams(window.location.search);
  const requestedView = params.get("view");
  if (isEngine(requestedView)) return { view: "sessions", engine: requestedView, threadID: params.get("thread") ?? "", ...(!params.get("thread") ? { initialFilter: requestedView } : {}) };
  const view = navigation.some(item => item.id === requestedView) ? requestedView as View : "dashboard";
  if (view === "tasks") return { view, threadID: "", task: {
    projectID: params.get("project_id") || undefined,
    taskID: params.get("task") || undefined,
    create: params.get("create") === "1"
  } };
  const engine = params.get("engine");
  if (view === "sessions") return { view, ...(params.get("workspace") ? {entry:{workspaceID:params.get("workspace")!,create:params.get("create")==="1"}} : {}), engine: isEngine(engine) ? engine : undefined, threadID: isEngine(engine) ? params.get("thread") ?? "" : "" };
  return { view, threadID: "" };
}

export function isEngine(value: unknown): value is Engine { return ["codex", "gemini", "claude"].includes(value as string); }

export function locationFor(view: View, threadID = "", engine?: Engine) {
  const params = new URLSearchParams();
  if (view !== "dashboard") params.set("view", view);
  if (view === "sessions" && engine && threadID) { params.set("engine", engine); params.set("thread", threadID); }
  const query = params.toString();
  return `${window.location.pathname}${query ? `?${query}` : ""}${window.location.hash}`;
}

export function navigateToLocation(view: View, threadID = "", replace = false, engine?: Engine) {
  const nextLocation = locationFor(view, threadID, engine);
  if (`${window.location.pathname}${window.location.search}${window.location.hash}` !== nextLocation) window.history[replace ? "replaceState" : "pushState"](null, "", nextLocation);
}
