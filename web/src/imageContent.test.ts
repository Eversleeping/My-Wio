import { expect, test } from "vitest";
import { extractImageSources, imageContentText, previewImageSource, workspaceImagePath } from "./imageContent";

test("extracts native Codex, Claude and Gemini screenshot blocks", () => {
  const source = "data:image/png;base64,AAAA";
  for (const block of [
    { type: "image", url: source },
    { data_url: source },
    { type: "image_url", image_url: { url: source } },
    { type: "image", source: { type: "base64", media_type: "image/png", data: "AAAA" } },
    { type: "content", content: { type: "image", mimeType: "image/png", data: "AAAA" } },
  ]) expect(extractImageSources({ result: { content: [block, block] } })).toEqual([source]);
  expect(extractImageSources({ type: "imageView", path: "/repo/screenshot.PNG" })).toEqual(["/repo/screenshot.PNG"]);
  expect(extractImageSources({ type: "image", source: { type: "base64", media_type: "image/svg+xml", data: "AAAA" } })).toEqual([]);
});

test("resolves document-relative images and rejects paths outside the workspace", () => {
  expect(workspaceImagePath("../images/screen.png", "/repo", "docs/guide.md")).toBe("images/screen.png");
  expect(workspaceImagePath("file:///repo/screen.jpg", "/repo")).toBe("screen.jpg");
  expect(workspaceImagePath("/repo/screen%20shot.PNG", "/repo")).toBe("screen shot.PNG");
  for (const path of ["../private.png", "/repo-other/screen.png", "/tmp/screen.png", "https://example.com/screen.png", "javascript:screen.png", "//example.com/screen.png"]) expect(workspaceImagePath(path, "/repo")).toBe("");
});

test("keeps image bytes out of text output and only renders complete raster previews", () => {
  expect(imageContentText([{ type: "text", text: "Screenshot" }, { type: "image", data: "AAAA", mimeType: "image/png" }])).toBe("Screenshot");
  expect(previewImageSource({ path: "a.png", content: "AAAA", encoding: "base64", mime_type: "image/png" })).toBe("data:image/png;base64,AAAA");
  expect(previewImageSource({ path: "a.png", content: "AAAA", encoding: "base64", mime_type: "image/png", truncated: true })).toBe("");
  expect(previewImageSource({ path: "a.svg", content: "AAAA", encoding: "base64", mime_type: "image/svg+xml" })).toBe("");
});
