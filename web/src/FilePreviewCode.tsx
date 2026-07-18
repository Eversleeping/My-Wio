import { useEffect, useRef } from "react";
import { Highlight, themes } from "prism-react-renderer";

export default function FilePreviewCode({ content, language, targetLine }: { content: string; language: string; targetLine?: number }) {
  const previewRef = useRef<HTMLPreElement>(null);
  useEffect(() => {
    if (!targetLine) return;
    const frame = requestAnimationFrame(() => previewRef.current?.querySelector<HTMLElement>(`[data-line="${targetLine}"]`)?.scrollIntoView({ block: "center" }));
    return () => cancelAnimationFrame(frame);
  }, [content, targetLine]);
  return <Highlight theme={themes.github} code={content} language={language}>{({ className, style, tokens, getLineProps, getTokenProps }) => <pre ref={previewRef} className={`file-code ${className}`} style={style}><code>{tokens.map((line, index) => { const number = index + 1; return <span {...getLineProps({ line })} className={`file-code-line ${targetLine === number ? "target" : ""}`} data-line={number} key={number}><span className="file-code-number">{number}</span><span className="file-code-content">{line.map((token, tokenIndex) => <span {...getTokenProps({ token })} key={tokenIndex} />)}</span></span>; })}</code></pre>}</Highlight>;
}
