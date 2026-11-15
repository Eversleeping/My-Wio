import { lazy, Suspense } from "react";
import { type PageProps, type CodexPageProps } from "../types";
import { PageLoading } from "../components/PageUI";

const ServersPageRoute = lazy(() => import("./ServersPage"));

export const CodexPageRoute = lazy(() => import("../sessions/engines/codex"));

export function ServersPage({ notify }: Pick<PageProps, "notify">) {
  return <Suspense fallback={<PageLoading />}><ServersPageRoute notify={notify} /></Suspense>;
}

export function CodexPage(props: CodexPageProps) {
  return <Suspense fallback={<PageLoading />}><CodexPageRoute {...props} /></Suspense>;
}
