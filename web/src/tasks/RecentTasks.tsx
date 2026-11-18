import { ChevronRight, ListChecks } from "lucide-react";
import { ErrorState, Section } from "../components/PageUI";
import { tasks } from "../data/tasks";
import { useResource } from "../data/resources";
import { useI18n } from "../i18n";
import { tasksLocation } from "../navigation";

import "./tasks.css";

export default function RecentTasks() {
  const { t } = useI18n();
  const recent = useResource(tasks.recent());
  return <Section title={t("tasks.recent")} icon={<ListChecks size={18} />}
    action={<a className="text-button" href={tasksLocation()}>{t("common.viewAll")}<ChevronRight size={15} /></a>}>
    {recent.error && <ErrorState error={recent.error} reload={recent.reload} />}
    {!recent.loading && recent.data?.items?.length === 0 && <p>{t("tasks.empty")}</p>}
    {recent.data?.items?.map(item => <div className="task-recent-row" key={item.id}>
      <a href={tasksLocation({ projectID: item.project_id, taskID: item.id })}>{item.title}</a>
      <small>{item.project_name} · {t(`tasks.state.${item.state}`)}</small>
    </div>)}
  </Section>;
}
