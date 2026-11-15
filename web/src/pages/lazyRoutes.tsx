import { lazy, Suspense } from "react";
import { type PageProps } from "../types";
import { PageLoading } from "../components/PageUI";

const ServersPageRoute = lazy(() => import("./ServersPage"));

export function ServersPage({ notify }: Pick<PageProps, "notify">) {
  return <Suspense fallback={<PageLoading />}><ServersPageRoute notify={notify} /></Suspense>;
}
