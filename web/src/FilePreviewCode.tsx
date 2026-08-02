import { Highlight, themes } from "prism-react-renderer";
import { VirtualizedList } from "./components/VirtualizedList";

export default function FilePreviewCode({ content, language, targetLine }: { content: string; language: string; targetLine?: number }) {
  return <Highlight theme={themes.github} code={content} language={language}>{({ className, style, tokens, getLineProps, getTokenProps }) => <VirtualizedList
    className={`file-code ${className}`}
    style={{ ...style, height: "100%" }}
    items={tokens}
    getKey={(_line, index) => String(index)}
    estimateSize={18}
    scrollToIndex={targetLine ? targetLine - 1 : undefined}
    scrollToAlign="center"
    renderItem={(line, index) => {
      const number = index + 1;
      return <span {...getLineProps({ line })} className={`file-code-line ${targetLine === number ? "target" : ""}`} data-line={number}><span className="file-code-number">{number}</span><span className="file-code-content">{line.map((token, tokenIndex) => <span {...getTokenProps({ token })} key={tokenIndex} />)}</span></span>;
    }}
  />}</Highlight>;
}
