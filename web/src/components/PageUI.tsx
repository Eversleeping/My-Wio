import { type ReactNode } from "react";
import { AlertTriangle, Boxes, LoaderCircle, RefreshCw } from "lucide-react";
import { useI18n } from "../i18n";

export function Section({ title, icon, action, children }: { title: string; icon?: ReactNode; action?: ReactNode; children: ReactNode }) {
  return <section className="section"><div className="section-heading"><div>{icon}<h2>{title}</h2></div>{action}</div>{children}</section>;
}

export function DataTable({ headers, empty, children }: { headers: string[]; empty: string; children: ReactNode }) {
  const count = Array.isArray(children) ? children.length : children ? 1 : 0;
  return <div className="table-wrap"><table><thead><tr>{headers.map(header => <th key={header}>{header}</th>)}</tr></thead><tbody>{count ? children : <tr><td colSpan={headers.length}><Empty icon={<Boxes size={22} />} text={empty} /></td></tr>}</tbody></table></div>;
}

export function Empty({ icon, text }: { icon: ReactNode; text: string }) {
  return <div className="empty">{icon}<span>{text}</span></div>;
}

export function Status({ value, icon }: { value: string; icon?: ReactNode }) {
  const { t } = useI18n();
  const normalized = value.replace(/([a-z0-9])([A-Z])/g, "$1-$2").toLowerCase().replaceAll("_", "-");
  const translated = t(`status.${normalized}`);
  return <span className={`status-tag ${normalized}`}>{icon}{translated.startsWith("status.") ? value.replaceAll("_", " ") : translated}</span>;
}

export function PageLoading() {
  return <div className="page-loading"><LoaderCircle className="spin" size={24} /></div>;
}

export function ErrorState({ error, reload }: { error: string; reload: () => void }) {
  const { t } = useI18n();
  return <div className="error-state"><AlertTriangle size={25} /><strong>{t("error.load")}</strong><span>{error}</span><button className="secondary-button" onClick={reload}><RefreshCw size={16} />{t("common.retry")}</button></div>;
}
