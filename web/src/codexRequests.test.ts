import { afterEach, expect, test, vi } from "vitest";
import { CodexRequests } from "./codexRequests";

function response(value: unknown, status = 200) {
  return new Response(JSON.stringify(value), { status });
}

afterEach(() => vi.unstubAllGlobals());

test("retries the same Codex steer and request_id after the turn has finished", async () => {
  const fetchMock = vi.fn().mockRejectedValueOnce(new TypeError("response lost")).mockResolvedValue(response({ operation_id: "op" }));
  vi.stubGlobal("fetch", fetchMock);
  const requests = new CodexRequests();
  const draft = { prompt: "correction" };
  await expect(requests.send("composer", draft, "/threads/t/steer", draft)).rejects.toThrow("response lost");
  await requests.send("composer", draft, "/threads/t/turns", { ...draft, model: "model" });
  expect(fetchMock.mock.calls[1][0]).toBe("/api/threads/t/steer");
  expect(fetchMock.mock.calls[1][1].body).toBe(fetchMock.mock.calls[0][1].body);
  expect(JSON.parse(fetchMock.mock.calls[0][1].body).request_id).toBeTruthy();
});

test("new drafts and successfully sent messages receive fresh request IDs", async () => {
  const fetchMock = vi.fn().mockImplementation(async () => response({})).mockRejectedValueOnce(new TypeError("offline"));
  vi.stubGlobal("fetch", fetchMock);
  const requests = new CodexRequests();
  await expect(requests.send("composer", "first", "/threads/t/turns", { prompt: "first" })).rejects.toThrow();
  await requests.send("composer", "changed", "/threads/t/turns", { prompt: "changed" });
  await requests.send("composer", "changed", "/threads/t/turns", { prompt: "changed" });
  const ids = fetchMock.mock.calls.map(call => JSON.parse(call[1].body).request_id);
  expect(new Set(ids).size).toBe(3);
});

test("an explicitly rejected steer can fall back to a new turn", async () => {
  const fetchMock = vi.fn().mockResolvedValueOnce(response({ code: "steer_inactive", error: "finished" }, 409)).mockResolvedValue(response({}));
  vi.stubGlobal("fetch", fetchMock);
  const requests = new CodexRequests();
  await expect(requests.send("answer", "same answer", "/threads/t/steer", { prompt: "answer" })).rejects.toMatchObject({ code: "steer_inactive" });
  await requests.send("answer", "same answer", "/threads/t/turns", { prompt: "answer" });
  expect(fetchMock.mock.calls[1][0]).toBe("/api/threads/t/turns");
  expect(JSON.parse(fetchMock.mock.calls[1][1].body).request_id).not.toBe(JSON.parse(fetchMock.mock.calls[0][1].body).request_id);
});

test("question answers and goal requests retain their keys after server timeouts", async () => {
  const fetchMock = vi.fn().mockResolvedValueOnce(response({ error: "timeout" }, 504)).mockResolvedValue(response({}));
  vi.stubGlobal("fetch", fetchMock);
  const requests = new CodexRequests();
  await expect(requests.send("question:1", ["t", "answer"], "/threads/t/turns", { prompt: "answer" })).rejects.toMatchObject({ status: 504 });
  await requests.send("question:1", ["t", "answer"], "/threads/t/turns", { prompt: "answer" });
  expect(fetchMock.mock.calls[1][1].body).toBe(fetchMock.mock.calls[0][1].body);
});
