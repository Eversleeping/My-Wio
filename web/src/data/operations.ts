import type { OperationSummary } from "../types";
import { mutation, query, writeResource } from "./resources";

export const operations = {
  list: (status = "", offset = 0, limit = 51) => query<OperationSummary[]>(`/operations?${status ? `status=${encodeURIComponent(status)}&` : ""}limit=${limit}&offset=${offset}`, ["operations"]),
  interrupt: (id: string) => writeResource(mutation(`/operations/${encodeURIComponent(id)}/interrupt`, "POST", ["operations"]), {}),
};
