import * as common from "./common";
import * as codex from "./codex";
import * as server from "./server";
import * as project from "./project";
import * as gemini from "./gemini";
import * as claude from "./claude";
import * as settings from "./settings";
import * as tasks from "./tasks";
import * as deployment from "./deployment";

export const enFragments = [common.en, codex.en, server.en, project.en, gemini.en, claude.en, settings.en, deployment.en, tasks.en] as const;
export const zhFragments = [common.zh, codex.zh, server.zh, project.zh, gemini.zh, claude.zh, settings.zh, deployment.zh, tasks.zh] as const;

export const en = {
  ...common.en,
  ...codex.en,
  ...server.en,
  ...project.en,
  ...gemini.en,
  ...claude.en,
  ...settings.en,
  ...deployment.en,
  ...tasks.en,
};

type MessageKey = keyof typeof en;

export const zh: Record<MessageKey, string> = {
  ...common.zh,
  ...codex.zh,
  ...server.zh,
  ...project.zh,
  ...gemini.zh,
  ...claude.zh,
  ...settings.zh,
  ...deployment.zh,
  ...tasks.zh,
};
