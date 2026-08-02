import { useRef, type RefObject } from "react";
import { Highlight, themes } from "prism-react-renderer";
import { VirtualizedItems } from "./components/VirtualizedList";

type DiffRow = { kind: "context" | "add" | "delete"; oldLine?: number; newLine?: number; text: string };
type DiffSection = { collapsed: number; rows: DiffRow[] };
type DiffRenderItem = { kind: "collapsed"; count: number; key: string } | { kind: "row"; row: DiffRow; tokenIndex: number; key: string };

export default function FileDiffCode({ content, language, unchangedLabel, scrollRef }: { content: string; language: string; unchangedLabel: (count: number) => string; scrollRef?: RefObject<HTMLElement | null> }) {
  const internalRef = useRef<HTMLDivElement>(null);
  const viewportRef = scrollRef ?? internalRef;
  const sections = parseUnifiedDiff(content);
  const renderItems: DiffRenderItem[] = [];
  const rows: DiffRow[] = [];
  sections.forEach((section, sectionIndex) => {
    if (section.collapsed > 0) renderItems.push({ kind: "collapsed", count: section.collapsed, key: `collapsed:${sectionIndex}` });
    section.rows.forEach((row, rowIndex) => {
      const tokenIndex = rows.length;
      rows.push(row);
      renderItems.push({ kind: "row", row, tokenIndex, key: `${sectionIndex}:${row.oldLine ?? ""}:${row.newLine ?? ""}:${rowIndex}` });
    });
  });
  return <div ref={internalRef} className="file-diff" role="table" style={scrollRef ? undefined : { overflowY: "auto", height: "100%" }}>
    <Highlight theme={themes.github} code={rows.map(row => row.text).join("\n")} language={language}>{({ tokens, getLineProps, getTokenProps }) => <VirtualizedItems
      items={renderItems}
      scrollRef={viewportRef}
      getKey={item => item.key}
      estimateSize={item => item.kind === "collapsed" ? 34 : 18}
      className="file-diff-lines"
      renderItem={item => item.kind === "collapsed" ? <div className="file-diff-collapsed">{unchangedLabel(item.count)}</div> : <div {...getLineProps({ line: tokens[item.tokenIndex] ?? [] })} className={`file-diff-line ${item.row.kind}`} role="row"><span className="file-diff-marker">{item.row.kind === "add" ? "+" : item.row.kind === "delete" ? "-" : ""}</span><span className="file-diff-number">{item.row.oldLine ?? ""}</span><span className="file-diff-number">{item.row.newLine ?? ""}</span><span className="file-diff-content">{(tokens[item.tokenIndex] ?? []).map((token, tokenIndex) => <span {...getTokenProps({ token })} key={tokenIndex} />)}</span></div>}
    />}</Highlight>
  </div>;
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
    const sectionRows: DiffRow[] = [];
    index++;
    while (index < lines.length && !lines[index].startsWith("@@ ")) {
      const line = lines[index];
      if (line.startsWith("diff --git ")) break;
      if (line.startsWith("+")) {
        sectionRows.push({ kind: "add", newLine, text: line.slice(1) });
        newLine++;
      } else if (line.startsWith("-")) {
        sectionRows.push({ kind: "delete", oldLine, text: line.slice(1) });
        oldLine++;
      } else if (line.startsWith(" ")) {
        sectionRows.push({ kind: "context", oldLine, newLine, text: line.slice(1) });
        oldLine++;
        newLine++;
      }
      index++;
    }
    previousOldEnd = oldLine - 1;
    sections.push({ collapsed, rows: sectionRows });
  }
  return sections;
}
