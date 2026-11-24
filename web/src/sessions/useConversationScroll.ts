import { useLayoutEffect, useRef, type UIEvent } from "react";

export function conversationEnd(element: HTMLElement) {
  return Math.max(0, element.scrollHeight - element.clientHeight);
}

export function isAtConversationEnd(element: HTMLElement) {
  return conversationEnd(element) - element.scrollTop <= 1;
}

export function useConversationScroll(items: readonly unknown[], running: boolean) {
  const stream = useRef<HTMLDivElement>(null);
  const follow = useRef(true);
  const historyAnchor = useRef<{ element: Element; top: number }>();

  useLayoutEffect(() => {
    const element = stream.current;
    if (!element) return;
    const anchor = historyAnchor.current;
    historyAnchor.current = undefined;
    if (anchor && element.contains(anchor.element)) {
      element.scrollTop += anchor.element.getBoundingClientRect().top - anchor.top;
    } else if (follow.current) {
      element.scrollTop = conversationEnd(element);
    }
  }, [items, running]);

  function preserveHistoryPosition() {
    const element = stream.current;
    if (!element) return;
    const viewportTop = element.getBoundingClientRect().top;
    const visible = Array.from(element.children).find(child =>
      !child.hasAttribute("data-history-loader") && child.getBoundingClientRect().bottom > viewportTop
    );
    if (visible) historyAnchor.current = { element: visible, top: visible.getBoundingClientRect().top };
    follow.current = false;
  }

  return {
    stream,
    preserveHistoryPosition,
    resumeFollowing: () => { follow.current = true; },
    onScroll: (event: UIEvent<HTMLDivElement>) => { follow.current = isAtConversationEnd(event.currentTarget); }
  };
}
