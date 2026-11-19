import { CalendarClock, KeyRound, Network, ShieldCheck, SquareTerminal } from "lucide-react";
import { type KeyboardEvent, type MouseEvent, useEffect, useRef, useState } from "react";
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
  const list = useRef<HTMLDivElement>(null);
  useEffect(() => {
    // On narrow screens the categories scroll sideways; keep the current one in view.
    const tabs = list.current;
    const current = tabs?.querySelector<HTMLAnchorElement>('[aria-selected="true"]');
    if (!tabs || !current) return;
    const start = current.offsetLeft - 4;
    const end = current.offsetLeft + current.offsetWidth + 4;
    if (start < tabs.scrollLeft) tabs.scrollLeft = start;
    else if (end > tabs.scrollLeft + tabs.clientWidth) tabs.scrollLeft = end - tabs.clientWidth;
  }, [active]);
  const select = (next: SettingsSection) => {
    if (next === active) return;
    (onSectionChange ?? setLocalSection)(next);
    layout.current?.scrollIntoView?.({ block: "start" });
  };
  const follow = (event: MouseEvent<HTMLAnchorElement>, next: SettingsSection) => {
    // Modified clicks keep the browser behaviour, such as opening a new tab.
    if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    select(next);
  };
  const handleTabKey = (event: KeyboardEvent<HTMLAnchorElement>, index: number) => {
    const count = settingsSections.length;
    let next: number;
    if (event.key === "ArrowRight") next = (index + 1) % count;
    else if (event.key === "ArrowLeft") next = (index + count - 1) % count;
    else if (event.key === "Home") next = 0;
    else if (event.key === "End") next = count - 1;
    else if (event.key === " ") next = index;
    else return;
    event.preventDefault();
    const id = settingsSections[next];
    select(id);
    list.current?.querySelector<HTMLAnchorElement>(`#settings-tab-${id}`)?.focus();
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
  return <div className="page-stack settings-layout" ref={layout}>
    <div className="segmented-control settings-tabs" ref={list} role="tablist" aria-label={t("settings.sections")}>
      {settingsSections.map((id, index) => {
        const { label, hint, Icon } = categories[id];
        return <a key={id} id={`settings-tab-${id}`} href={settingsLocation(id)} role="tab" aria-selected={id === active} aria-controls={`settings-panel-${id}`} tabIndex={id === active ? 0 : -1} title={t(hint)} onClick={event => follow(event, id)} onKeyDown={event => handleTabKey(event, index)}>
          <Icon size={15} aria-hidden="true" />{t(label)}
        </a>;
      })}
    </div>
    {settingsSections.map(id => <div key={id} id={`settings-panel-${id}`} role="tabpanel" aria-labelledby={`settings-tab-${id}`} className="settings-panel page-stack" hidden={id !== active}>
      {visited.has(id) && panel(id)}
    </div>)}
  </div>;
}

export default SettingsPage;
