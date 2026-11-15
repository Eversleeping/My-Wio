import type { OperationSummary } from "../types";
import { mutation, query, writeResource } from "./resources";

export const operations = {
  list: (status = "") => query<OperationSummary[]>(status ? `/operations?status=${encodeURIComponent(status)}&limit=200` : "/operations?limit=200", ["operations"]),
  interrupt: (id: string) => writeResource(mutation(`/operations/${encodeURIComponent(id)}/interrupt`, "POST", ["operations"]), {}),
};
