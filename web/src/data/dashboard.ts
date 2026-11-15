import type { Summary } from "../types";
import { query } from "./resources";

export const dashboard = { summary: () => query<Summary>("/summary", ["dashboard"], false, 1000) };
