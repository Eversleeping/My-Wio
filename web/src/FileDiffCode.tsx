import { Highlight, themes } from "prism-react-renderer";

type DiffRow = { kind: "context" | "add" | "delete"; oldLine?: number; newLine?: number; text: string };
type DiffSection = { collapsed: number; rows: DiffRow[] };

export default function FileDiffCode({ content, language, unchangedLabel }: { content: string; language: string; unchangedLabel: (count: number) => string }) {
  const sections = parseUnifiedDiff(content);
  return <div className="file-diff" role="table">{sections.map((section, sectionIndex) => <div className="file-diff-section" key={sectionIndex}>{section.collapsed > 0 && <div className="file-diff-collapsed">{unchangedLabel(section.collapsed)}</div>}<Highlight theme={themes.github} code={section.rows.map(row => row.text).join("\n")} language={language}>{({ tokens, getLineProps, getTokenProps }) => <div className="file-diff-lines">{section.rows.map((row, rowIndex) => <div {...getLineProps({ line: tokens[rowIndex] ?? [] })} className={`file-diff-line ${row.kind}`} role="row" key={`${row.oldLine ?? ""}:${row.newLine ?? ""}:${rowIndex}`}><span className="file-diff-marker">{row.kind === "add" ? "+" : row.kind === "delete" ? "-" : ""}</span><span className="file-diff-number">{row.oldLine ?? ""}</span><span className="file-diff-number">{row.newLine ?? ""}</span><span className="file-diff-content">{(tokens[rowIndex] ?? []).map((token, tokenIndex) => <span {...getTokenProps({ token })} key={tokenIndex} />)}</span></div>)}</div>}</Highlight></div>)}</div>;
}

function parseUnifiedDiff(content: string): DiffSection[] {
  const lines = content.split("\n");
  const sections: DiffSection[] = [];
  let previousOldEnd = 0;
  let index = 0;
  while (index < lines.length) {
    const match = lines[index].match(/^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/);
    if (!match) {
      index++;
      continue;
    }
    let oldLine = Number(match[1]);
    let newLine = Number(match[3]);
    const collapsed = Math.max(0, oldLine - previousOldEnd - 1);
    const rows: DiffRow[] = [];
    index++;
    while (index < lines.length && !lines[index].startsWith("@@ ")) {
      const line = lines[index];
      if (line.startsWith("diff --git ")) break;
      if (line.startsWith("+")) {
        rows.push({ kind: "add", newLine, text: line.slice(1) });
        newLine++;
      } else if (line.startsWith("-")) {
        rows.push({ kind: "delete", oldLine, text: line.slice(1) });
        oldLine++;
      } else if (line.startsWith(" ")) {
        rows.push({ kind: "context", oldLine, newLine, text: line.slice(1) });
        oldLine++;
        newLine++;
      }
      index++;
    }
    previousOldEnd = oldLine - 1;
    sections.push({ collapsed, rows });
  }
  return sections;
}
