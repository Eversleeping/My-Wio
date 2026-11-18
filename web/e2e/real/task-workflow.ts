import { expect, test, type Page } from "@playwright/test";
import { defaultBudgets, type Artifact, type Member, type Step, type TaskDetail, type TaskReview } from "../../src/data/tasks";

export async function runTaskWorkflow(page: Page, baseURL: string, headers: Record<string, string>) {
  const get = async <T>(path: string): Promise<T> => {
    const response = await page.request.get(baseURL + "/api" + path);
    expect(response.ok(), await response.text()).toBe(true);
    return response.json();
  };
  const send = async <T>(path: string, data: object, status: number, method = "POST"): Promise<T> => {
    const response = await page.request.fetch(baseURL + "/api" + path, { headers, data, method });
    expect(response.status(), await response.text()).toBe(status);
    return response.json();
  };
  type Workspace = { id: string; project_id: string; path: string; status: string };
  const workspaces = await get<Workspace[]>("/workspaces");
  const workspace = workspaces.find(item => item.path === process.env.WIO_E2E_WORKSPACE_PATH);
  expect(workspace).toBeTruthy();
  const ready = async (branch: string) => {
    const result = await send<{ operation_id: string; workspace_id: string }>(`/workspaces/${workspace!.id}/worktrees`, { branch }, 202);
    await expect.poll(async () => (await get<{ status: string }>(`/operations/${result.operation_id}`)).status).toBe("succeeded");
    return result.workspace_id;
  };
  const reviewWorkspace = await ready("p7-review");
  const researchWorkspace = await ready("p7-research");
  const project = workspace!.project_id;
  const { task } = await send<{ task: TaskDetail["task"] }>("/tasks", {
    request_id: "p7-real-create", project_id: project, title: "Review a versioned artifact", requirement: "Run development, research and review",
    criteria: [{ id: "passes", text: "Inspect the prepared version" }]
  }, 201);
  const root = `/tasks/${task.id}`;
  const scope = `?project_id=${project}`;
  const detail = () => get<TaskDetail>(root + scope);
  const readReviews = () => get<{ items: TaskReview[] }>(root + "/reviews" + scope);
  const members: Member[] = [
    {id:"coordinator", role:"coordinator",engine:"codex",workspace_id:workspace!.id,mode:"on-request",model:"",effort:"",directory_mode:"shared"},
    {id:"developer", role:"developer",engine:"claude",workspace_id:workspace!.id,mode:"default",model:"sonnet",effort:"",directory_mode:"shared"},
    {id:"researcher", role:"researcher",engine:"gemini",workspace_id:researchWorkspace,mode:"default",model:"flash",effort:"",directory_mode:"existing_worktree"},
    {id:"reviewer", role:"reviewer",engine:"gemini",workspace_id:reviewWorkspace,mode:"default",model:"flash",effort:"",directory_mode:"existing_worktree"}
  ];
  const step = (id: string, title: string, member_id: string, kind: Step["kind"], completion_policy: Step["completion_policy"], depends_on: string[], instructions: string): Step => ({
    id,title,member_id,kind,completion_policy,depends_on,instructions,required:true,criterion_ids:kind==="review"?["passes"]:[],
    input_refs:kind==="review"?[{kind:"artifact",step_id:"develop"}]:[]
  });
  const steps = [
    step("plan","Plan implementation","coordinator","plan","execution",[],"Plan the fixture."),
    step("develop","Produce code","developer","develop","artifact",["plan"],"P7-09 update the fixture output"),
    step("research","Research constraints","researcher","research","execution",["plan"],"Research the fixture."),
    step("review","Review prepared version","reviewer","review","review",["develop"],"Inspect the fixture."),
    step("summarize","Summarize checks","coordinator","summarize","execution",["research","review"],"Summarize the evidence.")
  ];
  const plan = await send<{ task_version: number; plan: {id:string} }>(root + "/plans" + scope, {
    request_id:"p7-real-plan",expected_version:task.version, members,steps,reason:"Explicit browser acceptance plan"
  }, 201);
  const grant = await send<{ task_version: number; grant: {revision:number} }>(root + "/grant" + scope, {
    request_id:"p7-real-grant",expected_version:plan.task_version,enabled:true,task_tools:true,budgets:defaultBudgets,
    allowed_members: members.map(item => item.id), workspace_ids: [...new Set(members.map(item => item.workspace_id))],
    execution_modes: Object.fromEntries(members.map(item => [item.id, {engine:item.engine,mode:item.mode}]))
  }, 200, "PUT");
  const { run } = await send<{ run: {id:string} }>(root + "/runs" + scope, {
    request_id:"p7-real-start",expected_version:grant.task_version,plan_id:plan.plan.id,grant_revision:grant.grant.revision
  }, 201);
  const pageURL = `${baseURL}/?view=tasks&project_id=${project}&task=${task.id}`;
  let previousDigest = "";
  for (let round = 1; round <= 2; round++) {
    await test.step(`Capture and review artifact ${round}`, async () => {
      await expect.poll(async () => {
        const snapshot = await detail();
        return snapshot.steps.find(item => item.step_id === "develop")?.block_code === "artifact_required" &&
          snapshot.attempts.some(item => item.step_id === "develop" && item.number === round && item.state === "succeeded");
      }, { timeout: 40_000 }).toBe(true);
      const before = await detail();
      const attempt = before.attempts.find(item => item.step_id === "develop" && item.number === round)!;
      const capture = await send<Artifact>(root + "/artifacts" + scope, {
        request_id:`p7-capture-${round}`,expected_version:before.task.version,attempt_id:attempt.id,paths:["task-output.txt"]
      }, 202);
      const getArtifact = async () => (await get<{items:Artifact[]}>(root + "/artifacts" + scope)).items.find(item => item.id === capture.id);
      await expect.poll(async () => (await getArtifact())?.state).toBe("ready");
      const artifact = (await getArtifact())!;
      expect(artifact.digest).not.toBe(previousDigest);
      previousDigest = artifact.digest;
      const prepared = await send<{operation_id:string}>(root + `/artifacts/${artifact.id}/handoffs` + scope, {
        request_id:`p7-prepare-${round}`,expected_version:(await detail()).task.version,action:"apply",workspace_id:reviewWorkspace,digest:artifact.digest
      }, 202);
      await expect.poll(async () => (await get<{ status: string }>(`/operations/${prepared.operation_id}`)).status).toBe("succeeded");
      await expect.poll(async () => (await readReviews()).items.find(item => item.artifact_id === artifact.id)?.evidence_hash ?? "").not.toBe("");
      await page.goto(pageURL);
      const review = page.getByRole("region", { name: "Reviews", exact: true });
      await review.getByRole("button", { name: "Record review" }).click();
      const dialog = page.getByRole("dialog", { name: "Record review" });
      await dialog.getByRole("combobox", {name:"Status",exact:true}).selectOption(round === 1 ? "failed" : "passed");
      await dialog.getByLabel("Execution evidence", {exact:true}).fill("Inspected the prepared artifact and native review session");
      await dialog.getByRole("combobox", {name:"Verdict",exact:true}).selectOption(round === 1 ? "rejected" : "approved");
      await dialog.getByLabel("Reason", {exact:true}).fill(round === 1 ? "Request one improvement" : "The new version passes");
      await dialog.getByRole("button", {name:"Save",exact:true}).click();
      await expect(dialog).not.toBeVisible();
      if (round === 1) {
        await review.getByRole("button", {name:"Rework artifact",exact:true}).click();
        const rework = page.getByRole("dialog", {name:"Rework artifact",exact:true});
        await rework.getByLabel("Reason").fill("Make the requested improvement");
        await rework.getByRole("button", {name:"Rework artifact",exact:true}).click();
        await expect(rework).not.toBeVisible();
      }
    });
  }
  await expect.poll(async () => (await detail()).run?.state, { timeout: 40_000 }).toBe("awaiting_acceptance");
  const accept = page.getByRole("button", {name:"Accept result",exact:true});
  await accept.click();
  const acceptance = page.getByRole("dialog", {name:"Accept result",exact:true});
  await acceptance.getByLabel("Reason").fill("Reviewed both versions and the final execution evidence");
  await acceptance.getByRole("button", {name:"Accept result",exact:true}).click();
  await expect.poll(async () => (await detail()).run?.state).toBe("accepted");
  expect((await detail()).run?.id).toBe(run.id);
  const requests = await get<{items:{kind:string;state:string;artifact_id:string|null}[]}>(root + "/team" + scope);
  const requested = requests.items.filter(item => item.kind === "wio_request_task_review");
  expect(requested.map(item => item.state)).toEqual(["fulfilled", "fulfilled"]);
  expect(new Set(requested.map(item => item.artifact_id)).size).toBe(2);

  for (const viewport of [{width:1440,height:900},{width:390,height:844}]) {
    await page.setViewportSize(viewport);
    await page.goto(pageURL);
    const graph = page.getByRole("group", {name:"Dependency graph",exact:true});
    await expect(graph.getByText("Reworks: 1", {exact:true})).toBeVisible();
    await expect(graph.getByText("Review decision", {exact:true})).toBeVisible();
    await expect(page.getByRole("region", {name:"Artifacts",exact:true}).getByText("task-output.txt", {exact:true})).toHaveCount(2);
    const review = page.locator("#task-step-review");
    await review.getByRole("link", {name:"Open session",exact:true}).first().click();
    await expect(page).toHaveURL(/view=sessions&engine=gemini&thread=/);
    await expect(page.locator(".session-header h2")).toContainText("Review a versioned artifact");
  }
}
