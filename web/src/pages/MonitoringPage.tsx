import { type PointerEvent as ReactPointerEvent, type ReactNode, useEffect, useMemo, useState } from "react";
import {
  AlertTriangle,
  ArrowDownToLine,
  ArrowUpFromLine,
  BellRing,
  Check,
  Cpu,
  Gauge,
  HardDrive,
  MapPin,
  MemoryStick,
  MonitorDot,
  Settings as SettingsIcon,
  StickyNote
} from "lucide-react";
import { post } from "../api";
import { DataTable, Section, Status } from "../components/PageUI";
import { formatDate, relative } from "../format";
import { currentLocale, useI18n } from "../i18n";
import type { Alert, Metric, OperationMetrics, Server } from "../types";
import { useData } from "../useData";

type ChartPoint = { time: string; values: number[] };
type ChartSeries = { label: string; color: string; format: (value?: number) => string };
type NetworkRate = { time: string; rx: number; tx: number };

export function MonitoringPage({ realtime }: { realtime: number }) {
  const { t } = useI18n();
  const servers = useData<Server[]>("/servers", realtime);
  const alerts = useData<Alert[]>("/alerts", realtime);
  const [serverID, setServerID] = useState("");
  const [rangeHours, setRangeHours] = useState(24);
  useEffect(() => { if (!serverID && servers.data?.[0]) setServerID(servers.data[0].id); }, [servers.data, serverID]);
  const metrics = useData<Metric[]>(serverID ? `/servers/${serverID}/metrics?hours=${rangeHours}` : null, `${realtime}:${serverID}:${rangeHours}`);
  const operationMetrics = useData<OperationMetrics>(serverID ? `/servers/${serverID}/operation-metrics?hours=${rangeHours}` : null, `${realtime}:operation:${serverID}:${rangeHours}`);
  const activeServer = servers.data?.find(server => server.id === serverID);
  const sampled = useMemo(() => downsampleMetrics(metrics.data ?? [], 420), [metrics.data]);
  const network = useMemo(() => networkRates(sampled), [sampled]);
  const rawNetwork = useMemo(() => networkRates(metrics.data ?? []), [metrics.data]);
  const latest = metrics.data?.at(-1);
  const latestNetwork = rawNetwork.at(-1);
  const resourcePoints = sampled.map(point => ({ time: point.bucket_at, values: [point.cpu_percent, point.memory_percent, point.disk_percent] }));
  const loadPoints = sampled.map(point => ({ time: point.bucket_at, values: [point.load_1] }));
  const networkPoints = network.map(point => ({ time: point.time, values: [point.rx, point.tx] }));
  const ranges = [1, 6, 24, 168];
  const metricAction = <div className="monitor-actions"><select className="compact-select" aria-label={t("monitor.selectServer")} value={serverID} onChange={event => setServerID(event.target.value)}>{(servers.data ?? []).map(server => <option key={server.id} value={server.id}>{server.name}</option>)}</select><div className="range-control" role="group" aria-label={t("monitor.timeRange")}>{ranges.map(hours => <button type="button" aria-pressed={rangeHours === hours} className={rangeHours === hours ? "active" : ""} key={hours} onClick={() => setRangeHours(hours)}>{t(`monitor.range.${hours}`)}</button>)}</div></div>;
  return <div className="page-stack">
    <Section title={t("monitor.serverMetrics")} icon={<MonitorDot size={18} />} action={metricAction}>
      <div className="monitor-header"><div><strong>{activeServer?.name ?? t("server.none")}</strong><ServerInformation server={activeServer} className="monitor-server-information" /><small>{latest ? t("monitor.lastSample", { time: formatDate(latest.bucket_at) }) : t("monitor.awaitingData")}</small></div><div className="monitor-header-meta"><span>{t("monitor.samples", { count: String(metrics.data?.length ?? 0) })}</span><Status value={activeServer?.status ?? "offline"} /></div></div>
      <div className="metric-summary-grid">
        <MetricStat icon={<Cpu size={17} />} label={t("monitor.cpu")} value={formatPercent(latest?.cpu_percent)} detail={t("monitor.peak", { value: formatPercent(metricPeak(metrics.data, point => point.cpu_percent)) })} tone="green" />
        <MetricStat icon={<MemoryStick size={17} />} label={t("monitor.memory")} value={formatPercent(latest?.memory_percent)} detail={t("monitor.peak", { value: formatPercent(metricPeak(metrics.data, point => point.memory_percent)) })} tone="cyan" />
        <MetricStat icon={<HardDrive size={17} />} label={t("monitor.disk")} value={formatPercent(latest?.disk_percent)} detail={t("monitor.peak", { value: formatPercent(metricPeak(metrics.data, point => point.disk_percent)) })} tone="amber" />
        <MetricStat icon={<Gauge size={17} />} label={t("monitor.load1")} value={formatDecimal(latest?.load_1)} detail={t("monitor.peak", { value: formatDecimal(metricPeak(metrics.data, point => point.load_1)) })} tone="violet" />
        <MetricStat icon={<ArrowDownToLine size={17} />} label={t("monitor.networkIn")} value={formatByteRate(latestNetwork?.rx)} detail={t("monitor.peak", { value: formatByteRate(metricPeak(rawNetwork, point => point.rx)) })} tone="blue" />
        <MetricStat icon={<ArrowUpFromLine size={17} />} label={t("monitor.networkOut")} value={formatByteRate(latestNetwork?.tx)} detail={t("monitor.peak", { value: formatByteRate(metricPeak(rawNetwork, point => point.tx)) })} tone="red" />
      </div>
      <div className="monitor-chart-layout">
        <TimeSeriesChart className="resource-chart" title={t("monitor.resourceTrend")} data={resourcePoints} fixedMax={100} axisFormat={value => `${Math.round(value ?? 0)}%`} empty={t("monitor.noMetrics")} series={[{ label: t("monitor.cpu"), color: "#168f68", format: formatPercent }, { label: t("monitor.memory"), color: "#1684a3", format: formatPercent }, { label: t("monitor.disk"), color: "#d28a1d", format: formatPercent }]} />
        <TimeSeriesChart title={t("monitor.loadTrend")} data={loadPoints} axisFormat={formatDecimal} empty={t("monitor.noMetrics")} series={[{ label: t("monitor.load1"), color: "#7b61a8", format: formatDecimal }]} />
        <TimeSeriesChart title={t("monitor.networkTrend")} data={networkPoints} axisFormat={formatByteRate} empty={t("monitor.noMetrics")} series={[{ label: t("monitor.networkIn"), color: "#267aa3", format: formatByteRate }, { label: t("monitor.networkOut"), color: "#b65555", format: formatByteRate }]} />
      </div>
    </Section>
    <Section title={t("monitor.operationMetrics")} icon={<Gauge size={18} />}>
      <div className="monitor-header"><div><strong>{activeServer?.name ?? t("server.none")}</strong><small>{t("monitor.operationCount", { count: String(operationMetrics.data?.total ?? 0) })}</small></div><span className="muted">{t("monitor.thresholds")}</span></div>
      <div className="metric-summary-grid operation-metric-grid">
        <OperationMetricCard label={t("monitor.queueWait")} aggregate={operationMetrics.data?.queue_wait} tone="amber" />
        <OperationMetricCard label={t("monitor.delivery")} aggregate={operationMetrics.data?.delivery} tone="blue" />
        <OperationMetricCard label={t("monitor.execution")} aggregate={operationMetrics.data?.execution} tone="violet" />
      </div>
      {operationMetrics.error && <div className="snapshot-notice warning"><AlertTriangle size={15} />{operationMetrics.error}</div>}
    </Section>
    <Section title={t("monitor.alerts")} icon={<BellRing size={18} />}><DataTable headers={[t("column.severity"), t("column.alert"), t("column.server"), t("column.state"), t("column.started"), ""]} empty={t("monitor.noAlerts")}>{(alerts.data ?? []).map(alert => <tr key={alert.id}><td><Status value={alert.severity} /></td><td><div className="cell-main"><strong>{alert.title}</strong><small>{alert.detail}</small></div></td><td>{alert.server_name}</td><td><Status value={alert.status} /></td><td>{relative(alert.opened_at)}</td><td>{!alert.acknowledged_at && <button className="icon-button" title={t("monitor.acknowledge")} onClick={async () => { await post(`/alerts/${alert.id}/acknowledge`, {}); alerts.reload(); }}><Check size={16} /></button>}</td></tr>)}</DataTable></Section>
  </div>;
}

function MetricStat({ icon, label, value, detail, tone }: { icon: ReactNode; label: string; value: string; detail: string; tone: string }) {
  return <div className={`metric-stat ${tone}`}><span className="metric-stat-icon">{icon}</span><div><small>{label}</small><strong>{value}</strong><span>{detail}</span></div></div>;
}

function formatLatency(value?: number) {
  if (!Number.isFinite(value)) return "-";
  if ((value ?? 0) >= 1000) return `${((value ?? 0) / 1000).toFixed((value ?? 0) >= 10_000 ? 0 : 1)}s`;
  return `${Math.round(value ?? 0)}ms`;
}

function OperationMetricCard({ label, aggregate, tone }: { label: string; aggregate?: OperationMetrics["queue_wait"]; tone: string }) {
  const { t } = useI18n();
  return <div className={`metric-stat ${tone}`}><span className="metric-stat-icon"><Gauge size={17} /></span><div><small>{label}</small><strong>{formatLatency(aggregate?.average_ms)}</strong><span>{aggregate?.count ? `${t("monitor.p95", { value: formatLatency(aggregate.p95_ms) })} · ${t("monitor.max", { value: formatLatency(aggregate.max_ms) })}` : "-"}</span></div></div>;
}

function TimeSeriesChart({ title, data, series, axisFormat, empty, fixedMax, className = "" }: { title: string; data: ChartPoint[]; series: ChartSeries[]; axisFormat: (value?: number) => string; empty: string; fixedMax?: number; className?: string }) {
  const [hoverIndex, setHoverIndex] = useState<number | null>(null);
  const width = 640; const left = 48; const right = 626; const top = 12; const bottom = 164;
  const observedMax = Math.max(0, ...data.flatMap(point => point.values).filter(Number.isFinite));
  const maximum = fixedMax ?? niceMaximum(observedMax);
  const x = (index: number) => data.length <= 1 ? (left + right) / 2 : left + (index / (data.length - 1)) * (right - left);
  const y = (value: number) => bottom - (Math.max(0, Math.min(maximum, Number.isFinite(value) ? value : 0)) / maximum) * (bottom - top);
  const hovered = hoverIndex === null ? null : data[hoverIndex];
  const span = data.length > 1 ? new Date(data.at(-1)!.time).getTime() - new Date(data[0].time).getTime() : 0;
  const timeIndexes = data.length ? Array.from(new Set([0, Math.floor((data.length - 1) / 2), data.length - 1])) : [];
  const move = (event: ReactPointerEvent<SVGSVGElement>) => {
    if (!data.length) return;
    const bounds = event.currentTarget.getBoundingClientRect();
    const viewX = ((event.clientX - bounds.left) / bounds.width) * width;
    const ratio = Math.max(0, Math.min(1, (viewX - left) / (right - left)));
    setHoverIndex(Math.round(ratio * (data.length - 1)));
  };
  return <div className={`timeseries-chart ${className}`}>
    <div className="timeseries-heading"><strong>{title}</strong><div className="chart-legend">{series.map(item => <span key={item.label}><i style={{ background: item.color }} />{item.label}</span>)}</div></div>
    {data.length === 0 ? <div className="chart-empty">{empty}</div> : <div className="chart-canvas">
      <svg viewBox={`0 0 ${width} 190`} preserveAspectRatio="none" role="img" aria-label={title} onPointerMove={move} onPointerLeave={() => setHoverIndex(null)}>
        {[maximum, maximum / 2, 0].map((tick, index) => { const py = top + index * ((bottom - top) / 2); return <g key={tick}><line x1={left} y1={py} x2={right} y2={py} className="chart-gridline" /><text x={left - 8} y={py + 3} textAnchor="end" className="chart-axis-label">{axisFormat(tick)}</text></g>; })}
        {timeIndexes.map(index => <text x={x(index)} y="184" textAnchor={index === 0 ? "start" : index === data.length - 1 ? "end" : "middle"} className="chart-axis-label" key={index}>{formatAxisTime(data[index].time, span)}</text>)}
        {series.map((item, seriesIndex) => <polyline key={item.label} points={data.map((point, index) => `${x(index)},${y(point.values[seriesIndex] ?? 0)}`).join(" ")} fill="none" stroke={item.color} strokeWidth="2" vectorEffect="non-scaling-stroke" />)}
        {hovered && <g><line x1={x(hoverIndex!)} y1={top} x2={x(hoverIndex!)} y2={bottom} className="chart-cursor" />{series.map((item, seriesIndex) => <circle key={item.label} cx={x(hoverIndex!)} cy={y(hovered.values[seriesIndex] ?? 0)} r="4" fill={item.color} stroke="#fff" strokeWidth="2" vectorEffect="non-scaling-stroke" />)}</g>}
      </svg>
      {hovered && <div className="chart-tooltip" style={{ left: `${(x(hoverIndex!) / width) * 100}%`, transform: hoverIndex! > data.length * .7 ? "translateX(-100%)" : "translateX(0)" }}><strong>{formatDate(hovered.time)}</strong>{series.map((item, index) => <span key={item.label}><i style={{ background: item.color }} />{item.label}<b>{item.format(hovered.values[index])}</b></span>)}</div>}
    </div>}
  </div>;
}

function downsampleMetrics(points: Metric[], maximumPoints: number): Metric[] {
  if (points.length <= maximumPoints) return points;
  const size = Math.ceil(points.length / maximumPoints);
  const output: Metric[] = [];
  for (let start = 0; start < points.length; start += size) {
    const bucket = points.slice(start, start + size);
    const last = bucket[bucket.length - 1];
    const average = (read: (point: Metric) => number) => bucket.reduce((sum, point) => sum + read(point), 0) / bucket.length;
    output.push({ ...last, cpu_percent: average(point => point.cpu_percent), memory_percent: average(point => point.memory_percent), disk_percent: average(point => point.disk_percent), load_1: average(point => point.load_1) });
  }
  return output;
}

function networkRates(points: Metric[]): NetworkRate[] {
  return points.map((point, index) => {
    if (index === 0) return { time: point.bucket_at, rx: 0, tx: 0 };
    const previous = points[index - 1];
    const seconds = Math.max(1, (new Date(point.bucket_at).getTime() - new Date(previous.bucket_at).getTime()) / 1000);
    return { time: point.bucket_at, rx: point.net_rx_bytes >= previous.net_rx_bytes ? (point.net_rx_bytes - previous.net_rx_bytes) / seconds : 0, tx: point.net_tx_bytes >= previous.net_tx_bytes ? (point.net_tx_bytes - previous.net_tx_bytes) / seconds : 0 };
  });
}

function metricPeak<T>(values: T[] | null | undefined, read: (value: T) => number): number | undefined {
  return values?.length ? Math.max(...values.map(read).filter(Number.isFinite)) : undefined;
}

function niceMaximum(value: number) {
  if (!Number.isFinite(value) || value <= 0) return 1;
  const padded = value * 1.1;
  const magnitude = 10 ** Math.floor(Math.log10(padded));
  return Math.max(1, Math.ceil((padded / magnitude) * 2) / 2 * magnitude);
}

function formatPercent(value?: number) { return Number.isFinite(value) ? `${value!.toFixed(1)}%` : "-"; }
function formatDecimal(value?: number) { return Number.isFinite(value) ? value!.toFixed(value! >= 10 ? 1 : 2) : "-"; }
function formatByteRate(value?: number) { if (!Number.isFinite(value)) return "-"; const units = ["B/s", "KB/s", "MB/s", "GB/s"]; let scaled = Math.max(0, value!); let unit = 0; while (scaled >= 1024 && unit < units.length - 1) { scaled /= 1024; unit++; } return `${scaled.toFixed(scaled >= 100 ? 0 : scaled >= 10 ? 1 : 2)} ${units[unit]}`; }
function formatAxisTime(value: string, span: number) { return new Intl.DateTimeFormat(currentLocale(), span > 24 * 60 * 60 * 1000 ? { month: "short", day: "numeric", hour: "2-digit" } : { hour: "2-digit", minute: "2-digit" }).format(new Date(value)); }
function ServerInformation({ server, className = "" }: { server?: Server; className?: string }) {
  const { t } = useI18n();
  if (!server || (!server.address && !server.configuration && !server.notes)) return <span className="muted">{t("server.noInformation")}</span>;
  return <div className={`server-information ${className}`}>{server.address && <span className="server-information-line" title={`${t("server.address")}: ${server.address}`}><MapPin size={13} /><code>{server.address}</code></span>}{server.configuration && <span className="server-information-line" title={`${t("server.configuration")}: ${server.configuration}`}><SettingsIcon size={13} /><span>{server.configuration}</span></span>}{server.notes && <span className="server-information-line" title={`${t("server.notes")}: ${server.notes}`}><StickyNote size={13} /><span>{server.notes}</span></span>}</div>;
}

export default MonitoringPage;
