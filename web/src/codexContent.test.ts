import { describe, expect, test } from "vitest";
import { safeImageSource, workspaceFileLink } from "./codexContent";

describe("Codex content links", () => {
  test("resolves workspace file links and line numbers", () => {
    expect(workspaceFileLink("file:///srv/project/src/main.go#L42", "/srv/project")).toEqual({ path: "src/main.go", line: 42 });
    expect(workspaceFileLink("src/main.go:12:4", "/srv/project")).toEqual({ path: "src/main.go", line: 12 });
  });

  test("rejects external and escaping workspace links", () => {
    expect(workspaceFileLink("https://example.com/file.go", "/srv/project")).toBeNull();
    expect(workspaceFileLink("../outside.txt", "/srv/project")).toBeNull();
  });

  test("allows displayable image sources only", () => {
    expect(safeImageSource("https://example.com/image.png")).toBe("https://example.com/image.png");
    expect(safeImageSource("data:image/png;base64,AAAA")).toBe("data:image/png;base64,AAAA");
    expect(safeImageSource("javascript:alert(1)")).toBe("");
  });
});
