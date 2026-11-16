import type { Alert, Metric, OperationMetrics } from "../types";
import { mutation, query, writeResource } from "./resources";

export const monitoring = {
  alerts: (offset?: number, limit = 51) => query<Alert[]>(offset === undefined ? "/alerts" : `/alerts?limit=${limit}&offset=${offset}`, ["monitoring"]),
  metrics: (serverID: string, hours: number) => query<Metric[]>(`/servers/${serverID}/metrics?hours=${hours}`, ["monitoring"]),
  operationMetrics: (serverID: string, hours: number) => query<OperationMetrics>(`/servers/${serverID}/operation-metrics?hours=${hours}`, ["monitoring"]),
  acknowledge: (id: string) => writeResource(mutation(`/alerts/${id}/acknowledge`, "POST", ["monitoring", "dashboard"]), {}),
};
