import { CalendarClock, KeyRound, Network, ShieldCheck, SquareTerminal } from "lucide-react";
import { type MouseEvent, useEffect, useRef, useState } from "react";
import { CollaborationSettings } from "../components/CollaborationSettings";
import { ServerAccessSettings } from "../components/ServerAccessSettings";
import { useI18n } from "../i18n";
import { defaultSettingsSection, settingsLocation, settingsSections, type SettingsSection } from "../navigation";
import { CLIVersionSettings } from "./settings/CLIVersionSettings";
import { CredentialSettings } from "./settings/CredentialSettings";
import { ScheduledTaskSettings } from "./settings/ScheduledTaskSettings";

export interface PageProps {
  notify: (text: string) => void;
  /** The category to show. Without `onSectionChange` it only chooses the first one shown. */
  section?: SettingsSection;
  /** Lets the owner of the address bar follow the category. */
  onSectionChange?: (section: SettingsSection) => void;
}

const categories: Record<SettingsSection, { label: string; hint: string; Icon: typeof Network }> = {
  collaboration: { label: "collaboration.title", hint: "settings.sectionCollaborationHint", Icon: Network },
  servers: { label: "settings.sectionServers", hint: "settings.sectionServersHint", Icon: ShieldCheck },
  cli: { label: "settings.cliVersions", hint: "settings.sectionCLIHint", Icon: SquareTerminal },
  "scheduled-tasks": { label: "settings.scheduledTasks", hint: "settings.sectionScheduledTasksHint", Icon: CalendarClock },
  credentials: { label: "settings.sectionCredentials", hint: "settings.sectionCredentialsHint", Icon: KeyRound },
};

export function SettingsPage({ notify, section, onSectionChange }: PageProps) {
  const { t } = useI18n();
  const [localSection, setLocalSection] = useState<SettingsSection>(section ?? defaultSettingsSection);
  const active = onSectionChange ? section ?? defaultSettingsSection : localSection;
  // A category loads on its first visit and then stays mounted, so unsaved
  // edits and operations awaiting an Agent survive switching between categories.
  const [visited, setVisited] = useState<ReadonlySet<SettingsSection>>(() => new Set([active]));
  if (!visited.has(active)) setVisited(new Set(visited).add(active));
  const layout = useRef<HTMLDivElement>(null);
  const list = useRef<HTMLElement>(null);
  useEffect(() => {
    // On narrow screens the categories scroll sideways; keep the current one in view.
    const current = list.current?.querySelector<HTMLElement>('[aria-current="page"]');
    if (!list.current || !current) return;
    const start = current.offsetLeft - 8, end = current.offsetLeft + current.offsetWidth + 8;
    if (start < list.current.scrollLeft) list.current.scrollLeft = start;
    else if (end > list.current.scrollLeft + list.current.clientWidth) list.current.scrollLeft = end - list.current.clientWidth;
  }, [active]);
  const select = (event: MouseEvent<HTMLAnchorElement>, next: SettingsSection) => {
    // Modified clicks keep the browser behaviour, such as opening a new tab.
    if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    if (next === active) return;
    (onSectionChange ?? setLocalSection)(next);
    layout.current?.scrollIntoView?.({ block: "start" });
  };
  const panel = (id: SettingsSection) => {
    switch (id) {
      case "collaboration": return <CollaborationSettings notify={notify} />;
      case "servers": return <ServerAccessSettings />;
      case "cli": return <CLIVersionSettings notify={notify} />;
      case "scheduled-tasks": return <ScheduledTaskSettings notify={notify} active={id === active} />;
      case "credentials": return <CredentialSettings notify={notify} active={id === active} />;
    }
  };
  return <div className="settings-layout" ref={layout}>
    <nav className="settings-nav" ref={list} aria-label={t("settings.sections")}>
      {settingsSections.map(id => {
        const { label, hint, Icon } = categories[id];
        return <a key={id} href={settingsLocation(id)} className={id === active ? "active" : ""} aria-current={id === active ? "page" : undefined} aria-label={t(label)} aria-describedby={`settings-nav-${id}-hint`} onClick={event => select(event, id)}><Icon size={17} /><strong>{t(label)}</strong><small id={`settings-nav-${id}-hint`}>{t(hint)}</small></a>;
      })}
    </nav>
    <div className="settings-panels">
      {settingsSections.map(id => visited.has(id) && <div key={id} className="settings-panel page-stack" hidden={id !== active}>{panel(id)}</div>)}
    </div>
  </div>;
}

export default SettingsPage;
