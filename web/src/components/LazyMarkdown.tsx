import { lazy, memo, Suspense } from "react";
import { type FilePreviewSelection } from "../codexContent";

export const MarkdownContentRenderer = lazy(() => import("../MarkdownContent"));

export const MarkdownContent = memo(function MarkdownContent({ text, workspaceRoot, onOpenFile }: { text: string; workspaceRoot: string; onOpenFile: (selection: FilePreviewSelection) => void }) {
  return <Suspense fallback={<div className="message-content">{text}</div>}><MarkdownContentRenderer text={text} workspaceRoot={workspaceRoot} onOpenFile={onOpenFile} /></Suspense>;
});
