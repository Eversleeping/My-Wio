import type { Server } from "../types";
import { query } from "./resources";

export const servers = {
  list: () => query<Server[]>("/servers", ["servers", "monitoring", "projects", "deployments", "settings"]),
};
