import { expect, test, type WebSocketRoute } from "@playwright/test";
import { assertMockApplication, installMockApplication } from "./support/mock-api";
import { defaultBudgets, type Member, type Plan, type Run, type Step, type Task, type TaskDetail } from "../src/data/tasks";

const workspace = { id: "workspace", project_id: "project", server_id: "server", server_name: "Build server",
  project_name: "Project", display_name: "Feature", path: "/srv/project", status: "ready", management_mode: "managed" };
const engines = ["codex", "gemini", "claude"] as const;
const initialTask: Task = {
  id: "feature", project_id: "project", title: "Ship the feature", requirement: "Provide a reliable feature",
  criteria: [{id: "works", text: "All checks pass"}], state: "active", version: 7, spec_version: 1,
  archived_at: null, updated_at: "2026-11-18T00:00:00Z", counters: { runs: 1, attempts: 3, plans: 1 }
};
const members: Member[] = engines.map((engine, index) => ({
  id: engine, engine, role: index === 0 ? "coordinator" : "developer", workspace_id: workspace.id,
  model: "", mode: engine === "codex" ? "on-request" : "default", effort: "", directory_mode: "shared"
}));
const steps: Step[] = engines.map(engine => ({
  id: engine, member_id: engine, kind: "develop", title: `Implement with ${engine}`, instructions: "Check the work.",
  input_refs: [], depends_on: engine === "codex" ? [] : ["codex"], criterion_ids: ["works"],
  required: true, completion_policy: "execution"
}));
const initialPlan: Plan = { id: "plan-v1", revision: 1, spec_version: 1, members, steps, reason: "Explicit plan" };

test.afterEach(({ page }) => assertMockApplication(page));

test("task snapshots recover without events and link three native sessions", async ({ page }) => {
  test.setTimeout(60000);
  const run: Run = { id: "run", state: "blocked", version: 3, deadline_at: null };
  let archive = false;
  const projections = engines.map((engine, index) => ({
    step_id: engine, state: index === 0 ? "succeeded" : "blocked", current_attempt_id: engine,
    block_code: index === 1 ? "agent_update" : index === 2 ? "workspace_not_ready" : null
  }));
  const sessions = engines.map(engine => ({
    engine, id: engine, title: `${engine} native conversation`, status: "idle", server_status: "online",
    workspace_id: workspace.id, project_id: "project", server_id: "server", project_name: "Project",
    server_name: "Build server", path: workspace.path, model: "fixture", mode: "default",
    approval_mode: "default", permission_mode: "default", capabilities: {}, archived_at: null,
    native_id: `native-${engine}`, session_id: `native-${engine}`, codex_thread_id: `native-${engine}`,
    created_at: initialTask.updated_at, updated_at: initialTask.updated_at
  }));
  let readCount = 0;
  let decision: Record<string, unknown> | undefined;
  await installMockApplication(page, { configured: true, onAPIRequest: request => {
    const task = { ...initialTask, state: run.state === "blocked" || run.state === "paused" ? "active" : run.state, archived_at: archive ? initialTask.updated_at : null };
    const snapshot = {
      task, run, plan: initialPlan, steps: projections, blockers: [],
      attempts: engines.map(engine => ({ id: engine, step_id: engine, engine, session_id: engine, operation_id: `operation-${engine}`, number: 1, state: "succeeded", result_summary: "Model said done", outcome_certainty: "confirmed" })),
      grant: { enabled: true, revision: 1, budgets: defaultBudgets }, capabilities: { edit_plan: false, start_run: false },
      acceptance_evidence_hash: run.state === "awaiting_acceptance" ? "evidence-digest" : undefined
    };
    if (request.path === "/projects") return { body: [{ id: "project", name: "Project" }] };
    if (request.path === "/workspaces") return { body: [workspace] };
    if (request.path === "/servers") return { body: [{ id: "server", status: "online" }] };
    if (request.path === "/tasks/feature/location") return { body: { project_id: "project" } };
    if (request.path === "/tasks") return { body: { items: [task], next_cursor: null } };
    if (request.path === "/tasks/feature") {
      expect(new URLSearchParams(request.search).get("project_id")).toBe("project");
      readCount++;
      return { body: snapshot };
    }
    if (request.path === "/tasks/feature/runs/run/actions") {
      const body = JSON.parse(request.body!);
      expect(body).toMatchObject({ action: "pause", expected_version: 3, reason: "Check the environment" });
      run.state = "paused";
      run.version++;
      return { status: 202, body: { run } };
    }
    if (request.path === "/tasks/feature/runs/run/acceptance") {
      decision = JSON.parse(request.body!);
      run.state = "accepted";
      run.version++;
      run.acceptance_reason = String(decision?.reason);
      return { body: { run } };
    }
    if (request.path === "/tasks/feature/archive") {
      archive = Boolean(JSON.parse(request.body!).archived);
      return { body: { task: { ...task, archived_at: initialTask.updated_at } } };
    }
    if (request.path === "/sessions") return { body: { items: sessions, next_cursor: null, groups: [{workspace_id: workspace.id, matched_count: 3}] } };
    const session = sessions.find(item => request.path === `/sessions/${item.engine}/${item.id}`);
    if (session) return { body: session };
    if (/\/events$/.test(request.path)) return { body: [] };
    if (/\/goal$/.test(request.path)) return { body: { status: "succeeded", supported: true, data: null, error: "" } };
    if (/\/workspaces\/workspace\/(gemini|claude)\/models$/.test(request.path)) return { body: { configured: true, models: [{ id: "fixture", name: "Fixture" }], default_model: "fixture" } };
    if (request.path === "/workspaces/workspace/files") return { body: { files: [], status: "succeeded", error: "", truncated: false } };
    return undefined;
  }});
  let socket: WebSocketRoute | undefined;
  await page.routeWebSocket("**/api/ws", ws => { socket = ws; });
  await page.goto("/?view=tasks&task=feature");
  const task = page.getByRole("region", { name: initialTask.title });
  await expect(task.getByRole("heading", {name: initialTask.title})).toBeVisible();
  await expect(task.getByText("This Agent or engine does not support starting turns. Check its version.")).toBeVisible();
  await expect(task.getByText("The workspace is not ready.")).toBeVisible();
  await expect(task.getByRole("link", { name: "Open session" })).toHaveCount(3);
  await expect(task.getByText("Accepted", { exact: true })).toHaveCount(0);
  await task.getByRole("button", { name: "Pause run" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Reason", { exact: true }).fill("Check the environment");
  await dialog.getByRole("button", { name: "Pause run" }).click();
  await expect(task.getByText("Paused", { exact: true })).toBeVisible();

  projections[1].block_code = "attempt_budget";
  const before = readCount;
  await expect(page.locator(".connection")).toContainText("Live");
  socket?.close();
  await expect(task.getByText("The task's execution budget is exhausted.")).toBeVisible();
  expect(readCount).toBeGreaterThan(before);

  run.state = "awaiting_acceptance";
  run.version++;
  projections.forEach(item => { item.state = "succeeded"; item.block_code = null; });
  await page.reload();
  await expect(task.getByText("Awaiting acceptance", { exact: true })).toBeVisible();
  await expect(task.getByText("Accepted", { exact: true })).toHaveCount(0);
  for (const engine of engines) {
    await task.getByRole("listitem").filter({ has: page.getByRole("heading", { name: `Implement with ${engine}` }) })
      .getByRole("link", { name: "Open session" }).click();
    await expect(page).toHaveURL(new RegExp(`view=sessions&engine=${engine}&thread=${engine}`));
    await expect(page.locator(".session-header h2")).toHaveText(`${engine} native conversation`);
    await page.goBack();
    await expect(task.getByText("Awaiting acceptance", { exact: true })).toBeVisible();
  }
  const { scrollWidth, innerWidth } = await page.evaluate(() => ({ scrollWidth: document.documentElement.scrollWidth, innerWidth: window.innerWidth }));
  expect(scrollWidth).toBeLessThanOrEqual(innerWidth);
  await task.getByRole("button", { name: "Accept result" }).click();
  await dialog.getByLabel("Reason", { exact: true }).fill("Reviewed the session results");
  await dialog.getByRole("button", { name: "Accept result" }).click();
  await expect(task.getByText("Accepted", { exact: true })).toBeVisible();
  expect(decision).toMatchObject({ decision: "accepted", evidence_hash: "evidence-digest", reason: "Reviewed the session results" });
  await task.getByRole("button", { name: "Archive task" }).click();
  await dialog.getByRole("button", { name: "Archive task" }).click();
  await expect(task.getByText("Archived", { exact: true })).toBeVisible();
});

test("project task creation saves a plan and starts only after separate authorization", async ({ page }) => {
  let detail: TaskDetail | undefined;
  let lostRequest = "";
  await installMockApplication(page, { configured: true, onAPIRequest: request => {
    const body = request.body ? JSON.parse(request.body) : {};
    if (request.path === "/projects") return { body: [{ id: "project", name: "Project", workspace_count: 1, status: "ready", updated_at: initialTask.updated_at }] };
    if (request.path === "/servers") return { body: [] };
    if (request.path === "/workspaces") return { body: [workspace] };
    if (request.path === "/tasks" && request.method === "GET") return { body: { items: detail ? [detail.task] : [], next_cursor: null } };
    if (request.path === "/tasks" && request.method === "POST") {
      expect(body).toMatchObject({ project_id: "project", title: "Draft feature", requirement: "Build the feature" });
      expect(body.criteria).toMatchObject([{ text: "Tests pass" }]);
      const task = { ...initialTask, title: body.title, requirement: body.requirement, criteria: body.criteria, version: 1, state: "draft" as const };
      detail = { task, plan: null, run: null, steps: [], attempts: [], blockers: [], grant: null,
        capabilities: { edit_plan: true, start_run: false } };
      lostRequest = body.request_id;
      return { status: 503, body: { error: "Lost response after saving" } };
    }
    if (request.path === `/task-requests/${lostRequest}`) return { body: { response: { task: detail!.task } } };
    if (request.path === "/tasks/feature") return { body: detail };
    if (request.path === "/tasks/feature/plans") {
      expect(body.expected_version).toBe(1);
      expect(body.members).toMatchObject([{engine: "gemini", workspace_id: "workspace", mode: "default"}]);
      expect(body.steps).toMatchObject([{title: "Build", member_id: body.members[0].id}]);
      detail!.plan = { ...initialPlan, steps: body.steps, members: body.members };
      detail!.task.version = 2;
      return { status: 201, body: { plan: detail!.plan, task_version: 2 } };
    }
    if (request.path === "/tasks/feature/grant") {
      expect(body.expected_version).toBe(2);
      expect(body.workspace_ids).toEqual(["workspace"]);
      expect(body.budgets.concurrency).toBe(1);
      detail!.grant = { enabled: true, revision: 1, budgets: body.budgets };
      detail!.task.version = 3;
      detail!.capabilities.start_run = true;
      return { body: { grant: detail!.grant, task_version: 3 } };
    }
    if (request.path === "/tasks/feature/runs") {
      expect(body).toMatchObject({ expected_version: 3, plan_id: "plan-v1", grant_revision: 1 });
      detail!.run = { id: "run-new", state: "running", version: 1, deadline_at: null };
      detail!.task.state = "active";
      detail!.capabilities = { edit_plan: false, start_run: false };
      return { status: 201, body: { run: detail!.run, task_version: 4 } };
    }
    if (request.path === "/tasks/feature/runs/run-new/actions") {
      expect(body).toMatchObject({action: "cancel", expected_version: 1, reason: "Stop the current run"});
      detail!.run!.state = "cancelling";
      detail!.run!.version = 2;
      return { status: 202, body: { run: detail!.run } };
    }
    return undefined;
  }});
  await page.goto("/?view=projects");
  await page.getByRole("link", { name: "New task", exact: true }).click();
  await expect(page).toHaveURL("/?view=tasks&project_id=project&create=1");
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Title", { exact: true }).fill("Draft feature");
  await dialog.getByLabel("Requirement").fill("Build the feature");
  await dialog.getByLabel("Criterion 1", { exact: true }).fill("Tests pass");
  await dialog.getByRole("button", { name: "Create draft" }).click();
  const task = page.getByRole("region", { name: "Draft feature" });
  await expect(task.getByText("Draft", { exact: true })).toBeVisible();
  await task.getByRole("button", { name: "Edit plan" }).click();
  await dialog.getByRole("combobox", {name: "Engine", exact: true}).selectOption("gemini");
  await dialog.getByLabel("Title", { exact: true }).fill("Build");
  await dialog.getByLabel("Instructions").fill("Run the feature check");
  await dialog.getByLabel("Reason", { exact: true }).fill("Explicit user plan");
  await dialog.getByRole("button", { name: "Save plan" }).click();
  await expect(task.getByRole("heading", { name: "Build", exact: true })).toBeVisible();
  await expect(task.getByRole("button", { name: "Start run" })).toHaveCount(0);
  await task.getByRole("button", { name: "Execution authorization" }).click();
  await dialog.getByLabel("Concurrent steps").fill("1");
  await dialog.getByRole("button", { name: "Authorize plan" }).click();
  await task.getByRole("button", { name: "Start run" }).click();
  await dialog.getByRole("button", { name: "Start run" }).click();
  await expect(task.getByText("Running", { exact: true })).toBeVisible();
  await task.getByRole("button", { name: "Cancel run" }).click();
  await dialog.getByLabel("Reason", { exact: true }).fill("Stop the current run");
  await dialog.getByRole("button", { name: "Cancel run" }).click();
  await expect(task.getByText("Cancelling", { exact: true })).toBeVisible();
});

test("dependency graph flags a cycle and preserves a concurrent plan revision", async ({ page }) => {
  const detail: TaskDetail = {
    task: {...initialTask, state:"draft", version:4}, plan:initialPlan, run:null, steps:[], attempts:[], blockers:[], grant:null,
    capabilities:{edit_plan:true,start_run:false,graph:true}
  };
  let writes = 0;
  await installMockApplication(page, {configured:true,onAPIRequest:request => {
    if (request.path === "/projects") return {body:[{id:"project",name:"Project"}]};
    if (request.path === "/workspaces") return {body:[workspace]};
    if (request.path === "/tasks") return {body:{items:[detail.task],next_cursor:null}};
    if (request.path === "/tasks/feature") return {body:detail};
    if (request.path === "/tasks/feature/plans" && request.method === "GET") return {body:{items:[{id:"plan-v2",revision:2},{id:"plan-v1",revision:1}]}};
    if (request.path === "/tasks/feature/plans/plan-v1") return {body:initialPlan};
    if (request.path === "/tasks/feature/plans" && request.method === "POST") {
      writes++;
      expect(JSON.parse(request.body!).expected_version).toBe(4);
      detail.task.version = 5;
      detail.plan = {...initialPlan, id:"plan-v2", revision:2};
      return {status:409,body:{code:"VERSION_CONFLICT",error:"task version changed"}};
    }
    return undefined;
  }});
  await page.goto("/?view=tasks&project_id=project&task=feature");
  const graph = page.getByRole("group",{name:"Dependency graph",exact:true});
  await expect(graph.getByRole("button",{name:/Implement with gemini/})).toContainText("Depends on Implement with codex");
  await page.getByRole("button",{name:"Edit plan",exact:true}).click();
  const dialog = page.getByRole("dialog",{name:"Edit plan",exact:true});
  const first = dialog.getByRole("group",{name:"Step 1",exact:true});
  const backEdge = first.getByRole("checkbox",{name:"Implement with gemini",exact:true});
  await backEdge.check();
  await expect(first.getByText("This step has a missing dependency or depends on a cycle.",{exact:true})).toBeVisible();
  await expect(dialog.getByRole("button",{name:"Save plan",exact:true})).toBeDisabled();
  expect(writes).toBe(0);
  await backEdge.uncheck();
  await dialog.getByLabel("Reason",{exact:true}).fill("Change the plan");
  await dialog.getByRole("button",{name:"Save plan",exact:true}).click();
  await expect(dialog.getByRole("alert")).toContainText("task version changed");
  await dialog.getByRole("button",{name:"Close",exact:true}).click();
  await expect(page.locator(".task-detail").getByText("Plan v2",{exact:true}).first()).toBeVisible();
  await page.getByRole("combobox",{name:"Plan version",exact:true}).selectOption("plan-v1");
  await expect(graph.getByText("Implement with codex",{exact:true})).toBeVisible();
  expect(writes).toBe(1);
});
