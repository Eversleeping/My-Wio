import type { SessionSummary } from "./types";
export type ScheduledNativeValue = {model: string; reasoning_effort: string; approval_mode: string; thinking_level: string; enabled: boolean};
export type ScheduledFieldsProps = {open: boolean; form: ScheduledNativeValue; update: (changes: Partial<ScheduledNativeValue>) => void; busy: boolean; thread?: SessionSummary & {model?: string}};
