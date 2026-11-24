import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type CSSProperties, type MutableRefObject, type ReactNode, type RefObject } from "react";

export type VirtualizedWindow = {
  start: number;
  end: number;
  totalSize: number;
  offsets: number[];
  onMeasure: (index: number, element: HTMLElement | null) => void;
  listRef: RefObject<HTMLDivElement>;
};

type VirtualizedWindowOptions<T> = {
  items: T[];
  scrollRef: RefObject<HTMLElement | null>;
  getKey?: (item: T, index: number) => string;
  estimateSize?: number | ((item: T, index: number) => number);
  overscan?: number;
};

type VirtualizedScrollOptions = {
  scrollToIndex?: number;
  scrollToAlign?: "start" | "center" | "end";
};

export function captureVirtualizedReadingPosition(stream: HTMLElement) {
  const viewport = stream.getBoundingClientRect();
  return Array.from(stream.querySelectorAll<HTMLElement>("[data-virtual-key]")).flatMap(element => {
    const rect = element.getBoundingClientRect();
    return rect.bottom <= viewport.top || rect.top >= viewport.bottom
      ? []
      : [{ key: element.dataset.virtualKey!, top: rect.top - viewport.top }];
  });
}

type ReadingPositionRef = MutableRefObject<ReturnType<typeof captureVirtualizedReadingPosition> | undefined>;

function clampSize(value: number, fallback: number) {
  return Number.isFinite(value) && value > 0 ? Math.max(20, value) : fallback;
}

function findStart(offsets: number[], scrollTop: number) {
  let low = 0;
  let high = Math.max(0, offsets.length - 1);
  while (low < high) {
    const middle = Math.floor((low + high) / 2);
    if (offsets[middle + 1] <= scrollTop) low = middle + 1;
    else high = middle;
  }
  return low;
}

export function useVirtualizedWindow<T>({ items, scrollRef, getKey = (_item, index) => String(index), estimateSize = 120, overscan = 6 }: VirtualizedWindowOptions<T>, followEnd?: RefObject<boolean>, restoreReadingPosition?: ReadingPositionRef): VirtualizedWindow {
  const [scrollState, setScrollState] = useState({ top: 0, height: 0, listTop: 0 });
  const listRef = useRef<HTMLDivElement>(null);
  const previousKeys = useRef<string[]>();
  const measuredRef = useRef(new Map<string, number>());
  const [measureVersion, redraw] = useState(0);
  const keyList = useMemo(() => items.map(getKey), [getKey, items]);
  const estimates = useMemo(() => items.map((item, index) => clampSize(typeof estimateSize === "function" ? estimateSize(item, index) : estimateSize, 120)), [estimateSize, items]);
  const offsets = useMemo(() => {
    const output = new Array<number>(items.length + 1).fill(0);
    for (let index = 0; index < items.length; index++) {
      output[index + 1] = output[index] + (measuredRef.current.get(keyList[index]) ?? estimates[index]);
    }
    return output;
  }, [estimates, items.length, keyList, measureVersion]);

  const pendingPosition = restoreReadingPosition?.current;
  const restoring = pendingPosition !== undefined && previousKeys.current !== keyList;
  const anchor = restoring ? pendingPosition.find(item => keyList.includes(item.key)) : undefined;
  const anchorIndex = anchor ? keyList.indexOf(anchor.key) : 0;
  useLayoutEffect(() => {
    const stream = scrollRef.current;
    const list = listRef.current;
    if (!followEnd || !stream || !list) return;
    const hadRows = Boolean(previousKeys.current?.length);
    previousKeys.current = keyList;

    const scrollTop = stream.scrollTop;
    if (restoring && restoreReadingPosition) {
      restoreReadingPosition.current = undefined;
      if (anchor) {
        const row = list.querySelector(`[data-virtual-index="${anchorIndex}"]`);
        if (row) {
          stream.scrollTop += row.getBoundingClientRect().top - stream.getBoundingClientRect().top - anchor.top;
        }
      }
    } else if (hadRows && followEnd.current) {
      const end = Math.max(0, stream.scrollHeight - stream.clientHeight);
      if (Math.abs(scrollTop - end) > 1) stream.scrollTop = end;
    }
    if (restoring || stream.scrollTop !== scrollTop || list.offsetTop !== scrollState.listTop) {
      setScrollState({ top: stream.scrollTop, height: stream.clientHeight, listTop: list.offsetTop });
    }
  }, [anchor, anchorIndex, followEnd, keyList, offsets, restoreReadingPosition, restoring, scrollRef, scrollState.listTop]);

  useEffect(() => {
    const element = scrollRef.current;
    if (!element) return;
    let frame = 0;
    const update = () => {
      frame = 0;
      const top = element.scrollTop;
      const height = element.clientHeight;
      const listTop = followEnd ? listRef.current?.offsetTop ?? 0 : 0;
      setScrollState(previous => previous.top === top && previous.height === height && previous.listTop === listTop
        ? previous : { top, height, listTop });
    };
    const onScroll = () => {
      if (!frame) frame = window.requestAnimationFrame(update);
    };
    element.addEventListener("scroll", onScroll, { passive: true });
    const resize = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(update);
    resize?.observe(element);
    update();
    return () => {
      element.removeEventListener("scroll", onScroll);
      resize?.disconnect();
      if (frame) window.cancelAnimationFrame(frame);
    };
  }, [followEnd, scrollRef]);

  const onMeasure = useCallback((index: number, element: HTMLElement | null) => {
    if (!element) return;
    const key = keyList[index];
    if (!key) return;
    const height = element.getBoundingClientRect().height;
    const measured = followEnd ? height : Math.ceil(height);
    const previous = measuredRef.current.get(key);
    if (measured <= 0 || previous === measured) return;
    measuredRef.current.set(key, measured);
    redraw(value => value + 1);
  }, [followEnd, keyList]);

  const viewport = Math.max(0, scrollState.height || 560);
  const top = Math.max(0, anchor ? offsets[anchorIndex] - anchor.top : scrollState.top - scrollState.listTop);
  const first = items.length ? findStart(offsets, top) : 0;
  const start = Math.max(0, first - overscan);
  const end = Math.min(items.length, findStart(offsets, top + viewport) + overscan + 1);

  useLayoutEffect(() => {
    const list = listRef.current;
    if (!list) return;
    const resize = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(entries => {
      let changed = false;
      for (const entry of entries) {
        const node = entry.target as HTMLElement;
        const index = Number(node.dataset.virtualIndex);
        const key = keyList[index];
        const size = followEnd ? entry.contentRect.height : Math.ceil(entry.contentRect.height);
        if (!key || !Number.isFinite(size) || size <= 0 || measuredRef.current.get(key) === size) continue;
        measuredRef.current.set(key, size);
        changed = true;
      }
      if (changed) redraw(value => value + 1);
    });
    if (resize) list.querySelectorAll<HTMLElement>("[data-virtual-index]").forEach(node => resize.observe(node));
    return () => resize?.disconnect();
  }, [end, followEnd, keyList, start]);
  return { start, end, totalSize: offsets.at(-1) ?? 0, offsets, onMeasure, listRef };
}

export function VirtualizedItems<T>({ items, scrollRef, getKey, estimateSize, overscan = 6, renderItem, className = "", style, scrollToIndex, scrollToAlign = "center", followEnd, restoreReadingPosition }: VirtualizedItemsProps<T> & VirtualizedScrollOptions & { followEnd?: RefObject<boolean>; restoreReadingPosition?: ReadingPositionRef }) {
  const virtual = useVirtualizedWindow({ items, scrollRef, getKey, estimateSize, overscan }, followEnd, restoreReadingPosition);
  const lastScrolledIndex = useRef<number | null>(null);
  useEffect(() => {
    if (scrollToIndex == null || !items.length || lastScrolledIndex.current === scrollToIndex) return;
    const element = scrollRef.current;
    if (!element) return;
    const index = Math.max(0, Math.min(items.length - 1, Math.floor(scrollToIndex)));
    const itemSize = virtual.offsets[index + 1] - virtual.offsets[index];
    const viewport = element.clientHeight;
    const alignOffset = scrollToAlign === "start" ? 0 : scrollToAlign === "end" ? Math.max(0, viewport - itemSize) : Math.max(0, (viewport - itemSize) / 2);
    element.scrollTop = Math.max(0, virtual.offsets[index] - alignOffset);
    lastScrolledIndex.current = scrollToIndex;
    element.dispatchEvent(new Event("scroll"));
  }, [items.length, scrollRef, scrollToAlign, scrollToIndex, virtual.offsets]);
  const rows = items.slice(virtual.start, virtual.end).map((item, relativeIndex) => {
    const index = virtual.start + relativeIndex;
    const key = getKey ? getKey(item, index) : String(index);
    return <div key={key} className="virtualized-item" data-virtual-index={index} data-virtual-key={key} ref={element => virtual.onMeasure(index, element)} style={followEnd ? undefined : { position: "absolute", top: virtual.offsets[index], left: 0, right: 0 }}>
      {renderItem(item, index)}
    </div>;
  });
  // Normal-flow conversation rows let the browser anchor changes to their heights.
  return <div ref={virtual.listRef} className={className} style={followEnd ? style : { ...style, minHeight: virtual.totalSize }} data-virtualized-list data-item-count={items.length}>
    {followEnd ? <>
      <div className="virtualized-space" style={{ height: virtual.offsets[virtual.start] }} />
      {rows}
      <div className="virtualized-space" style={{ height: virtual.totalSize - virtual.offsets[virtual.end] }} />
    </> : <div style={{ height: virtual.totalSize, position: "relative" }}>{rows}</div>}
  </div>;
}

export function VirtualizedList<T>({ items, getKey, estimateSize, overscan, renderItem, className = "", style, scrollToIndex, scrollToAlign }: Omit<VirtualizedItemsProps<T>, "scrollRef"> & VirtualizedScrollOptions) {
  const scrollRef = useRef<HTMLDivElement>(null);
  return <div ref={scrollRef} className={`virtualized-scroll ${className}`} style={{ overflowY: "auto", ...style }}>
    <VirtualizedItems items={items} scrollRef={scrollRef} getKey={getKey} estimateSize={estimateSize} overscan={overscan} renderItem={renderItem} scrollToIndex={scrollToIndex} scrollToAlign={scrollToAlign} />
  </div>;
}

type VirtualizedItemsProps<T> = VirtualizedWindowOptions<T> & { renderItem: (item: T, index: number) => ReactNode; className?: string; style?: CSSProperties };
