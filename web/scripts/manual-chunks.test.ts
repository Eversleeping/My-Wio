// @vitest-environment node

import { describe, expect, test } from "vitest";
import { manualChunks } from "../vite.config";

const moduleId = (packageName: string) => `D:/workspace/web/node_modules/${packageName}/index.js`;

describe("manualChunks", () => {
  test("keeps React runtime modules in one shared vendor chunk", () => {
    expect(manualChunks(moduleId("react"))).toBe("vendor-react");
    expect(manualChunks(moduleId("react-dom"))).toBe("vendor-react");
    expect(manualChunks(moduleId("scheduler"))).toBe("vendor-react");
  });

  test("keeps markdown parsing and icons in their own stable chunks", () => {
    expect(manualChunks(moduleId("react-markdown"))).toBe("vendor-markdown");
    expect(manualChunks(moduleId("remark-gfm"))).toBe("vendor-markdown");
    expect(manualChunks(moduleId("lucide-react"))).toBe("vendor-icons");
  });

  test("leaves application code and lazy-only dependencies to Rollup", () => {
    expect(manualChunks("D:/workspace/web/src/App.tsx")).toBeUndefined();
    expect(manualChunks(moduleId("prism-react-renderer"))).toBeUndefined();
  });
});
