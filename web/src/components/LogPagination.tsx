import { ChevronLeft, ChevronRight } from "lucide-react";
import { useI18n } from "../i18n";

export const logPageSize = 50;

export function LogPagination({ page, hasMore, busy, onChange }: { page: number; hasMore: boolean; busy: boolean; onChange: (page: number) => void }) {
  const { t } = useI18n();
  return <nav className="log-pagination" aria-label={t("logs.pagination")}>
    <span role="status">{t("logs.page", { page: page + 1, size: logPageSize })}</span>
    <button type="button" className="secondary-button small" disabled={busy || page === 0} onClick={() => onChange(page - 1)}><ChevronLeft size={15} />{t("logs.previous")}</button>
    <button type="button" className="secondary-button small" disabled={busy || !hasMore} onClick={() => onChange(page + 1)}>{t("logs.next")}<ChevronRight size={15} /></button>
  </nav>;
}
