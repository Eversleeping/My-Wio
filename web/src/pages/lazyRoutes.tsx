import { lazy, Suspense } from "react";
import { type PageProps, type CodexPageProps } from "../types";
import { PageLoading } from "../components/PageUI";

const ServersPageRoute = lazy(() => import("./ServersPage"));

export const CodexPageRoute = lazy(() => import("./CodexPage"));

export function ServersPage({ realtime, notify }: PageProps) {
  return <Suspense fallback={<PageLoading />}><ServersPageRoute realtime={realtime} notify={notify} /></Suspense>;
}

export function CodexPage(props: CodexPageProps) {
  return <Suspense fallback={<PageLoading />}><CodexPageRoute {...props} /></Suspense>;
}
