import { ComponentType, KeyboardEvent, MutableRefObject, useEffect, useMemo, useRef, useState } from "react";
import { ArrowLeft, Check, LucideProps } from "lucide-react";

export type SlashCommandItem = {
  id: string;
  name: string;
  description: string;
  icon: ComponentType<LucideProps>;
  detail?: string;
  selected?: boolean;
  onSelect: () => void;
};

export function SlashCommandMenu({ items, query, label, backLabel, onBack, onDismiss, keyboardRef }: {
  items: SlashCommandItem[];
  query: string;
  label: string;
  backLabel?: string;
  onBack?: () => void;
  onDismiss: () => void;
  keyboardRef: MutableRefObject<((event: KeyboardEvent<HTMLTextAreaElement>) => boolean) | null>;
}) {
  const [activeIndex, setActiveIndex] = useState(0);
  const itemRefs = useRef<Array<HTMLButtonElement | null>>([]);
  const normalizedQuery = query.trim().toLocaleLowerCase();
  const itemSignature = items.map(item => item.id).join("\u0000");
  const filtered = useMemo(() => items.filter(item => !normalizedQuery || `${item.name} ${item.description}`.toLocaleLowerCase().includes(normalizedQuery)), [items, normalizedQuery]);

  useEffect(() => setActiveIndex(0), [normalizedQuery, itemSignature]);
  useEffect(() => { itemRefs.current[activeIndex]?.scrollIntoView({ block: "nearest" }); }, [activeIndex]);
  useEffect(() => {
    keyboardRef.current = event => {
      if (event.nativeEvent.isComposing) return false;
      if (event.key === "Escape") { event.preventDefault(); onDismiss(); return true; }
      if (event.key === "ArrowDown") { event.preventDefault(); setActiveIndex(index => filtered.length ? (index + 1) % filtered.length : 0); return true; }
      if (event.key === "ArrowUp") { event.preventDefault(); setActiveIndex(index => filtered.length ? (index - 1 + filtered.length) % filtered.length : 0); return true; }
      if (event.key === "Home") { event.preventDefault(); setActiveIndex(0); return true; }
      if (event.key === "End") { event.preventDefault(); setActiveIndex(Math.max(0, filtered.length - 1)); return true; }
      if (event.key === "Enter") { event.preventDefault(); filtered[activeIndex]?.onSelect(); return true; }
      if (event.key === "Backspace" && onBack && !query) { event.preventDefault(); onBack(); return true; }
      return false;
    };
    return () => { keyboardRef.current = null; };
  }, [activeIndex, filtered, keyboardRef, onBack, onDismiss, query]);

  return <div className="slash-command-menu" role="listbox" aria-label={label}>
    {onBack && <button type="button" className="slash-command-back" onMouseDown={event => event.preventDefault()} onClick={onBack}><ArrowLeft size={15} />{backLabel}</button>}
    <div className="slash-command-items">
      {filtered.map((item, index) => { const Icon = item.icon; return <button type="button" role="option" aria-selected={index === activeIndex} className={index === activeIndex ? "active" : ""} key={item.id} ref={node => { itemRefs.current[index] = node; }} onMouseEnter={() => setActiveIndex(index)} onMouseDown={event => event.preventDefault()} onClick={item.onSelect}><Icon size={18} /><span><strong>{item.name}</strong><small>{item.description}</small></span>{item.detail && <em>{item.detail}</em>}{item.selected && <Check size={16} />}</button>; })}
    </div>
  </div>;
}
