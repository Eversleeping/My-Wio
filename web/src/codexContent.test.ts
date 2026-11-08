import { describe, expect, test } from "vitest";
import { remoteImageURL, safeImageSource, workspaceFileLink } from "./codexContent";

describe("Codex content links", () => {
  test("resolves workspace file links and line numbers", () => {
    expect(workspaceFileLink("file:///srv/project/src/main.go#L42", "/srv/project")).toEqual({ path: "src/main.go", line: 42 });
    expect(workspaceFileLink("src/main.go:12:4", "/srv/project")).toEqual({ path: "src/main.go", line: 12 });
  });

  test("rejects external and escaping workspace links", () => {
    expect(workspaceFileLink("https://example.com/file.go", "/srv/project")).toBeNull();
    expect(workspaceFileLink("../outside.txt", "/srv/project")).toBeNull();
  });

  test("loads only embedded, local and same-origin images automatically", () => {
    for (const type of ["png", "jpeg", "gif", "webp"]) expect(safeImageSource(`data:image/${type};base64,AAAA`)).toBe(`data:image/${type};base64,AAAA`);
    expect(safeImageSource(`${location.origin}/api/files/image.png`)).toBe(`${location.origin}/api/files/image.png`);
    expect(safeImageSource(`blob:${location.origin}/0f9c1c1e`)).toBe(`blob:${location.origin}/0f9c1c1e`);
    expect(safeImageSource("https://example.com/image.png")).toBe("");
    expect(safeImageSource("//example.com/image.png")).toBe("");
    expect(safeImageSource("data:image/svg+xml;base64,AAAA")).toBe("");
    expect(safeImageSource("javascript:alert(1)")).toBe("");
  });

  test("identifies remote images that may only be opened on request", () => {
    expect(remoteImageURL("https://attacker.example/pixel.png?secret=1")).toBe("https://attacker.example/pixel.png?secret=1");
    expect(remoteImageURL("//attacker.example/pixel.png")).toBe("");
    expect(remoteImageURL(`${location.origin}/image.png`)).toBe("");
    expect(remoteImageURL("data:image/png;base64,AAAA")).toBe("");
    expect(remoteImageURL("javascript:alert(1)")).toBe("");
  });
});
