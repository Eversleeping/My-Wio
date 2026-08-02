import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type CSSProperties, type ReactNode, type RefObject } from "react";

export type VirtualizedWindow = {
  start: number;
  end: number;
  totalSize: number;
  offsets: number[];
  onMeasure: (index: number, element: HTMLElement | null) => void;
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

export function useVirtualizedWindow<T>({ items, scrollRef, getKey = (_item, index) => String(index), estimateSize = 120, overscan = 6 }: VirtualizedWindowOptions<T>): VirtualizedWindow {
  const [scrollState, setScrollState] = useState({ top: 0, height: 0 });
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

  useEffect(() => {
    const element = scrollRef.current;
    if (!element) return;
    let frame = 0;
    const update = () => {
      frame = 0;
      setScrollState({ top: element.scrollTop, height: element.clientHeight });
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
  }, [scrollRef]);

  const onMeasure = useCallback((index: number, element: HTMLElement | null) => {
    if (!element) return;
    const key = keyList[index];
    if (!key) return;
    const measured = Math.ceil(element.getBoundingClientRect().height);
    const previous = measuredRef.current.get(key);
    if (measured <= 0 || previous === measured) return;
    measuredRef.current.set(key, measured);
    redraw(value => value + 1);
  }, [keyList]);

  useLayoutEffect(() => {
    const element = scrollRef.current;
    if (!element) return;
    const resize = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(entries => {
      let changed = false;
      for (const entry of entries) {
        const node = entry.target as HTMLElement;
        const index = Number(node.dataset.virtualIndex);
        const key = keyList[index];
        const size = Math.ceil(entry.contentRect.height);
        if (!key || !Number.isFinite(size) || size <= 0 || measuredRef.current.get(key) === size) continue;
        measuredRef.current.set(key, size);
        changed = true;
      }
      if (changed) redraw(value => value + 1);
    });
    if (resize) element.querySelectorAll<HTMLElement>("[data-virtual-index]").forEach(node => resize.observe(node));
    return () => resize?.disconnect();
  }, [keyList, scrollRef, scrollState.top]);

  const viewport = Math.max(0, scrollState.height || 560);
  const first = items.length ? findStart(offsets, Math.max(0, scrollState.top)) : 0;
  const start = Math.max(0, first - overscan);
  const end = Math.min(items.length, findStart(offsets, Math.max(0, scrollState.top) + viewport) + overscan + 1);
  return { start, end, totalSize: offsets.at(-1) ?? 0, offsets, onMeasure };
}

export function VirtualizedItems<T>({ items, scrollRef, getKey, estimateSize, overscan = 6, renderItem, className = "", style, scrollToIndex, scrollToAlign = "center" }: VirtualizedWindowOptions<T> & VirtualizedScrollOptions & { renderItem: (item: T, index: number) => ReactNode; className?: string; style?: CSSProperties }) {
  const virtual = useVirtualizedWindow({ items, scrollRef, getKey, estimateSize, overscan });
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
  return <div className={className} style={{ ...style, minHeight: virtual.totalSize }} data-virtualized-list data-item-count={items.length}>
    <div style={{ height: virtual.totalSize, position: "relative" }}>
      {items.slice(virtual.start, virtual.end).map((item, relativeIndex) => {
        const index = virtual.start + relativeIndex;
        const key = getKey ? getKey(item, index) : String(index);
        return <div key={key} className="virtualized-item" data-virtual-index={index} data-virtual-key={key} ref={element => virtual.onMeasure(index, element)} style={{ position: "absolute", top: virtual.offsets[index], left: 0, right: 0 }}>
          {renderItem(item, index)}
        </div>;
      })}
    </div>
  </div>;
}

export function VirtualizedList<T>({ items, getKey, estimateSize, overscan, renderItem, className = "", style, scrollToIndex, scrollToAlign }: Omit<VirtualizedItemsProps<T>, "scrollRef"> & VirtualizedScrollOptions) {
  const scrollRef = useRef<HTMLDivElement>(null);
  return <div ref={scrollRef} className={`virtualized-scroll ${className}`} style={{ overflowY: "auto", ...style }}>
    <VirtualizedItems items={items} scrollRef={scrollRef} getKey={getKey} estimateSize={estimateSize} overscan={overscan} renderItem={renderItem} scrollToIndex={scrollToIndex} scrollToAlign={scrollToAlign} />
  </div>;
}

type VirtualizedItemsProps<T> = VirtualizedWindowOptions<T> & { renderItem: (item: T, index: number) => ReactNode; className?: string; style?: CSSProperties };
