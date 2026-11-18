import { ListChecks, Plus } from "lucide-react";
import { useState, type MouseEvent } from "react";
import { Field } from "../components/FormUI";
import { Empty, ErrorState, PageLoading, RefreshWarning, Section } from "../components/PageUI";
import { projects } from "../data/projects";
import { invalidateResources, useResource } from "../data/resources";
import { tasks, type TaskState } from "../data/tasks";
import { useVisibleInterval } from "../hooks/useVisibleInterval";
import { useI18n } from "../i18n";
import { tasksLocation, type TaskLocation } from "../navigation";
import TaskDetail, { TaskStatus } from "./TaskDetail";
import { TaskSpecForm } from "./TaskSpecForm";
import "./tasks.css";

const states: TaskState[] = ["draft", "active", "awaiting_acceptance", "accepted", "rejected", "failed", "cancelled"];

export default function TasksPage({ location, onNavigate, connected }: {
  location: TaskLocation; onNavigate: (location: TaskLocation, replace?: boolean) => void; connected: boolean;
}) {
  const { t } = useI18n();
  const allProjects = useResource(projects.list());
  const locator = useResource(location.taskID && !location.projectID ? tasks.locate(location.taskID) : null);
  const projectID = location.projectID || locator.data?.project_id || (!location.taskID ? allProjects.data?.[0]?.id : "") || "";
  const snapshot = useResource(location.taskID && projectID ? tasks.detail(location.taskID, projectID) : null);
  const workspaces = useResource(projectID ? projects.workspaces() : null);
  // Even an open socket can miss a notification. Refresh visible task snapshots
  // periodically, in addition to the shared five-second disconnect fallback.
  useVisibleInterval(() => invalidateResources(["tasks"]), connected && Boolean(projectID) ? 15000 : null);
  const workspaceOptions = (workspaces.data ?? []).filter(item => item.project_id === projectID);

  const selectTask = (event: MouseEvent, taskID: string) => {
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    onNavigate({ projectID, taskID });
  };

  return <div className="page-stack">
    <div className="task-toolbar">
      <Field label={t("column.project")}><select value={projectID} onChange={e => onNavigate({ projectID: e.target.value })}>
        {!projectID && <option value="">{t("tasks.chooseProject")}</option>}
        {projectID && !allProjects.data?.some(item => item.id === projectID) && <option value={projectID}>{snapshot.data?.task.project_id ?? projectID}</option>}
        {allProjects.data?.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}
      </select></Field>
      <button className="primary-button" disabled={!projectID} onClick={() => onNavigate({ projectID, create: true })}><Plus size={17} />{t("tasks.new")}</button>
    </div>
    {allProjects.error && <RefreshWarning error={allProjects.error} reload={allProjects.reload} />}
    {workspaces.error && <RefreshWarning error={workspaces.error} reload={workspaces.reload} />}
    <div className={`task-layout ${location.taskID ? "task-selected" : ""}`}>
      {projectID && <TaskList key={projectID} projectID={projectID} selected={location.taskID} onSelect={selectTask} />}
      {location.taskID && <div className="task-selection">
        {locator.error && <ErrorState error={locator.error} reload={locator.reload} />}
        {(locator.loading || snapshot.loading) && <PageLoading />}
        {snapshot.error && (snapshot.data ? <RefreshWarning error={snapshot.error} reload={snapshot.reload} /> : <ErrorState error={snapshot.error} reload={snapshot.reload} />)}
        {snapshot.data && <TaskDetail key={snapshot.data.task.id} detail={snapshot.data} workspaces={workspaceOptions} reload={snapshot.reload} />}
      </div>}
      {!location.taskID && projectID && <Section title={t("tasks.detail")}><Empty icon={<ListChecks size={22} />} text={t("tasks.selectTask")} /></Section>}
      {!projectID && !location.taskID && !allProjects.loading && <p>{t("tasks.noProject")}</p>}
    </div>
    {location.create && projectID && <TaskSpecForm key={projectID} projectID={projectID}
      onClose={() => onNavigate({ projectID }, true)}
      onSaved={task => onNavigate({ projectID, taskID: task.id }, true)} />}
  </div>;
}

function TaskList({ projectID, selected, onSelect }: {
  projectID: string; selected?: string; onSelect: (event: MouseEvent, id: string) => void;
}) {
  const { t } = useI18n();
  const [state, setState] = useState("");
  const [cursors, setCursors] = useState<string[]>([""]);
  const list = useResource(tasks.list(projectID, state, cursors[cursors.length - 1]));
  return <Section title={t("tasks.projectTasks")} icon={<ListChecks size={18} />}>
    <div className="task-list">
      <Field label={t("column.status")}><select value={state} onChange={e => { setState(e.target.value); setCursors([""]); }}>
        <option value="">{t("tasks.allStates")}</option>
        {states.map(value => <option value={value} key={value}>{t(`tasks.state.${value}`)}</option>)}
      </select></Field>
      {list.loading && <PageLoading />}
      {list.error && <ErrorState error={list.error} reload={list.reload} />}
      {list.data?.items.length === 0 && <p>{t("tasks.empty")}</p>}
      {list.data?.items.map(item => <a className="task-list-row" aria-current={selected === item.id ? "page" : undefined} key={item.id}
        href={tasksLocation({ projectID, taskID: item.id })} onClick={e => onSelect(e, item.id)}>
        <strong>{item.title}</strong><TaskStatus state={item.archived_at ? "archived" : item.state} />
      </a>)}
      {(cursors.length > 1 || list.data?.next_cursor) && <div className="task-actions">
        <button className="secondary-button small" disabled={cursors.length < 2} onClick={() => setCursors(cursors.slice(0, -1))}>{t("tasks.previous")}</button>
        <button className="secondary-button small" disabled={!list.data?.next_cursor}
          onClick={() => { if (list.data?.next_cursor) setCursors([...cursors, list.data.next_cursor]); }}>{t("tasks.next")}</button>
      </div>}
    </div>
  </Section>;
}
