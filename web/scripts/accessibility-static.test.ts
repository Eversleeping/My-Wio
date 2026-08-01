// @vitest-environment node

import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import ts from "typescript";
import { describe, expect, test } from "vitest";

function sourceFiles(directory: string): string[] {
  return readdirSync(directory, { withFileTypes: true }).flatMap(entry => {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) return sourceFiles(path);
    return entry.isFile() && entry.name.endsWith(".tsx") && !entry.name.endsWith(".test.tsx") ? [path] : [];
  });
}

describe("static accessibility safeguards", () => {
  test("requires an accessible name on every icon button", () => {
    const missing: string[] = [];
    for (const path of sourceFiles("src")) {
      const source = readFileSync(path, "utf8");
      const file = ts.createSourceFile(path, source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
      const visit = (node: ts.Node) => {
        if (ts.isJsxOpeningElement(node) || ts.isJsxSelfClosingElement(node)) {
          if (node.tagName.getText(file) === "button") {
            const attributes = node.attributes.properties;
            const className = attributes.find(attribute => ts.isJsxAttribute(attribute) && attribute.name.getText(file) === "className");
            if (className?.getText(file).includes("icon-button")) {
              const named = attributes.some(attribute => ts.isJsxAttribute(attribute) && ["aria-label", "title"].includes(attribute.name.getText(file)));
              if (!named) missing.push(`${path}:${file.getLineAndCharacterOfPosition(node.getStart(file)).line + 1}`);
            }
          }
        }
        ts.forEachChild(node, visit);
      };
      visit(file);
    }
    expect(missing).toEqual([]);
  });

  test("announces connection and operation feedback without interrupting the reader", () => {
    const app = readFileSync("src/App.tsx", "utf8");
    expect(app).toMatch(/className=\{`connection[\s\S]*?role="status" aria-live="polite"/);
    expect(app).toMatch(/className="toast" role="status" aria-live="polite"/);
  });
});
