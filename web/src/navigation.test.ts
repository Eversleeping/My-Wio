import { afterEach, expect, test } from "vitest";
import { locationFor, navigation, readLocationState } from "./navigation";
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
  expect(navigation.map(item => item.id)).toEqual(["dashboard","servers","projects","sessions","deployments","operations","monitoring","settings"]);
  window.history.replaceState(null,"","/console?view=claude&thread=old#reply");
  expect(locationFor("sessions","old","claude")).toBe("/console?view=sessions&engine=claude&thread=old#reply");
});
