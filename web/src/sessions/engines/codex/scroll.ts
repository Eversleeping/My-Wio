
export const codexScrollPositions = new Map<string, number>();

const codexAutoFollowThreshold = 96;

export function setScrollTopImmediately(element: HTMLElement, top: number) {
  const scrollBehavior = element.style.scrollBehavior;
  element.style.scrollBehavior = "auto";
  element.scrollTop = top;
  element.style.scrollBehavior = scrollBehavior;
}

export function latestScrollTop(element: HTMLElement) {
  return Math.max(0, element.scrollHeight - element.clientHeight);
}

export function isNearCodexStreamBottom(element: HTMLElement) {
  return latestScrollTop(element) - element.scrollTop <= codexAutoFollowThreshold;
}
