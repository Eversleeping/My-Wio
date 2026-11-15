import type { Engine } from "./sessions/types";
import {
  LayoutDashboard,
  Server as ServerIcon,
  GitBranch,
  Code2,
  Sparkles,
  MessageSquare,
  Bot,
  Rocket,
  ListChecks,
  Activity,
  Settings
} from "lucide-react";

export type View = "dashboard" | "servers" | "projects" | "sessions" | "codex" | "gemini" | "claude" | "deployments" | "operations" | "monitoring" | "settings";

export const navigation: Array<{ id: View; labelKey: string; icon: typeof LayoutDashboard }> = [
  { id: "dashboard", labelKey: "nav.overview", icon: LayoutDashboard },
  { id: "servers", labelKey: "nav.servers", icon: ServerIcon },
  { id: "projects", labelKey: "nav.projects", icon: GitBranch },
  { id: "sessions", labelKey: "nav.sessions", icon: MessageSquare },
  { id: "codex", labelKey: "nav.codex", icon: Code2 },
  { id: "gemini", labelKey: "nav.gemini", icon: Sparkles },
  { id: "claude", labelKey: "nav.claude", icon: Bot },
  { id: "deployments", labelKey: "nav.deployments", icon: Rocket },
  { id: "operations", labelKey: "nav.operations", icon: ListChecks },
  { id: "monitoring", labelKey: "nav.monitoring", icon: Activity },
  { id: "settings", labelKey: "nav.settings", icon: Settings }
];

export function readLocationState(): { view: View; threadID: string; engine?: Engine } {
  const params = new URLSearchParams(window.location.search);
  const requestedView = params.get("view");
  const view = navigation.some(item => item.id === requestedView) ? requestedView as View : "dashboard";
  const engine = params.get("engine");
  if (view === "sessions") return { view, engine: isEngine(engine) ? engine : undefined, threadID: isEngine(engine) ? params.get("thread") ?? "" : "" };
  return { view, threadID: view === "codex" || view === "gemini" || view === "claude" ? params.get("thread") ?? "" : "" };
}

export function isEngine(value: unknown): value is Engine { return ["codex", "gemini", "claude"].includes(value as string); }

export function locationFor(view: View, threadID = "", engine?: Engine) {
  const params = new URLSearchParams();
  if (view !== "dashboard") params.set("view", view);
  if ((view === "codex" || view === "gemini" || view === "claude") && threadID) params.set("thread", threadID);
  if (view === "sessions" && engine && threadID) { params.set("engine", engine); params.set("thread", threadID); }
  const query = params.toString();
  return `${window.location.pathname}${query ? `?${query}` : ""}${window.location.hash}`;
}

export function navigateToLocation(view: View, threadID = "", replace = false, engine?: Engine) {
  const nextLocation = locationFor(view, threadID, engine);
  if (`${window.location.pathname}${window.location.search}${window.location.hash}` !== nextLocation) window.history[replace ? "replaceState" : "pushState"](null, "", nextLocation);
}
