import { useEffect, useMemo, useRef } from "react";
import type { Member, Step, StepRun } from "../data/tasks";
import { useI18n } from "../i18n";

export function graphLayers(steps: Step[]) {
  const known = new Set(steps.map(step => step.id));
  const depths = new Map<string, number>();
  const layers: Step[][] = [];
  const invalid = new Set<string>();
  for (const step of steps) {
    if (step.depends_on?.some(id => !known.has(id))) invalid.add(step.id);
  }
  let pending = steps.filter(step => !invalid.has(step.id));
  while (pending.length) {
    const next = pending.filter(step => (step.depends_on ?? []).every(id => depths.has(id)));
    if (!next.length) break;
    for (const step of next) depths.set(step.id, layers.length);
    layers.push(next);
    pending = pending.filter(step => !depths.has(step.id));
  }
  for (const step of pending) invalid.add(step.id);
  return { layers, invalid };
}

export function TaskGraph({ steps, members, projection = [], onSelect }: {
  steps: Step[]; members: Member[]; projection?: StepRun[]; onSelect?: (id: string) => void;
}) {
  const { t } = useI18n();
  const { layers, invalid } = useMemo(() => graphLayers(steps), [steps]);
  const display = invalid.size ? [...layers, steps.filter(step => invalid.has(step.id))] : layers;
  const container = useRef<HTMLDivElement>(null);
  const canvas = useRef<HTMLCanvasElement>(null);
  const nodes = useRef(new Map<string, HTMLElement>());
  useEffect(() => {
    const element = container.current;
    const surface = canvas.current;
    if (!element || !surface || !("ResizeObserver" in window)) return;
    const draw = () => {
      const bounds = element.getBoundingClientRect();
      const width = element.clientWidth;
      const height = element.clientHeight;
      const ratio = window.devicePixelRatio || 1;
      if (!width || !height) return;
      surface.width = width * ratio;
      surface.height = height * ratio;
      const ctx = surface.getContext("2d");
      if (!ctx) return;
      ctx.scale(ratio, ratio);
      ctx.strokeStyle = getComputedStyle(element).getPropertyValue("--muted");
      ctx.lineWidth = 1;
      for (const step of steps) {
        const target = nodes.current.get(step.id)?.getBoundingClientRect();
        if (!target) continue;
        for (const id of step.depends_on ?? []) {
          const source = nodes.current.get(id)?.getBoundingClientRect();
          if (!source) continue;
          const startX = source.right - bounds.left;
          const endX = target.left - bounds.left;
          const startY = source.top + source.height / 2 - bounds.top;
          const endY = target.top + target.height / 2 - bounds.top;
          const mid = (startX + endX) / 2;
          ctx.setLineDash(invalid.has(step.id) && invalid.has(id) ? [4, 3] : []);
          ctx.beginPath();
          ctx.moveTo(startX, startY);
          ctx.bezierCurveTo(mid, startY, mid, endY, endX, endY);
          ctx.lineTo(endX - 5, endY - 3);
          ctx.moveTo(endX, endY);
          ctx.lineTo(endX - 5, endY + 3);
          ctx.stroke();
        }
      }
    };
    const observer = new ResizeObserver(draw);
    observer.observe(element);
    for (const node of nodes.current.values()) observer.observe(node);
    const theme = new MutationObserver(draw);
    theme.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
    draw();
    return () => { observer.disconnect(); theme.disconnect(); };
  }, [steps, invalid]);
  return <div className="task-graph-scroll" role="group" aria-label={t("tasks.graph")}>
    <div className="task-graph" ref={container}>
      <canvas ref={canvas} aria-hidden="true" />
      {display.map((layer, index) => <div className="task-graph-layer" key={index}>
        {layer.map(step => {
          const execution = projection.find(item => item.step_id === step.id);
          const state = execution?.state;
          const engine = members.find(member => member.id === step.member_id)?.engine;
          const dependencies = (step.depends_on ?? []).map(id => steps.find(item => item.id === id)?.title || id);
          const title = step.title || t("tasks.stepLabel", {count: steps.indexOf(step) + 1});
          const content = <>
            <strong>{title}</strong>
            {(engine || state) && <small>{[engine, state ? t(`tasks.state.${state}`) : ""].filter(Boolean).join(" · ")}</small>}
            {step.completion_policy !== "execution" && <small>{t(`tasks.policy.${step.completion_policy}`)}</small>}
            {!!execution?.reworks && <small>{t("tasks.graph.reworks", { count: execution.reworks })}</small>}
            {dependencies.length > 0 && <small>{t("tasks.dependencies")} {dependencies.join(", ")}</small>}
            {invalid.has(step.id) && <span className="form-error">{t("tasks.graph.invalid")}</span>}
          </>;
          const ref = (node: HTMLElement | null) => { if (node) nodes.current.set(step.id, node); else nodes.current.delete(step.id); };
          return onSelect
            ? <button ref={ref} key={step.id} type="button" className="task-graph-node" onClick={() => onSelect(step.id)}>{content}</button>
            : <div ref={ref} key={step.id} className="task-graph-node">{content}</div>;
        })}
      </div>)}
    </div>
  </div>;
}
