import { fireEvent, render, waitFor } from "@testing-library/react";
import { useRef } from "react";
import { expect, test } from "vitest";
import { VirtualizedItems, VirtualizedList } from "./components/VirtualizedList";

test("only mounts a bounded window for long lists", () => {
  const items = Array.from({ length: 1_000 }, (_, index) => index);
  const { container } = render(<VirtualizedList items={items} style={{ height: 240 }} estimateSize={24} renderItem={item => <div>{item}</div>} />);
  const list = container.querySelector<HTMLElement>(".virtualized-scroll");
  expect(list).toBeTruthy();
  expect(list?.querySelector("[data-item-count='1000']")).toBeTruthy();
  expect(list?.querySelectorAll("[data-virtual-index]").length).toBeLessThan(100);
});

test("marks each virtualized row for conversation-specific layout", () => {
  const { container } = render(<VirtualizedList items={["message"]} style={{ height: 120 }} renderItem={item => <article>{item}</article>} />);
  expect(container.querySelector("[data-virtual-index='0']")).toHaveClass("virtualized-item");
});

test("updates the rendered window when the list scrolls", async () => {
  const items = Array.from({ length: 500 }, (_, index) => index);
  const { container } = render(<VirtualizedList items={items} style={{ height: 200 }} estimateSize={20} renderItem={item => <div>{item}</div>} />);
  const list = container.querySelector<HTMLElement>(".virtualized-scroll");
  if (!list) throw new Error("virtualized list missing");
  const before = list.querySelector("[data-virtual-index='0']");
  Object.defineProperty(list, "clientHeight", { configurable: true, value: 200 });
  Object.defineProperty(list, "scrollHeight", { configurable: true, value: 10_000 });
  list.scrollTop = 4_000;
  fireEvent.scroll(list);
  await waitFor(() => expect(list.querySelector("[data-virtual-index='0']")).not.toBe(before));
});

test("scrolls to a requested target row even when it is outside the mounted window", async () => {
  const items = Array.from({ length: 200 }, (_, index) => index);
  function TargetHarness() {
    const scrollRef = useRef<HTMLDivElement | null>(null);
    return <div ref={node => { if (node) Object.defineProperty(node, "clientHeight", { configurable: true, value: 200 }); scrollRef.current = node; }} style={{ overflowY: "auto", height: 200 }}><VirtualizedItems items={items} scrollRef={scrollRef} estimateSize={20} scrollToIndex={150} renderItem={item => <div>{item}</div>} /></div>;
  }
  const { container } = render(<TargetHarness />);
  const list = container.firstElementChild as HTMLElement;
  await waitFor(() => expect(list.scrollTop).toBe(2_910));
  await waitFor(() => expect(list.querySelector("[data-virtual-index='150']")).toBeInTheDocument());
});
