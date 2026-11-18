import { afterEach, expect, test } from "vitest";
import { locationFor, navigation, readLocationState, tasksLocation, workspaceSessionsLocation } from "./navigation";
afterEach(() => window.history.replaceState(null, "", "/"));
for (const engine of ["codex", "gemini", "claude"] as const) {
  test(`keeps persisted ${engine} result/notification URLs compatible`, () => {
    const historicalResult = { session: { url: `/?view=${engine}&thread=old%2Fid` } };
    window.history.replaceState(null, "", historicalResult.session.url);
    expect(readLocationState()).toEqual({view:"sessions",engine,threadID:"old/id"});
    expect(locationFor("sessions", "old/id", engine)).toBe(`/?view=sessions&engine=${engine}&thread=old%2Fid`);
    window.history.replaceState(null, "", `/?view=${engine}`);
    expect(readLocationState()).toEqual({view:"sessions",engine,threadID:"",initialFilter:engine});
  });
}
test("offers a single session navigation entry and preserves subpaths and fragments", () => {
  expect(navigation.map(item => item.id)).toEqual(["dashboard","servers","projects","sessions","tasks","deployments","operations","monitoring","settings"]);
  window.history.replaceState(null,"","/console?view=claude&thread=old#reply");
  expect(locationFor("sessions","old","claude")).toBe("/console?view=sessions&engine=claude&thread=old#reply");
});

test("encodes and reads a one-time project workspace intent", () => {
  expect(workspaceSessionsLocation("w / one",true)).toBe("/?view=sessions&workspace=w+%2F+one&create=1");
  window.history.replaceState(null,"",workspaceSessionsLocation("w / one",true));
  expect(readLocationState().entry).toEqual({workspaceID:"w / one",create:true});
});

test("encodes task deep links within their project", () => {
  window.history.replaceState(null, "", "/console#review");
  const location = tasksLocation({ projectID: "project/a", taskID: "task 2" });
  expect(location).toBe("/console?view=tasks&project_id=project%2Fa&task=task+2#review");
  window.history.replaceState(null, "", location);
  expect(readLocationState().task).toEqual({ projectID: "project/a", taskID: "task 2", create: false });
});
