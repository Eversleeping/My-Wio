CREATE TABLE tasks (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
 title TEXT NOT NULL,
 requirement TEXT NOT NULL,
 criteria_json TEXT NOT NULL,
 spec_version BIGINT NOT NULL DEFAULT 1 CHECK(spec_version>0),
 current_plan_id TEXT,
 current_run_id TEXT,
 current_grant_revision BIGINT,
 counters_json TEXT NOT NULL DEFAULT '{"plans":0,"runs":0,"attempts":0,"messages":0}',
 version BIGINT NOT NULL DEFAULT 1 CHECK(version>0),
 created_by TEXT NOT NULL,
 created_at TIMESTAMP NOT NULL,
 updated_at TIMESTAMP NOT NULL,
 archived_at TIMESTAMP,
 UNIQUE(id,project_id)
);
CREATE INDEX tasks_project_updated_idx ON tasks(project_id,updated_at DESC,id DESC);
CREATE TABLE task_plan_revisions (
 id TEXT PRIMARY KEY,
 task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT,
 revision BIGINT NOT NULL CHECK(revision>0),
 parent_id TEXT,
 spec_version BIGINT NOT NULL CHECK(spec_version>0),
 content_json TEXT NOT NULL,
 content_hash TEXT NOT NULL,
 author TEXT NOT NULL,
 created_at TIMESTAMP NOT NULL,
 UNIQUE(task_id,revision),
 UNIQUE(task_id,id),
 FOREIGN KEY(task_id,parent_id) REFERENCES task_plan_revisions(task_id,id) ON DELETE RESTRICT
);
CREATE TABLE task_plan_members (
 task_id TEXT NOT NULL,
 plan_id TEXT NOT NULL,
 id TEXT NOT NULL,
 role TEXT NOT NULL CHECK(role IN ('coordinator','developer','researcher','reviewer')),
 engine TEXT NOT NULL CHECK(engine IN ('codex','claude','gemini')),
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
 execution_json TEXT NOT NULL,
 PRIMARY KEY(plan_id,id),
 FOREIGN KEY(task_id,plan_id) REFERENCES task_plan_revisions(task_id,id) ON DELETE RESTRICT
);
CREATE INDEX task_plan_members_workspace_idx ON task_plan_members(workspace_id);
CREATE TABLE task_plan_steps (
 task_id TEXT NOT NULL,
 plan_id TEXT NOT NULL,
 id TEXT NOT NULL,
 member_id TEXT NOT NULL,
 definition_json TEXT NOT NULL,
 PRIMARY KEY(plan_id,id),
 FOREIGN KEY(task_id,plan_id) REFERENCES task_plan_revisions(task_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(plan_id,member_id) REFERENCES task_plan_members(plan_id,id) ON DELETE RESTRICT
);
CREATE TABLE task_step_dependencies (
 plan_id TEXT NOT NULL,
 step_id TEXT NOT NULL,
 depends_on TEXT NOT NULL,
 PRIMARY KEY(plan_id,step_id,depends_on),
 CHECK(step_id<>depends_on),
 FOREIGN KEY(plan_id,step_id) REFERENCES task_plan_steps(plan_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(plan_id,depends_on) REFERENCES task_plan_steps(plan_id,id) ON DELETE RESTRICT
);
CREATE TABLE task_grant_revisions (
 task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT,
 revision BIGINT NOT NULL CHECK(revision>0),
 actor TEXT NOT NULL,
 grant_json TEXT NOT NULL,
 created_at TIMESTAMP NOT NULL,
 PRIMARY KEY(task_id,revision)
);
CREATE TABLE task_runs (
 id TEXT PRIMARY KEY,
 task_id TEXT NOT NULL,
 plan_id TEXT NOT NULL,
 grant_revision BIGINT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('running','paused','blocked','cancelling','cancelled','failed','awaiting_acceptance','accepted','rejected')),
 version BIGINT NOT NULL DEFAULT 1 CHECK(version>0),
 counters_json TEXT NOT NULL DEFAULT '{}',
 started_at TIMESTAMP,
 deadline_at TIMESTAMP,
 ended_at TIMESTAMP,
 accepted_by TEXT,
 acceptance_reason TEXT,
 accepted_at TIMESTAMP,
 UNIQUE(task_id,id),
 UNIQUE(task_id,id,plan_id),
 FOREIGN KEY(task_id,plan_id) REFERENCES task_plan_revisions(task_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(task_id,grant_revision) REFERENCES task_grant_revisions(task_id,revision) ON DELETE RESTRICT
);
CREATE UNIQUE INDEX task_runs_one_active_idx ON task_runs(task_id) WHERE state IN ('running','paused','blocked','cancelling','awaiting_acceptance');
CREATE TABLE task_step_runs (
 task_id TEXT NOT NULL,
 run_id TEXT NOT NULL,
 plan_id TEXT NOT NULL,
 step_id TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('pending','ready','active','blocked','succeeded','failed','cancelled')),
 current_attempt_id TEXT,
 block_code TEXT,
 version BIGINT NOT NULL DEFAULT 1 CHECK(version>0),
 PRIMARY KEY(run_id,step_id),
 UNIQUE(task_id,run_id,plan_id,step_id),
 FOREIGN KEY(task_id,run_id,plan_id) REFERENCES task_runs(task_id,id,plan_id) ON DELETE RESTRICT,
 FOREIGN KEY(plan_id,step_id) REFERENCES task_plan_steps(plan_id,id) ON DELETE RESTRICT
);
CREATE TABLE task_attempts (
 id TEXT PRIMARY KEY,
 task_id TEXT NOT NULL,
 run_id TEXT NOT NULL,
 plan_id TEXT NOT NULL,
 step_id TEXT NOT NULL,
 number INTEGER NOT NULL CHECK(number>0),
 dispatch_key TEXT NOT NULL UNIQUE,
 input_hash TEXT NOT NULL,
 input_json TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('queued','running','unknown','succeeded','failed','cancelled')),
 engine TEXT NOT NULL CHECK(engine IN ('codex','claude','gemini')),
 session_id TEXT NOT NULL,
 server_id TEXT NOT NULL,
 workspace_id TEXT NOT NULL,
 source_json TEXT NOT NULL,
 operation_id TEXT UNIQUE REFERENCES agent_operations(id) ON DELETE RESTRICT,
 interrupt_operation_id TEXT REFERENCES agent_operations(id) ON DELETE RESTRICT,
 lease_owner TEXT,
 lease_generation BIGINT NOT NULL DEFAULT 1 CHECK(lease_generation>0),
 lease_expires_at TIMESTAMP,
 evidence_cursor BIGINT NOT NULL DEFAULT 0,
 outcome_certainty TEXT NOT NULL DEFAULT 'unknown' CHECK(outcome_certainty IN ('unknown','confirmed','user_confirmed')),
 result_summary TEXT,
 created_at TIMESTAMP NOT NULL,
 ended_at TIMESTAMP,
 UNIQUE(run_id,step_id,number),
 UNIQUE(task_id,run_id,id),
 FOREIGN KEY(task_id,run_id,plan_id,step_id) REFERENCES task_step_runs(task_id,run_id,plan_id,step_id) ON DELETE RESTRICT
);
CREATE INDEX task_attempts_session_idx ON task_attempts(engine,session_id);
CREATE INDEX task_attempts_workspace_idx ON task_attempts(workspace_id);
CREATE TABLE task_session_bindings (
 id TEXT PRIMARY KEY,
 task_id TEXT NOT NULL,
 run_id TEXT NOT NULL,
 engine TEXT NOT NULL CHECK(engine IN ('codex','claude','gemini')),
 session_id TEXT NOT NULL,
 active_attempt_id TEXT,
 released_at TIMESTAMP,
 FOREIGN KEY(task_id,run_id) REFERENCES task_runs(task_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(task_id,run_id,active_attempt_id) REFERENCES task_attempts(task_id,run_id,id) ON DELETE RESTRICT
);
CREATE UNIQUE INDEX task_session_bindings_one_active_idx ON task_session_bindings(engine,session_id) WHERE released_at IS NULL;
CREATE TABLE task_requests (
 actor_key TEXT NOT NULL,
 project_id TEXT NOT NULL,
 request_id TEXT NOT NULL,
 action TEXT NOT NULL,
 input_hash TEXT NOT NULL,
 task_id TEXT NOT NULL,
 response_json TEXT NOT NULL,
 response_status INTEGER NOT NULL,
 created_at TIMESTAMP NOT NULL,
 PRIMARY KEY(actor_key,project_id,request_id),
 FOREIGN KEY(task_id,project_id) REFERENCES tasks(id,project_id) ON DELETE RESTRICT
);
