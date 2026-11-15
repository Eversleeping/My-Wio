// Test adapter for native conversation suites exercised through the unified page.
import UnifiedSessionsPage from "./UnifiedSessionsPage";
import type { CodexPageProps } from "../types";
import type { Engine } from "./types";
type Props = Pick<CodexPageProps, "realtime" | "notify" | "selectedThreadID" | "onSelectThread"> & Partial<CodexPageProps> & { engine: Engine };
export function SessionTestPage({ engine, selectedThreadID, onSelectThread, ...props }: Props) {
  return <UnifiedSessionsPage streamRevisions={{}} approvals={[]} approvalSignal={0} reloadApprovals={() => undefined} {...props} selected={selectedThreadID ? {engine, id: selectedThreadID} : undefined} onSelect={(ref, replace) => onSelectThread(ref?.id ?? "", replace)} />;
}
