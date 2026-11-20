import { Fragment, useEffect, useId, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import { api, type BilledReport, type CellAgent, type CostCell, type CostCells, type ExternalSource, type ExternalUsage, type UsageReading } from "../api";
import { fmtBucket, fmtClockUTC, fmtCount, fmtCountAxis, fmtNumber, fmtUSD, fmtUSDAxis, fmtUSDBelow, fmtUSDFixed } from "../format";
import { SOURCES, SOURCE_COLOR, SPLIT_COLOR, SPLIT_HATCH, sourceHint, sourceLabel, type Source } from "./costSources";

export { SOURCE_COLOR };

import {
  billedFetchBucket,
  billedPerPeriod,
  billedWholeDays,
  credentialFigures,
  credId,
  isSeat,
  leadOf,
  meterPoints,
  meterY,
  type MeterPoint,
  periodMultiple,
  priceTableDifference,
  pricePerPoint,
  recomputedOf,
  meterSplit,
  splitPerPeriod,
  stepRate,
  type CredentialFigures,
  type SplitPeriod,
  EMPTY_SPLIT_PERIOD,
} from "./costFigures";
import { PlanPrice } from "./PlanPrice";
import { CostBasisPanel, MonthlyMeters, fmtMoney, usageSub } from "./CostBasis";
import {
  OTHER_SOURCES,
  beforeRange,
  listedSources,
  outsideKpis,
  outsideKpisBySource,
  outsideSplit,
  type OutsideSplit,
  seatSources,
  sourceStyles,
  hostsBehind,
  kpiLead,
  sourceRealParts,
  sourceRealPerPeriod,
  sourcesPerPeriod,
  sourcesWithoutSeat,
  type HostBehind,
  type OutsideKpis,
  type SourcePeriod,
  type SourceStyles,
} from "./outsideFigures";

/* The cost explorer. The credential is the first level: what money means
 * depends on it, so every figure belongs to one credential and nothing is
 * added up across two. Per credential four figures (costFigures.ts):
 *
 *   reported    — what Claude Code priced its own lines at (grey);
 *   recomputed  — the same lines' tokens priced with the list (blue): a
 *                 check of the price table, not of the tokens;
 *   real        — a subscription's meter movement × the plan's price per
 *                 point, an estimate with a range (green, hatched); or what
 *                 Anthropic billed an API key over the window it names,
 *                 complete up to its mark (teal), or why there is no figure;
 *   multiple    — list $ per real $, only from 3 points of movement, with its
 *                 range, and a note that it can be off in both directions.
 *
 * The booked figure (cost_entries) is not shown as a figure anywhere: a
 * booking-check row says when some bookings are an older daemon's running
 * totals. */

export type Split = "type" | "none";
type Layout = "combined" | "agents";
/** "amounts": every source's bar; "vs": the list bar and the real bar of each period side by side. */
type View = "amounts" | "vs";

/** Cost types: one blue family from light (cheap, plentiful) to dark (dear).
 *  Shared with the cost-types card and the token chart. */
export const TYPE_COLOR: Record<string, string> = {
  input: "var(--cost-type-input)",
  cache_read: "var(--cost-type-cache-read)",
  cache_write: "var(--cost-type-cache-write)",
  cache_write_5m: "var(--cost-type-cache-write)",
  cache_write_1h: "var(--cost-type-cache-write-1h)",
  output: "var(--cost-type-output)",
  web_search: "var(--cost-type-web-search)",
  unpriced: "var(--cost-type-unpriced)",
};
/** The cost types of the recomputed figure, bottom of a bar first. */
const COST_TYPES = ["cache_read", "cache_write", "input", "output", "web_search", "unpriced"];
/** Token types, bottom of a bar first. */
export const TOKEN_TYPES = ["cache_read", "cache_write_5m", "cache_write_1h", "input", "output"];

const AGENT_COLORS = [1, 2, 3, 4, 5, 6, 7, 8].map((i) => `var(--agent-${i})`);

export const BUCKET_MS: Record<string, number> = { "10min": 600_000, hour: 3600_000, "10hour": 36_000_000, day: 86400_000, week: 7 * 86400_000, month: 30 * 86400_000 };

export function useCostCells(days: number, bucket: string) {
  return useQuery({
    queryKey: ["cost", "cells", days, bucket],
    queryFn: () => api<CostCells>(`/cost/cells?days=${days}&bucket=${bucket}`),
    refetchInterval: 30000,
  });
}

/** The provider's meter readings of the period: the cards below the chart
 *  show them, and the plan form takes a credential's place and its weekly
 *  reset from them. */
export function useUsageReadings(days: number) {
  return useQuery({
    queryKey: ["cost", "usage-readings", days],
    queryFn: () => api<UsageReading[] | null>(`/cost/usage-readings?days=${days}`),
    refetchInterval: 30000,
  });
}

/** What Anthropic billed, per bucket and API key; an organisation without an
 *  admin key gets an empty bill, not an error. */
export function useBilled(days: number, bucket: string) {
  // Hours or days, summed into the chart's bucket on the page (billedPerPeriod).
  const fetchBucket = billedFetchBucket(days, bucket);
  return useQuery({
    queryKey: ["cost", "billed", days, fetchBucket],
    queryFn: () => api<BilledReport>(`/cost/billed?days=${days}&bucket=${fetchBucket}`),
    refetchInterval: 60000,
    retry: false,
  });
}

/** Agents ranked by their recomputed list price. */
export function rankAgents(agents: readonly CellAgent[]): CellAgent[] {
  return [...agents].sort((a, b) => (b.measured_usd ?? 0) - (a.measured_usd ?? 0));
}

/** Agent colour by rank; the agent palette never reuses a source hue. */
export function agentColor(agents: readonly { id: string }[], id: string) {
  const i = agents.findIndex((a) => a.id === id);
  return AGENT_COLORS[(i < 0 ? 0 : i) % AGENT_COLORS.length];
}

export function Chip({ on, color, hatched, hatch, ink, children, onClick, onDoubleClick, locked, title, disabled, describedBy }: {
  on: boolean; color?: string; hatched?: boolean; hatch?: number; ink?: string; children: React.ReactNode; onClick: () => void; onDoubleClick?: () => void;
  /** The id of visible text that says why the chip is disabled. */
  describedBy?: string;
  /** The last chip that is on: it stays, and says so instead of ignoring the click. */
  locked?: boolean; title?: string;
  /** Not available here (e.g. Real and Billed at 10 minutes); title says why. */
  disabled?: boolean;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      onDoubleClick={onDoubleClick}
      aria-pressed={on}
      aria-disabled={locked || undefined}
      aria-describedby={describedBy}
      disabled={disabled}
      title={title}
      className="text-xs"
      style={{
        display: "inline-flex", alignItems: "center", gap: 6, padding: "4px 10px", borderRadius: 6,
        border: `1px solid ${on ? "var(--border-strong)" : "var(--border)"}`,
        background: on ? "var(--surface-2)" : "transparent",
        color: on ? "var(--text-primary)" : "var(--text-muted)",
        cursor: disabled ? "not-allowed" : locked ? "default" : "pointer", lineHeight: 1.2, opacity: disabled ? 0.45 : 1,
      }}
    >
      {color && <Swatch color={color} hatched={hatched} hatch={hatch} ink={ink} dim={!on} />}
      {children}
    </button>
  );
}

/** The hatches: 0 is a hatched bar's own (the real cost, 45°); 1–3 at 135°,
 *  90° and 0° (the outside part, and with 0 the sources' four angles,
 *  outsideFigures.SOURCE_HATCHES); 4 the cross-hatch (the unattributed
 *  part); 5 dots ("other sources"). */
export const HATCH_ANGLES = [45, 135, 90, 0, 45, 45];
/** The cross-hatch: the unattributed part's (SPLIT_HATCH). */
export const CROSS_HATCH = 4;
/** Dots: the sources folded into "other sources" (outsideFigures.OTHER_SOURCES_HATCH). */
export const DOT_HATCH = 5;

/** A pattern's identity: its hatch, the colour of its tint and outline, and
 *  the ink of its stripes (the segment's colour where none). */
export const patternKey = (color: string, hatch: number, ink?: string) => `${hatch}|${color}|${ink ?? ""}`;

/** The one <pattern> of a hatch, drawn alike in a bar and in its swatch (a
 *  legend, a chip, a table row, the tooltip), so the two can never drift
 *  apart: a CSS gradient's angle runs perpendicular to an SVG rotate of the
 *  same number (#120 review, H1). */
export function HatchPattern({ id, color, hatch, ink }: { id: string; color: string; hatch: number; ink?: string }) {
  const stroke = ink ?? color;
  return (
    <pattern id={id} data-hatch-pattern={hatch} data-pattern-key={patternKey(color, hatch, ink)} patternUnits="userSpaceOnUse"
      width={5} height={5} patternTransform={`rotate(${HATCH_ANGLES[hatch] ?? 45})`}>
      <rect width={5} height={5} fill={color} opacity={0.3} />
      {hatch === DOT_HATCH
        ? <circle cx={2.5} cy={2.5} r={1.3} fill={stroke} />
        : <line x1={0} y1={0} x2={0} y2={5} stroke={stroke} strokeWidth={hatch === CROSS_HATCH ? 2 : 3} />}
      {hatch === CROSS_HATCH && <line x1={0} y1={0} x2={5} y2={0} stroke={stroke} strokeWidth={2} />}
    </pattern>
  );
}

/** A tiny SVG in a series' colour and, hatched, in the very pattern of its
 *  bar (HatchPattern). */
export function Swatch({ color, hatched, dim, dashed, hatch, ink }: {
  color: string; hatched?: boolean; dim?: boolean; dashed?: string;
  /** A hatch of its own (an outside source's): HatchPattern's. */
  hatch?: number;
  /** The ink of the stripes (an outside source's). */
  ink?: string;
}) {
  const pid = `sw${useId().replace(/[^a-zA-Z0-9_-]/g, "")}`;
  const striped = (hatched || hatch != null) && !dashed;
  const h = hatch ?? 0;
  return (
    <svg aria-hidden data-swatch data-hatch={hatch} data-pattern-key={striped ? patternKey(color, h, ink) : undefined}
      width={10} height={10} viewBox="0 0 10 10"
      style={{ display: "inline-block", flexShrink: 0, opacity: dim ? 0.4 : 1, overflow: "visible" }}>
      {striped && <defs><HatchPattern id={pid} color={color} hatch={h} ink={ink} /></defs>}
      <rect x={0.5} y={0.5} width={9} height={9} rx={1.5}
        fill={striped ? `url(#${pid})` : color}
        stroke={dashed ?? (striped ? color : "none")} strokeDasharray={dashed ? "2 2" : undefined} />
    </svg>
  );
}

/** A card that does not follow the agent filter says so: its figures are
 *  the whole organisation's while the rest of the page is narrowed. */
export function OrgWide() {
  const { t } = useTranslation();
  return (
    <span className="muted text-xs" data-org-wide style={{
      marginLeft: 8, padding: "1px 6px", borderRadius: 4, border: "1px solid var(--border)", fontWeight: 400, whiteSpace: "nowrap",
    }}>{t("costs.orgWide")}</span>
  );
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-wrap items-center gap-2" style={{ marginBottom: 8 }}>
      <span className="muted text-xs" style={{ width: 84, flexShrink: 0 }}>{label}</span>
      {children}
    </div>
  );
}

/** Chips a department shows before "+N more" folds the rest away. */
export const DEPT_CHIPS = 5;

export type DepartmentGroup = {
  /** The department's id; "" for the agents without one. */
  key: string;
  /** Null for the group without a department: the heading is localized by the caller. */
  name: string | null;
  /** Their recomputed list price over the window. */
  usd: number;
  /** Ranked by cost, as the flat filter was. */
  agents: CellAgent[];
};

/** The agents grouped by their current department: departments by their
 *  cost in the window, descending, then by name; the agents without one
 *  last. Not Dashboard's groupByDepartment, which orders the org chart's way. */
export function groupCellAgentsByDepartment(agents: readonly CellAgent[]): DepartmentGroup[] {
  const groups = new Map<string, DepartmentGroup>();
  for (const a of rankAgents(agents)) {
    const key = a.department_id ?? "";
    let g = groups.get(key);
    if (!g) {
      g = { key, name: key ? a.department ?? key : null, usd: 0, agents: [] };
      groups.set(key, g);
    }
    g.usd += a.measured_usd ?? 0;
    g.agents.push(a);
  }
  return [...groups.values()].sort((a, b) =>
    a.key === "" ? 1 : b.key === "" ? -1 : b.usd - a.usd || (a.name ?? "").localeCompare(b.name ?? ""));
}

/** The agent filter: chips, any combination; "All" = nobody narrowed out.
 *  With departments the chips sit under their heading, and the heading
 *  selects the whole department: pressed, not pressed, or mixed. */
export function AgentFilter({ agents, selected, onChange }: {
  agents: CellAgent[]; selected: Set<string>; onChange: (s: Set<string>) => void;
}) {
  const { t } = useTranslation();
  const ranked = rankAgents(agents);
  const [open, setOpen] = useState<Set<string>>(new Set());
  const [pinned, setPinned] = useState<Set<string>>(new Set());
  const chipsId = useId().replace(/[^a-zA-Z0-9_-]/g, "");
  const toggle = (id: string) => {
    const n = new Set(selected);
    if (n.has(id)) n.delete(id);
    else n.add(id);
    onChange(n);
  };
  const chip = (a: CellAgent) => (
    <Chip key={a.id} on={selected.has(a.id)} color={agentColor(ranked, a.id)}
      onClick={() => toggle(a.id)} onDoubleClick={() => onChange(new Set([a.id]))}>
      {a.label} <span style={{ opacity: 0.7 }}>{fmtUSD(a.measured_usd ?? 0)}</span>
    </Chip>
  );
  const hint = ranked.length > 1 && <span className="muted text-xs">{t("costs.filter.toggleHint")}</span>;
  const all = <Chip on={selected.size === 0} onClick={() => onChange(new Set())}>{t("costs.filter.all")}</Chip>;

  // Nobody has a department: headings would all say the same, keep the flat row.
  const flat = !ranked.some((a) => a.department_id);
  const groups = flat ? [] : groupCellAgentsByDepartment(ranked);
  // A folded group shows its first chips and the selected ones: a selection
  // no chip shows would narrow the figures unseen. A chip on screen only
  // because it is selected is pinned there until its group is folded or
  // unfolded, so deselecting it does not pull it from under the pointer or
  // the keyboard focus.
  const toPin = groups.flatMap((g) => open.has(g.key) ? [] : g.agents
    .filter((a, i) => i >= DEPT_CHIPS && selected.has(a.id) && !pinned.has(a.id)).map((a) => a.id));
  useEffect(() => {
    if (toPin.length) setPinned((p) => new Set([...p, ...toPin]));
  }, [toPin.join(",")]); // eslint-disable-line react-hooks/exhaustive-deps

  if (flat) {
    return (
      <Row label={t("costs.filter.agents")}>
        {all}
        {ranked.map(chip)}
        {hint}
      </Row>
    );
  }

  const toggleGroup = (g: DepartmentGroup) => {
    const n = new Set(selected);
    const everyone = g.agents.every((a) => n.has(a.id));
    for (const a of g.agents) {
      if (everyone) n.delete(a.id);
      else n.add(a.id);
    }
    onChange(n);
  };
  const fold = (g: DepartmentGroup, expand: boolean) => {
    setOpen((s) => {
      const n = new Set(s);
      if (expand) n.add(g.key);
      else n.delete(g.key);
      return n;
    });
    setPinned((p) => new Set([...p].filter((id) => !g.agents.some((a) => a.id === id))));
  };
  return (
    <Row label={t("costs.filter.agents")}>
      <div style={{ flex: "1 1 240px", minWidth: 0 }}>
        <div className="flex flex-wrap items-center gap-2" style={{ marginBottom: 8 }}>
          {all}
          {hint}
        </div>
        {groups.map((g) => {
          const name = g.name ?? t("costs.filter.noDepartment");
          const picked = g.agents.filter((a) => selected.has(a.id)).length;
          const expanded = open.has(g.key);
          const shown = expanded ? g.agents
            : g.agents.filter((a, i) => i < DEPT_CHIPS || selected.has(a.id) || pinned.has(a.id));
          const hidden = g.agents.length - shown.length;
          // One disclosure button, kept mounted across Enter so the focus
          // stays on it: "+N more" while chips are hidden, "Show fewer" while
          // all show and folding would hide one. That also holds for a folded
          // group whose pins show every chip; "Show fewer" lets go of them.
          const showsAll = hidden === 0;
          const canHide = g.agents.some((a, i) => i >= DEPT_CHIPS && !selected.has(a.id));
          const disclosure = !showsAll || canHide;
          const rowId = `${chipsId}-${g.key || "none"}`;
          return (
            <div key={g.key} role="group" aria-label={name} data-department={g.key || "none"} style={{ marginBottom: 8 }}>
              <button
                type="button"
                onClick={() => toggleGroup(g)}
                aria-pressed={picked === 0 ? false : picked === g.agents.length ? true : "mixed"}
                className="text-xs"
                style={{
                  display: "inline-flex", alignItems: "baseline", gap: 6, maxWidth: "100%", padding: "2px 0", marginBottom: 4,
                  border: 0, background: "transparent", cursor: "pointer", textAlign: "left", overflowWrap: "anywhere",
                  color: picked ? "var(--text-primary)" : "var(--text-muted)", fontWeight: 600,
                }}
              >
                <span aria-hidden style={{ opacity: 0.7 }}>{picked === 0 ? "○" : picked === g.agents.length ? "●" : "◐"}</span>
                {name} <span style={{ opacity: 0.7, fontWeight: 400, whiteSpace: "nowrap" }}>{fmtUSD(g.usd)}</span>
              </button>
              <div id={rowId} className="flex flex-wrap items-center gap-2">
                {shown.map(chip)}
                {disclosure && (
                  <button type="button" className="muted text-xs" aria-expanded={showsAll} aria-controls={rowId}
                    onClick={() => fold(g, !showsAll)}
                    style={{ border: 0, background: "transparent", cursor: "pointer", padding: "4px 2px" }}>
                    {showsAll ? t("costs.filter.fewer") : t("costs.filter.more", { n: hidden })}
                  </button>
                )}
              </div>
            </div>
          );
        })}
      </div>
    </Row>
  );
}

function Tile({ label, value, sub, span, note, check, color, hatched, parts, orgWide }: {
  label: string; value: string; sub?: string; span?: string; note?: string; color?: string; hatched?: boolean;
  /** covey's price table where it differs from the figure (priceTableLine). */
  check?: string;
  /** With the outside sessions included: the figure's parts, named. */
  parts?: React.ReactNode;
  /** The figure does not follow the agent filter, and says so. */
  orgWide?: boolean;
}) {
  return (
    <div role="group" aria-label={label} data-tile
      style={{ padding: "10px 14px", borderRadius: 8, border: "1px solid var(--border)", minWidth: 170, flex: "1 1 170px" }}>
      <div className="muted text-xs" style={{ display: "flex", alignItems: "center", gap: 6, flexWrap: "wrap" }}>
        {color && <Swatch color={color} hatched={hatched} />}
        {label}
        {orgWide && <OrgWide />}
      </div>
      <div style={{ fontSize: 20, fontWeight: 600, marginTop: 2, fontVariantNumeric: "tabular-nums" }}>{value}</div>
      {parts && <div className="muted text-xs" data-parts style={{ marginTop: 2 }}>{parts}</div>}
      {sub && <div className="muted text-xs" style={{ marginTop: 2 }}>{sub}</div>}
      {check && <div className="muted text-xs" data-price-check style={{ marginTop: 2 }}>{check}</div>}
      {span && <div className="muted text-xs" data-span style={{ marginTop: 2 }}>{span}</div>}
      {note && <div className="muted text-xs" style={{ marginTop: 2, fontStyle: "italic" }}>{note}</div>}
    </div>
  );
}

/** A "nice" axis: a top and a step that read as round numbers. */
export function niceAxis(max: number) {
  const raw = Math.max(1e-9, max) / 4;
  const mag = Math.pow(10, Math.floor(Math.log10(raw)));
  const n = raw / mag;
  const step = (n <= 1 ? 1 : n <= 2 ? 2 : n <= 2.5 ? 2.5 : n <= 5 ? 5 : 10) * mag;
  const top = Math.ceil(Math.max(1e-9, max) / step) * step;
  const ticks: number[] = [];
  for (let i = 0; i * step <= top + step / 2; i++) ticks.push(i * step);
  return { top, ticks, step };
}

/** dashed: the segment is drawn plain with a dashed edge in that colour
 *  instead of the bar's hatch, so it reads apart from its neighbours by its
 *  pattern, not by its colour alone. */
export type Seg = {
  key: string; label: string; color: string; value: number; dashed?: string;
  /** Hatched at its own angle (an outside source's series), whatever the bar. */
  hatch?: number;
  /** The ink of its stripes (an outside source's): HatchPattern's. */
  ink?: string;
  /** A column of its own in the table, under this header (an outside source's series). */
  column?: string;
  /** What the table says where the segment has no figure (e.g. "no list price"), never $0. */
  none?: string;
};
export type Bar = {
  key: string; label: string; color: string; hatched?: boolean; segs: Seg[];
  /** Print the bar's value above it, not only in the details. */
  valueLabel?: boolean;
  /** A small label above the value, e.g. a multiple. */
  tag?: string;
  /** What the tag leaves out, in the period's details, e.g. the multiple's range. */
  tagDetail?: string;
  /** A line under the bar's row in the period's details and a column of its
   *  own in the table: covey's price table where it differs from the bar
   *  (priceTableLine); none where they agree. */
  note?: string;
};

const sum = (s: Seg[]) => s.reduce((a, x) => a + x.value, 0);

/** The weekly meter's right-hand axis: fixed, whatever the readings were. */
const METER_TICKS = [0, 25, 50, 75, 100];
/** The width of that axis: its labels ("100 %") and its rotated title. */
const METER_AXIS_W = 60;
/** A tooltip lists this many readings from each end of a period; more are left out as "…". */
const READINGS_SHOWN = 4;

/** The readings a period's details list, each as its time and value ("18:05 UTC 9 %"), the first of a new week
 *  marked as such; null marks the readings left out of the middle. */
function readingTexts(ps: readonly MeterPoint[], fmtAt: (iso: string) => string, t: TFunction): (string | null)[] {
  const text = (p: MeterPoint) => `${fmtAt(p.at)} ${fmtNumber(p.pct, 0)} %${p.newWeek ? ` (${t("costs.fig.meterReset")})` : ""}`;
  return ps.length <= 2 * READINGS_SHOWN ? ps.map(text) : [...ps.slice(0, READINGS_SHOWN).map(text), null, ...ps.slice(-READINGS_SHOWN).map(text)];
}

/** The meter's line: one part per run of readings with none missing between
 *  them, a dot at each reading. Drawn only for what was measured, so a gap
 *  stays a gap. A weekly reset between two readings that exist is a fall from
 *  the old week's last value to the new week's first, a stroke that goes down.
 *  The line is solid: dashed is the edge of a bar's "not split" part, and a gap is
 *  empty space, not a different stroke. Not for the pointer or a screen reader:
 *  the period's details say the same. */
function MeterLine({ points, x, y }: { points: readonly MeterPoint[]; x: (p: MeterPoint) => number; y: (p: MeterPoint) => number }) {
  const parts: MeterPoint[][] = [];
  for (const p of points) (parts[p.part] ??= []).push(p);
  // Many readings (a day or a week per slot) would run into a string of beads: the dots shrink then.
  const dense = points.length > DENSE_POINTS;
  const pts = (ps: readonly MeterPoint[]) => ps.map((p) => `${x(p).toFixed(1)},${y(p).toFixed(1)}`).join(" ");
  return (
    <g aria-hidden pointerEvents="none" data-meter-line>
      {parts.filter(Boolean).map((ps) => {
        // The readings of one week, and the fall into each new week from the reading before it.
        const weeks: MeterPoint[][] = [];
        ps.forEach((p, k) => { if (k === 0 || p.reset) weeks.push([]); weeks[weeks.length - 1].push(p); });
        return (
          <g key={ps[0].part} data-meter-part>
            {weeks.map((w, k) => (
              <Fragment key={w[0].at}>
                {k > 0 && (() => {
                  const drop = pts([weeks[k - 1].at(-1)!, w[0]]);
                  return (
                    <>
                      <polyline points={drop} fill="none" stroke="var(--surface-2)" strokeWidth={3.75} strokeLinecap="round" data-meter-casing />
                      <polyline points={drop} fill="none" stroke="var(--cost-meter)" strokeWidth={1.75} strokeLinecap="round" data-meter-drop />
                    </>
                  );
                })()}
                {w.length > 1 && (
                  <>
                    {/* A casing in the card's colour under the line: the line keeps its contrast where it crosses a bar. */}
                    <polyline points={pts(w)} fill="none" stroke="var(--surface-2)" strokeWidth={3.75} strokeLinejoin="round" data-meter-casing />
                    <polyline points={pts(w)} fill="none" stroke="var(--cost-meter)" strokeWidth={1.75} strokeLinejoin="round" />
                  </>
                )}
              </Fragment>
            ))}
            {ps.map((p, k) => {
              // Dense: small dots, but a part's first and last keep their size and halo, so the ends of a gap stay visible.
              const big = !dense || k === 0 || k === ps.length - 1;
              return (
                <circle key={p.at} cx={x(p)} cy={y(p)} r={big ? 2.25 : 1.25} fill="var(--cost-meter)"
                  stroke={big ? "var(--surface-2)" : undefined} strokeWidth={1}
                  data-meter-point data-at={p.at} data-pct={p.pct} />
              );
            })}
          </g>
        );
      })}
    </g>
  );
}

/** More readings than this and the line's dots are drawn small and without a halo, except each part's ends. */
const DENSE_POINTS = 48;

/** The legend entry of the meter's line: its colour and dot. */
export function MeterSwatch() {
  return (
    <svg aria-hidden width={22} height={10} style={{ flexShrink: 0 }}>
      <line x1={0} x2={22} y1={5} y2={5} stroke="var(--cost-meter)" strokeWidth={1.75} />
      <circle cx={11} cy={5} r={2.25} fill="var(--cost-meter)" />
    </svg>
  );
}

/** A chart's notes (a line per period) come with the label of their column
 *  and of their line in the details: notes never go without a label of their own. */
type NotesProps =
  | { notes?: undefined; notesLabel?: undefined }
  | { /** A line per period. */ notes?: Map<number, string>; /** The notes' column header in the table and their label in the details. */ notesLabel: string };

/** Groups of bars over the periods. Every period is focusable: focus or hover
 *  opens its details, Enter/Space (or a tap) pins them, Escape closes them.
 *  "Show as table" renders the same numbers as a table. */
export function BarChart({ title, periods, groups, fmtPeriod, fmtValue, fmtAxis, height = 280, max, notes, notesLabel, meter, scrollKey, showX = true, asTable, wide }: NotesProps & {
  title: string;
  periods: string[];
  groups: Bar[][];
  fmtPeriod: (p: string) => string;
  fmtValue: (v: number) => string;
  /** A tick label; step is the distance between ticks, so a tick can carry its precision. */
  fmtAxis: (v: number, step: number) => string;
  height?: number;
  /** A shared top for several charts (small multiples). */
  max?: number;
  /** The weekly meter as a line over the periods, on a right-hand axis from 0
   *  to 100 %: the readings, the axis's title ("Weekly meter (%)"), the meter's
   *  name for the details and the table ("Weekly meter") and how a reading's
   *  time reads. */
  meter?: { points: readonly MeterPoint[]; label: string; name: string; fmtAt: (iso: string) => string };
  /** What the chart shows, as the caller knows it (the range and the bucket):
   *  when it changes, a chart with the meter's axis scrolls to its newest period. */
  scrollKey?: string;
  showX?: boolean;
  asTable?: boolean;
  /** Wider bars, for a value printed above each: the label spills over less. */
  wide?: boolean;
}) {
  const { t } = useTranslation();
  const notesHead = notesLabel ?? "";
  const uid = useId().replace(/[^a-zA-Z0-9_-]/g, "");
  const [hover, setHover] = useState<number | null>(null);
  const [pinned, setPinned] = useState<number | null>(null);
  const open = pinned ?? hover;
  const scroller = useRef<HTMLDivElement>(null);
  // A scroll or a resize re-places an open tooltip (see tipLeft); the number itself is read from the scroller.
  const [, setScrolled] = useState(0);

  // The series and their totals: the <desc>, the table's last row.
  const series = useMemo(() => {
    const m = new Map<string, { key: string; label: string; total: number }>();
    for (const g of groups) for (const b of g) {
      const s = m.get(b.key) ?? { key: b.key, label: b.label, total: 0 };
      s.total += sum(b.segs);
      m.set(b.key, s);
    }
    return [...m.values()];
  }, [groups]);
  // Segments with a column of their own (the outside sessions' series): in
  // the table each stands beside the bar it is part of.
  const extra = useMemo(() => {
    const m = new Map<string, { key: string; bar: string; seg: string; label: string; total: number; any: boolean }>();
    for (const g of groups) for (const b of g) for (const x of b.segs) {
      if (!x.column) continue;
      const k = `${b.key}/${x.key}`;
      const e = m.get(k) ?? { key: k, bar: b.key, seg: x.key, label: x.column, total: 0, any: false };
      e.total += x.value;
      if (!x.none) e.any = true;
      m.set(k, e);
    }
    return [...m.values()];
  }, [groups]);
  // The meter's readings of each period, for its details.
  const readingsAt = useMemo(() => {
    const m = new Map<number, MeterPoint[]>();
    for (const p of meter?.points ?? []) {
      const at = m.get(p.slot);
      if (at) at.push(p); else m.set(p.slot, [p]);
    }
    return m;
  }, [meter]);
  // A chart with the meter's axis scrolls with its newest period in view: the
  // axis sticks to the right edge and would otherwise hide the last bar.
  // It scrolls there on the first draw, when the caller's `scrollKey` (the
  // range and the bucket) changes, and when the view was already at the end;
  // a user who scrolled back to an older period stays there when a refetch
  // adds or drops one. The periods are sparse (only those with data), so the
  // chart cannot tell a new range from a refetch by their count or spacing.
  const hasMeter = meter != null, newest = periods.at(-1);
  const drawn = useRef<{ width: number; key: string | undefined } | null>(null);
  useEffect(() => {
    const el = scroller.current;
    if (!hasMeter || !el) { drawn.current = null; return; }
    const before = drawn.current;
    const atEnd = before != null && el.scrollLeft + el.clientWidth >= before.width - 2;
    if (before == null || atEnd || scrollKey !== before.key) el.scrollLeft = el.scrollWidth;
    drawn.current = { width: el.scrollWidth, key: scrollKey };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hasMeter, periods.length, newest, scrollKey]);
  // A window or orientation change re-places a pinned or open tooltip, as a scroll does.
  const isOpen = (pinned ?? hover) != null;
  useEffect(() => {
    const el = scroller.current;
    if (!isOpen || !el || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(() => setScrolled((n) => n + 1));
    ro.observe(el);
    return () => ro.disconnect();
  }, [isOpen]);
  const desc = t("costs.fig.chartDesc", { totals: series.map((s) => `${s.label} ${fmtValue(s.total)}`).join(", ") });
  // The price-table check of a period's bars: a column only where some period has one.
  // A period's readings as one string: the table's cell and the period's label.
  const readingsLabel = (i: number) => (readingsAt.get(i) ? readingTexts(readingsAt.get(i)!, meter!.fmtAt, t).map((x) => x ?? "…").join(" · ") : "");
  const checks = groups.some((g) => g.some((b) => b.note));
  const checkOf = (i: number) => (groups[i] ?? []).filter((b) => b.note).map((b) => b.note).join(" · ");

  if (asTable) {
    return (
      <div style={{ overflowX: "auto" }}>
        <table style={{ width: "100%", borderCollapse: "collapse", fontSize: 12 }}>
          <caption className="muted text-xs" style={{ textAlign: "left", paddingBottom: 4 }}>{title}</caption>
          <thead>
            <tr className="muted" style={{ textAlign: "right" }}>
              <th scope="col" style={{ textAlign: "left", padding: "2px 8px 4px 0", fontWeight: 500 }}>{t("costs.fig.period")}</th>
              {series.map((s) => <th key={s.key} scope="col" style={{ padding: "2px 8px 4px 0", fontWeight: 500 }}>{s.label}</th>)}
              {extra.map((e) => <th key={e.key} scope="col" data-extra-col style={{ padding: "2px 8px 4px 0", fontWeight: 500 }}>{e.label}</th>)}
              {checks && <th scope="col" data-check-col style={{ padding: "2px 8px 4px 0", fontWeight: 500, textAlign: "left" }}>{t("costs.lead.colCheck")}</th>}
              {notes && <th scope="col" style={{ padding: "2px 8px 4px 0", fontWeight: 500, textAlign: "left" }}>{notesHead}</th>}
              {meter && <th scope="col" data-meter-col style={{ padding: "2px 0 4px 0", fontWeight: 500, textAlign: "left" }}>{meter.name}</th>}
            </tr>
          </thead>
          <tbody>
            {periods.map((p, i) => (
              <tr key={p} style={{ borderTop: "1px solid var(--border)" }}>
                <th scope="row" style={{ textAlign: "left", padding: "2px 8px 2px 0", fontWeight: 400 }}>{fmtPeriod(p)}</th>
                {series.map((s) => {
                  const b = groups[i]?.find((x) => x.key === s.key);
                  return <td key={s.key} style={{ textAlign: "right", padding: "2px 8px 2px 0", fontVariantNumeric: "tabular-nums" }}>{b ? fmtValue(sum(b.segs)) : "–"}</td>;
                })}
                {extra.map((e) => {
                  const x = groups[i]?.find((b) => b.key === e.bar)?.segs.find((g) => g.key === e.seg);
                  return <td key={e.key} style={{ textAlign: "right", padding: "2px 8px 2px 0", fontVariantNumeric: "tabular-nums" }}>
                    {!x ? "–" : x.none && x.value === 0 ? x.none : fmtValue(x.value)}</td>;
                })}
                {checks && <td data-check style={{ padding: "2px 8px 2px 0" }}>{checkOf(i)}</td>}
                {notes && <td style={{ padding: "2px 8px 2px 0" }}>{notes.get(i) ?? ""}</td>}
                {meter && <td data-meter-cell style={{ padding: "2px 0" }}>{readingsLabel(i)}</td>}
              </tr>
            ))}
          </tbody>
          <tfoot>
            <tr data-totals style={{ borderTop: "1px solid var(--border-strong)", fontWeight: 600 }}>
              <th scope="row" style={{ textAlign: "left", padding: "4px 8px 2px 0" }}>{t("costs.fig.total")}</th>
              {series.map((s) => <td key={s.key} data-total style={{ textAlign: "right", padding: "4px 8px 2px 0", fontVariantNumeric: "tabular-nums" }}>{fmtValue(s.total)}</td>)}
              {extra.map((e) => <td key={e.key} data-total style={{ textAlign: "right", padding: "4px 8px 2px 0", fontVariantNumeric: "tabular-nums" }}>{e.any || e.total > 0 ? fmtValue(e.total) : "–"}</td>)}
              {checks && <td />}
              {notes && <td />}
              {meter && <td />}
            </tr>
          </tfoot>
        </table>
      </div>
    );
  }

  const tagged = groups.some((g) => g.some((b) => b.tag != null));
  const padL = 64, padR = 16, padT = tagged ? 30 : 18, padB = showX ? 34 : 8;
  const plotH = height - padT - padB;
  const n = periods.length;
  const nb = Math.max(1, ...groups.map((g) => g.length));
  const barW = wide ? 30 : nb === 1 ? 28 : nb === 2 ? 20 : 16, gap = 3;
  const slot = Math.max(nb * (barW + gap) + 24, n <= 1 ? 160 : Math.min(120, Math.round(900 / Math.max(1, n))));
  const W = Math.max(560, padL + padR + Math.max(1, n) * slot);
  const ax = niceAxis(max ?? Math.max(0, ...groups.flatMap((g) => g.map((b) => sum(b.segs)))));
  const y = (v: number) => padT + plotH - (v / ax.top) * plotH;
  // A label of the widest period's length must not touch its neighbour: at an
  // hour it is "11/17, 04:00 PM UTC", wider than a slot.
  const labelW = Math.max(84, Math.max(0, ...periods.map((p) => fmtPeriod(p).length)) * 6 + 12);
  const every = Math.max(1, Math.ceil(labelW / slot));
  // The tooltip stays inside the part of the chart in view: the scroller's
  // window less the meter's axis, which sticks to its right edge. The details
  // are the only place a reading's value is printed.
  const el = scroller.current;
  const v0 = el?.scrollLeft ?? 0;
  const vw = (el?.clientWidth || W) - (meter ? METER_AXIS_W : 0);
  const tipW = Math.min(250, vw - 8);
  const hx = open != null ? padL + open * slot + slot / 2 : 0;
  const preferred = hx + 16 + tipW > v0 + vw ? hx - 16 - tipW : hx + 16;
  const tipLeft = open == null ? 0 : Math.max(v0 + 4, Math.min(preferred, v0 + vw - tipW - 4));
  const tipId = `${uid}-tip`;
  // A hatched bar is hatched in each segment's own colour: the real cost
  // split between covey and the sessions outside it is one bar of three.
  // Each outside source's series is hatched at an angle of its own.
  // One pattern per hatch, colour and ink (HatchPattern, as the swatches draw it).
  const patterns = new Map<string, { color: string; hatch: number; ink?: string }>();
  const addPattern = (color: string, hatch: number, ink?: string) => patterns.set(patternKey(color, hatch, ink), { color, hatch, ink });
  for (const g of groups) for (const b of g) for (const x of b.segs) {
    if (x.dashed) continue;
    if (x.hatch != null) addPattern(x.color, x.hatch, x.ink);
    else if (b.hatched) addPattern(x.color, 0);
  }
  const hatches = [...patterns.keys()];
  const hatchId = (c: string, v = 0, ink?: string) => `${uid}-h${hatches.indexOf(patternKey(c, v, ink))}`;
  const summary = (i: number) => (groups[i] ?? []).filter((b) => sum(b.segs) > 0).map((b) => `${b.label} ${fmtValue(sum(b.segs))}`).join(", ");
  const close = () => { setPinned(null); setHover(null); };

  return (
    <div ref={scroller} style={{ position: "relative", overflowX: "auto" }}
      onScroll={open != null ? (e) => setScrolled(e.currentTarget.scrollLeft) : undefined}>
      <div style={{ display: "flex", width: "max-content" }}>
      {/* The $ axis in a strip of its own that sticks to the left edge, like the meter's on the right: a chart opened
          scrolled to its end still shows both scales. It stops above the x labels, so the first one is not covered. The plot
          starts under it (its x = 0 is the strip's left). */}
      <svg width={padL} height={height - padB + 6} aria-hidden data-cost-axis
        style={{ display: "block", flex: "none", position: "sticky", left: 0, zIndex: 1, background: "var(--surface-2)", borderRight: "1px solid var(--border)" }}>
        {ax.ticks.map((v, i) => (
          <text key={i} x={padL - 8} y={y(v) + 4} textAnchor="end" fontSize={11} fill="var(--text-muted)" data-cost-tick>{fmtAxis(v, ax.step)}</text>
        ))}
      </svg>
      <svg width={W} height={height} style={{ display: "block", maxWidth: "none", marginLeft: -padL }} role="group"
        aria-labelledby={`${uid}-title`} aria-describedby={`${uid}-desc`}>
        <title id={`${uid}-title`}>{title}</title>
        <desc id={`${uid}-desc`} data-desc>{desc}</desc>
        <defs>
          {hatches.map((h, i) => {
            const p = patterns.get(h)!;
            return <HatchPattern key={h} id={`${uid}-h${i}`} color={p.color} hatch={p.hatch} ink={p.ink} />;
          })}
        </defs>
        {ax.ticks.map((v, i) => (
          <g key={i} aria-hidden>
            <line x1={padL} x2={W - padR} y1={y(v)} y2={y(v)} stroke="var(--border)" strokeDasharray={i === 0 ? undefined : "2 4"} />
          </g>
        ))}
        {groups.map((bars, i) => {
          const groupW = bars.length * barW + (bars.length - 1) * gap;
          const x0 = padL + i * slot + slot / 2 - groupW / 2;
          const isOpen = open === i;
          const label = `${fmtPeriod(periods[i] ?? "")}: ${summary(i) || t("costs.fig.nothing")}${notes?.get(i) ? ` · ${notes.get(i)}` : ""}${readingsLabel(i) ? ` · ${meter!.name}: ${readingsLabel(i)}` : ""}`;
          return (
            <g key={periods.at(i)} tabIndex={0} role="button" aria-label={label} aria-pressed={pinned === i}
              aria-describedby={isOpen ? tipId : undefined} data-period={periods[i]}
              style={{ outline: "none", cursor: "pointer" }}
              onMouseEnter={() => setHover(i)} onMouseLeave={() => setHover(null)}
              onFocus={() => setHover(i)} onBlur={() => { setHover(null); setPinned((p) => (p === i ? null : p)); }}
              onClick={() => setPinned((p) => (p === i ? null : i))}
              onKeyDown={(e) => {
                if (e.key === "Enter" || e.key === " ") {
                  e.preventDefault();
                  setPinned((p) => (p === i ? null : i));
                } else if (e.key === "Escape") {
                  close();
                }
              }}>
              <rect x={padL + i * slot} y={padT} width={slot} height={plotH} fill={isOpen ? "var(--surface-1)" : "transparent"}
                stroke={isOpen ? "var(--border-strong)" : "none"} />
              {bars.map((b, bi) => {
                const bx = x0 + bi * (barW + gap);
                let acc = 0;
                return (
                  <g key={b.key}>
                    {b.segs.map((g) => {
                      if (g.value <= 0) return null;
                      const top = y(acc + g.value), h = Math.max(1, y(acc) - top);
                      acc += g.value;
                      if (g.dashed) {
                        return <rect key={g.key} x={bx + 0.5} y={top + 0.5} width={Math.max(barW - 1, 1)} height={Math.max(h - 1, 1)} rx={1.5}
                          fill={g.color} stroke={g.dashed} strokeDasharray="2 2" />;
                      }
                      const hv = g.hatch ?? (b.hatched ? 0 : null);
                      return <rect key={g.key} x={bx} y={top} width={barW} height={h} rx={1.5} data-seg={g.key}
                        fill={hv != null ? `url(#${hatchId(g.color, hv, g.hatch != null ? g.ink : undefined)})` : g.color} stroke={hv != null ? g.color : undefined} />;
                    })}
                  </g>
                );
              })}
            </g>
          );
        })}
        {meter && <MeterLine points={meter.points} x={(p) => padL + (p.slot + p.frac) * slot} y={(p) => meterY(p.pct, padT, plotH)} />}
        {/* The labels over the bars and the meter's line, so the line never strikes a value out. The period's group
            beneath still takes the pointer. */}
        <g aria-hidden pointerEvents="none">
          {groups.map((bars, i) => {
            const groupW = bars.length * barW + (bars.length - 1) * gap;
            const x0 = padL + i * slot + slot / 2 - groupW / 2;
            const isOpen = open === i;
            return (
              <g key={periods.at(i)}>
                {bars.map((b, bi) => {
                  const bx = x0 + bi * (barW + gap);
                  const total = sum(b.segs);
                  return (
                    <g key={b.key}>
                      {(isOpen || b.valueLabel) && total > 0 && (
                        // A halo in the page's colour keeps the value legible where it spills over the bar next to it.
                        <text x={bx + barW / 2} y={Math.max(10, y(total) - 4)} textAnchor="middle" fontSize={9.5} fill="var(--text-primary)"
                          stroke="var(--surface-1)" strokeWidth={3} paintOrder="stroke" data-value>{fmtValue(total)}</text>
                      )}
                      {b.tag && total > 0 && (
                        <text x={bx + barW / 2} y={Math.max(10, y(total) - 15)} textAnchor="middle" fontSize={9} fill="var(--text-muted)"
                          stroke="var(--surface-1)" strokeWidth={3} paintOrder="stroke" data-tag>{b.tag}</text>
                      )}
                    </g>
                  );
                })}
              </g>
            );
          })}
        </g>
        {showX && periods.map((p, i) =>
          i % every !== 0 && i !== n - 1 ? null : (
            <text key={p} aria-hidden x={padL + i * slot + slot / 2} y={height - padB + 18} textAnchor="middle" fontSize={11} fill="var(--text-muted)">{fmtPeriod(p)}</text>
          ),
        )}
      </svg>
      {meter && (
        // The weekly meter's own axis, right of the plot. It sticks to the
        // right edge of a chart that scrolls, so the line can be read at any
        // scroll position.
        <svg width={METER_AXIS_W} height={height} aria-hidden data-meter-axis
          style={{ display: "block", flex: "none", position: "sticky", right: 0, background: "var(--surface-2)", borderLeft: "1px solid var(--border)" }}>
          {METER_TICKS.map((v) => (
            <g key={v}>
              <line x1={0} x2={4} y1={meterY(v, padT, plotH)} y2={meterY(v, padT, plotH)} stroke="var(--cost-meter)" />
              <text x={8} y={meterY(v, padT, plotH) + 4} fontSize={11} fill="var(--cost-meter)" data-meter-tick data-pct={v}>{fmtNumber(v, 0)} %</text>
            </g>
          ))}
          <text transform={`translate(${METER_AXIS_W - 8} ${padT + plotH / 2}) rotate(90)`} textAnchor="middle" fontSize={11}
            fill="var(--cost-meter)" data-meter-title>{meter.label}</text>
        </svg>
      )}
      </div>
      {open != null && groups.at(open) && (
        <div id={tipId} role="tooltip" style={{
          position: "absolute", left: tipLeft, top: 4, width: tipW, pointerEvents: "none", zIndex: 5,
          background: "var(--surface-2)", border: "1px solid var(--border-strong)", borderRadius: 8,
          padding: "8px 10px", fontSize: 12, boxShadow: "0 6px 20px var(--shadow-md)",
        }}>
          <div className="muted" style={{ fontSize: 11, marginBottom: 6 }}>{fmtPeriod(periods.at(open) ?? "")}</div>
          {groups.at(open)!.filter((b) => sum(b.segs) > 0).map((b) => (
            <div key={b.key} style={{ marginBottom: 6 }}>
              <div style={{ display: "flex", justifyContent: "space-between", fontWeight: 600, gap: 8 }}>
                <span style={{ display: "inline-flex", alignItems: "center", gap: 6 }}><Swatch color={b.color} hatched={b.hatched} /> {b.label}</span>
                <span>{fmtValue(sum(b.segs))}</span>
              </div>
              {b.tag && <div className="muted" data-tag-detail style={{ paddingLeft: 16 }}>{b.tag}{b.tagDetail ? ` · ${b.tagDetail}` : ""}</div>}
              {b.note && <div className="muted" data-price-check style={{ paddingLeft: 16 }}>{b.note}</div>}
              {b.segs.filter((x) => x.value > 0).length > 1 && b.segs.filter((x) => x.value > 0).map((x) => (
                <div key={x.key} className="muted" style={{ display: "flex", justifyContent: "space-between", gap: 12, paddingLeft: 16 }}>
                  <span style={{ display: "inline-flex", alignItems: "center", gap: 6 }}><Swatch color={x.color} hatched={b.hatched} dashed={x.dashed} hatch={x.hatch} ink={x.ink} /> {x.label}</span>
                  <span>{fmtValue(x.value)}</span>
                </div>
              ))}
            </div>
          ))}
          {notes?.get(open) && <div className="muted" style={{ fontSize: 11 }}>{notesHead}: {notes.get(open)}</div>}
          {meter && readingsAt.get(open) && (
            // The weekly meter's readings of the period, each with its time: "Weekly meter: 18:05 UTC 9 % · 18:50 UTC 10 %".
            <div className="muted" data-meter-readings style={{ fontSize: 11 }}>
              {meter.name}: {readingTexts(readingsAt.get(open)!, meter.fmtAt, t).map((x, k) => (
                <Fragment key={k}>{k > 0 ? " · " : ""}<span style={{ whiteSpace: "nowrap" }}>{x ?? "…"}</span></Fragment>
              ))}
            </div>
          )}
          {groups.at(open)!.every((b) => sum(b.segs) <= 0) && !notes?.get(open) && !readingsAt.get(open) && <div className="muted">{t("costs.fig.nothing")}</div>}
        </div>
      )}
    </div>
  );
}

// Through the shared formatter: the decimal separator of the UI, as in the
// amounts next to them.
const pct = (v: number) => `${v >= 0 ? "+" : ""}${fmtNumber(v, 2)} %`;
const times = (v: number) => (Number.isFinite(v) ? `${fmtNumber(v, 1)}×` : "∞");

/** covey's own price table against the lead figure, in words (#120): one
 *  line where the two differ by more than PRICE_CHECK, none where they
 *  agree. */
export function priceTableLine(t: TFunction, lead: number, table: number): string | undefined {
  const d = priceTableDifference(lead, table);
  if (!d) return undefined;
  return t(d.pct < 0 ? "costs.lead.checkLess" : "costs.lead.checkMore", { usd: fmtUSD(d.usd), pct: fmtNumber(Math.abs(d.pct), Math.abs(d.pct) < 10 ? 1 : 0) });
}

/** The same as a table cell: "+3.2 %" where they differ, nothing where they agree. */
function priceTableCell(lead: number, table: number): string {
  const d = priceTableDifference(lead, table);
  return d ? pct(d.pct) : "";
}

// --- The outside sessions, included by default (#120) ---------------------------

/** The outside sessions on the chart: their per-period figures per source and
 *  the sources switched on; null for "covey only". */
export type OutsideOnChart = {
  ext: ExternalUsage;
  per: Map<string, Map<number, SourcePeriod>>;
  on: ReadonlySet<string>;
  /** Each source's pattern and series, once per answer. */
  styles: SourceStyles;
  /** Each listed source's figures, once per answer. */
  kpis: Map<string, OutsideKpis>;
  /** Each source's real part of the meters. */
  real: ReadonlyMap<string, number>;
};

/** The outside sessions on the chart, computed once per answer (#120 review, L4). */
export function outsideOnChart(ext: ExternalUsage, off: ReadonlySet<string>): OutsideOnChart {
  return {
    ext, per: sourcesPerPeriod(ext), on: new Set(listedSources(ext).map((x) => x.source).filter((n) => !off.has(n))),
    styles: sourceStyles(ext), kpis: outsideKpisBySource(ext), real: sourceRealParts(ext),
  };
}

/** "covey agents (internal) $X · outside sessions $Y", or with an agent
 *  filter "selected agents $X · plus outside sessions $Y": the figure of the
 *  outside sessions' own page, where they are one part. */
export function inclLine(t: TFunction, filtered: boolean, covey: string, outside: string): string {
  return t(filtered ? "costs.incl.splitSelected" : "costs.incl.split", { covey, outside });
}

/** One part of a figure that includes the outside sessions (#163). */
export type SplitLine = { key: "covey" | "operator" | "projects" | "perUse"; text: string; title?: string; recomputed: boolean };

/** A figure that includes the outside sessions, in its parts, named:
 *  covey's own agents (internal), the covey operator (the Claude Code session
 *  driving covey from outside), the other projects on the same machine and
 *  plan, and what is billed per use, on no subscription meter (#166 review, F1). money writes one outside part's figure; recomputed: it holds
 *  calls priced at the list for want of a reported cost (marked, and said
 *  once below the lines). The operator, the projects and the per-use
 *  sources are lines only where they have calls; the projects' and the
 *  per-use line carry each source's figure as their hover. range labels the lines as the range's, where a figure within the
 *  meter's readings stands beside them (#120 review, L5). */
export function splitLines(t: TFunction, o: {
  filtered: boolean; covey: string; split: OutsideSplit; money: (k: OutsideKpis) => string;
  recomputed?: (k: OutsideKpis) => boolean; range?: boolean;
}): SplitLine[] {
  const mark = (k: OutsideKpis) => o.recomputed?.(k) ?? false;
  const first = t(o.filtered ? "costs.incl.lineCoveySelected" : "costs.incl.lineCovey", { usd: o.covey });
  const lines: SplitLine[] = [{ key: "covey", text: o.range ? t("costs.incl.inRange", { line: first }) : first, recomputed: false }];
  const { operator, projects, perUse, projectList, perUseList } = o.split;
  const hover = (list: OutsideSplit["projectList"]) => list.filter((p) => p.kpis.calls > 0).map((p) => `${p.source}: ${o.money(p.kpis)}`).join("\n");
  if (operator && operator.calls > 0) {
    lines.push({ key: "operator", text: t("costs.incl.lineOperator", { usd: o.money(operator) }), recomputed: mark(operator) });
  }
  if (projects && projects.calls > 0) {
    lines.push({ key: "projects", text: t("costs.incl.lineProjects", { usd: o.money(projects) }), recomputed: mark(projects),
      title: hover(projectList) });
  }
  if (perUse && perUse.calls > 0) {
    lines.push({ key: "perUse", text: t("costs.incl.linePerUse", { usd: o.money(perUse) }), recomputed: mark(perUse), title: hover(perUseList) });
  }
  return lines;
}

/** The lines of a figure's parts: one under the other where `stacked` (the
 *  lead figure, one line each), else in a row. */
export function SplitParts({ lines, stacked = false }: { lines: SplitLine[]; stacked?: boolean }) {
  const { t } = useTranslation();
  const mark = (l: SplitLine) => (l.recomputed ? `${l.text} *` : l.text);
  const note = lines.some((l) => l.recomputed);
  // A line's hover is also its accessible description, which the keyboard and
  // touch screens reach where a title is not (#163 review, N6).
  const about = (l: SplitLine) => l.title?.split("\n").join("; ");
  if (!stacked) {
    return <>{lines.map((l, i) => <Fragment key={l.key}>{i > 0 && " · "}<span data-part={l.key} title={l.title} aria-description={about(l)}>{mark(l)}</span></Fragment>)}</>;
  }
  return <>
    {lines.map((l) => <div key={l.key} data-part={l.key} title={l.title} aria-description={about(l)}>{mark(l)}</div>)}
    {note && <div data-part-footnote>* {t("costs.lead.recomputedMark")}</div>}
  </>;
}

/** The lead figure's parts (#120, #163): each outside part at its lead price, unmarked, the mark said once. */
export function leadLines(t: TFunction, filtered: boolean, covey: string, split: OutsideSplit, range = false): SplitLine[] {
  return splitLines(t, { filtered, covey, split, range, money: (k) => outsideMoney(t, k, "lead", false),
    recomputed: (k) => kpiLead(k).recomputed > 0 });
}

/** A sum of parts some of which have no figure: the sum, marked (#120 review, L1). */
export function withUnpriced(t: TFunction, usd: number, unpriced: boolean): string {
  return unpriced ? t("costs.incl.withUnpriced", { usd: fmtUSD(usd) }) : fmtUSD(usd);
}

/** An outside part has no complete figure of this kind: some of its calls
 *  carry none (no reported cost, or no list price). */
export function partUnpriced(o: MoneyPart, kind: "reported" | "list" | "lead"): boolean {
  if (o.calls === 0) return false;
  if (kind === "lead") {
    const l = kpiLead(o);
    return l.lead == null || l.unpricedCalls > 0;
  }
  return kind === "reported" ? o.reported == null || o.reportedCalls < o.calls : o.list == null || o.unpricedTokens > 0;
}

/** What an outside part's money is read from. */
type MoneyPart = Pick<OutsideKpis, "calls" | "list" | "reported" | "reportedCalls" | "unpricedTokens">
  & Partial<Pick<OutsideKpis, "lead" | "leadRecomputed" | "leadUnpricedCalls" | "leadUnpricedTokens">>;

/** The outside sessions' money in a line: their figure, or why there is
 *  none — never $0 where the server has no figure. marked: the lead figure
 *  says "recomputed for sources without a reported cost" itself; false where
 *  the caller says it once for several parts (SplitParts). */
export function outsideMoney(t: TFunction, o: MoneyPart, kind: "reported" | "list" | "lead", marked = true): string {
  if (o.calls === 0) return t("costs.incl.noCalls");
  if (kind === "lead") {
    // At token prices (#120): reported where a call carried it, else the
    // list price, marked; the calls with neither are said, never $0.
    const l = kpiLead(o);
    if (l.lead == null) return l.unpricedTokens > 0 ? t("costs.incl.noListTokens", { n: fmtCount(l.unpricedTokens) }) : t("costs.incl.noList");
    const usd = l.unpricedTokens > 0 ? t("costs.incl.listUnpriced", { usd: fmtUSD(l.lead), n: fmtCount(l.unpricedTokens) }) : fmtUSD(l.lead);
    return marked && l.recomputed > 0 ? t("costs.lead.part", { usd }) : usd;
  }
  if (kind === "reported") {
    if (o.reported == null) return t("costs.incl.noReported");
    return o.reportedCalls > 0 && o.reportedCalls < o.calls
      ? t("costs.incl.reportedOf", { usd: fmtUSD(o.reported), n: fmtNumber(o.reportedCalls, 0), m: fmtNumber(o.calls, 0) })
      : fmtUSD(o.reported);
  }
  if (o.list == null) return o.unpricedTokens > 0 ? t("costs.incl.noListTokens", { n: fmtCount(o.unpricedTokens) }) : t("costs.incl.noList");
  return o.unpricedTokens > 0 ? t("costs.incl.listUnpriced", { usd: fmtUSD(o.list), n: fmtCount(o.unpricedTokens) }) : fmtUSD(o.list);
}

/** An outside list within the meter's readings: its figure, or "no list
 *  price" where none of the seat's outside calls had one — never $0 (#120
 *  review, M3). approx: the figure is an estimate (the shared part, F4), so "≈". */
export function withinMoney(t: TFunction, usd: number, o: { priced: boolean; calls: number; unpricedTokens: number; recomputed?: number }, marked = true, approx = false): string {
  const s = outsideMoney(t, { calls: o.calls, list: o.priced ? usd : null, reported: null, reportedCalls: 0, unpricedTokens: o.unpricedTokens }, "list");
  const shown = approx && o.priced && o.calls > 0 ? `≈ ${s}` : s;
  // At the lead price, a part priced at the list for want of a reported cost says so (#120).
  return marked && o.priced && o.calls > 0 && (o.recomputed ?? 0) > 0 ? t("costs.lead.part", { usd: shown }) : shown;
}

/** The outside part within the meter's readings, split between the covey
 *  operator and the other projects (#163). The server's within-readings
 *  figure is the seat's, not per source, so with both parts present it is
 *  an estimate: shared by each one's lead price over the range (the
 *  operator's rounded to the cent, the projects' the rest: the two always
 *  add up to the whole); with one part, that part has all of it. known:
 *  false where both parts are present but only one has a lead price — the
 *  split is then not known, and the other is not given all of it (#166
 *  review, F4). */
export function withinSplit(total: number, split: OutsideSplit): { operator: number; projects: number; shared: boolean; known: boolean } {
  const lead = (k: OutsideKpis | null) => (k ? kpiLead(k).lead : null);
  const op = split.operator && split.operator.calls > 0, pr = split.projects && split.projects.calls > 0;
  if (!op) return { operator: 0, projects: total, shared: false, known: true };
  if (!pr) return { operator: total, projects: 0, shared: false, known: true };
  const [a, b] = [lead(split.operator), lead(split.projects)];
  // Neither has a price: nothing to share, each part says "no list price". Only one: the split is not known.
  if (a == null && b == null) return { operator: 0, projects: total, shared: false, known: true };
  if (a == null || b == null) return { operator: 0, projects: 0, shared: true, known: false };
  const operator = a + b > 0 ? Math.round(total * (a / (a + b)) * 100) / 100 : 0;
  return { operator, projects: total - operator, shared: true, known: true };
}

/** "not reported yet by: laptop (operator: home) through …; desktop never". */
export function hostsBehindText(t: TFunction, hs: readonly HostBehind[]): string {
  return t("costs.split.hostsBehind", { hosts: hs.map((h) => h.through
    ? t("costs.split.hostThrough", { host: h.host, source: h.source, at: fmtBucket(h.through, "10min") })
    : t("costs.split.hostNever", { host: h.host, source: h.source })).join("; ") });
}

/** One source's figures over the range, from the figures computed once per answer. */
function sourceKpis(s: ExternalSource, o: OutsideOnChart): OutsideKpis {
  return o.kpis.get(s.source) ?? { sources: [s.source], calls: s.calls, list: s.list_usd, reported: s.reported_usd,
    reportedCalls: 0, tokens: {}, unpricedTokens: s.unpriced_tokens };
}

/** The segments of some sources' values, one per series: a source of its
 *  own, or "other sources" for the folded ones (summed, their names in the
 *  tooltip), each in its source's pattern (#120 review, M5). value: null
 *  where the source has no figure in it. */
export function sourceSeriesSegs(t: TFunction, styles: SourceStyles, color: string,
  values: readonly { source: string; value: number | null; calls?: number; marked?: boolean }[],
  col: { keyPrefix: string; column?: (name: string) => string; none?: string; mark?: string }): Seg[] {
  const by = new Map<string, { names: string[]; value: number; none: boolean; marked: boolean }>();
  for (const v of values) {
    const k = styles.series(v.source);
    const e = by.get(k) ?? { names: [], value: 0, none: false, marked: false };
    e.names.push(v.source);
    e.value += v.value ?? 0;
    if (v.value == null && (v.calls ?? 0) > 0) e.none = true;
    if (v.marked) e.marked = true;
    by.set(k, e);
  }
  return [...by.entries()].map(([k, e]): Seg => {
    const other = k === OTHER_SOURCES;
    const st = styles.style(other ? OTHER_SOURCES : k);
    const name = other ? t("costs.incl.otherSources", { n: e.names.length }) : k;
    const label = other ? t("costs.incl.otherSeg", { n: e.names.length, sources: e.names.join(", ") }) : t("costs.incl.seg", { source: k });
    return {
      key: `${col.keyPrefix}${other ? "other" : k}`,
      label: e.marked && col.mark ? `${label} · ${col.mark}` : label,
      color, hatch: st.hatch, ink: st.ink, value: e.value,
      column: col.column?.(name),
      none: e.none && e.value === 0 ? col.none : undefined,
    };
  });
}

/** The bars' segments of the given sources in one period: their reported
 *  cost or their list price, hatched per source, each with a column of its
 *  own in the table and "no list price" (never $0) where it has none. */
function outsideSegs(t: TFunction, o: OutsideOnChart, sources: readonly ExternalSource[], at: number, kind: "reported" | "list" | "lead"): Seg[] {
  return sourceSeriesSegs(t, o.styles, kind === "list" ? SOURCE_COLOR.recomputed : SOURCE_COLOR.reported,
    sources.map((s) => {
      const e = o.per.get(s.source)?.get(at);
      const value = kind === "lead" ? e?.lead : kind === "reported" ? e?.reported : e?.list;
      return { source: s.source, value: value ?? null, calls: e?.calls ?? 0, marked: kind === "lead" && (e?.leadRecomputed ?? 0) > 0 };
    }), {
      keyPrefix: "out:",
      column: (name) => t(kind === "lead" ? "costs.lead.col" : kind === "reported" ? "costs.incl.colReported" : "costs.incl.colList", { source: name }),
      none: t(kind === "reported" ? "costs.incl.noReported" : "costs.incl.noList"),
      mark: t("costs.lead.recomputedMark"),
    });
}

/** The outside list at covey's price table in one period, for the check. */
function outsideTable(o: OutsideOnChart, sources: readonly ExternalSource[], at: number): number {
  return sources.reduce((a, s) => a + (o.per.get(s.source)?.get(at)?.list ?? 0), 0);
}

/** "$200/mo plan" — the plan as the server stores it. */
function usePlanLabel() {
  const { t } = useTranslation();
  return (f: CredentialFigures) => {
    if (!f.plan) return f.kind === "subscription" ? t("costs.fig.noPlan") : "";
    const p = f.plan;
    const price = p.currency.toUpperCase() === "USD" ? fmtUSD(p.amount) : `${fmtNumber(p.amount, p.amount % 1 ? 2 : 0)} ${p.currency}`;
    return t("costs.fig.planLabel", { price, per: t(p.period === "year" ? "costs.fig.perYear" : "costs.fig.perMonth") });
  };
}

/** "real since <baseline>, last reading <last>": the span the real figure
 *  covers. Undefined without a meter. */
function realSpan(t: TFunction, f: CredentialFigures, key = "costs.fig.realSince"): string | undefined {
  if (!f.meter) return undefined;
  return t(key, { from: fmtBucket(f.meter.baseline_at, "hour"), to: fmtBucket(f.meter.last_at, "hour") });
}

/** The tiles of one credential. */
function CredentialTiles({ f, filtered, narrowed, out, split, seatSplit, full }: {
  f: CredentialFigures; filtered: boolean; out: OutsideKpis | null;
  /** The same sources in two parts: the covey operator and the other projects (#163). */
  split: OutsideSplit | null;
  /** Every source on the seat in the same two parts, the chips ignored: the everything tile is the whole seat's. */
  seatSplit: OutsideSplit | null;
  /** The page is narrowed to agents: an org-wide figure says so. */
  narrowed: boolean;
  /** Claude Code's figure and covey's price table as two tiles (the toggle); else the one lead tile. */
  full: boolean;
}) {
  const { t } = useTranslation();
  // With the outside sessions on this seat included, each figure names its
  // parts — the range's, where the multiple of everything on the meter
  // stands beside them with the parts within the meter's readings (L5).
  const range = f.everything != null;
  // One list-price figure leads (#120): Claude Code's price of each call,
  // covey's list price where it reported none; covey's price table is a
  // check under it. The toggle shows the two as tiles of their own.
  const lead = f.reported + (out ? kpiLead(out).lead ?? 0 : 0);
  const tiles: React.ReactNode[] = full ? [
    <Tile key="rep" label={t("costs.fig.reported")} value={fmtUSD(f.reported + (out?.reported ?? 0))} sub={t("costs.fig.reportedSub")} color={SOURCE_COLOR.reported}
      parts={split ? <SplitParts lines={splitLines(t, { filtered, covey: fmtUSD(f.reported), split, range, money: (k) => outsideMoney(t, k, "reported") })} /> : undefined} />,
    <Tile key="rec" label={t("costs.fig.recomputed")} value={fmtUSD(f.recomputed + (out?.list ?? 0))}
      sub={f.check != null ? t("costs.fig.priceTableCheck", { pct: pct(f.check) }) : undefined} color={SOURCE_COLOR.recomputed}
      parts={split ? <SplitParts lines={splitLines(t, { filtered, covey: fmtUSD(f.recomputed), split, range, money: (k) => outsideMoney(t, k, "list") })} /> : undefined} />,
  ] : [
    <Tile key="lead" label={t("costs.lead.label")} value={fmtUSD(lead)} sub={t("costs.lead.sub")} color={SOURCE_COLOR.reported}
      parts={split ? <SplitParts stacked lines={leadLines(t, filtered, fmtUSD(f.reported), split, range)} /> : undefined}
      check={priceTableLine(t, lead, f.recomputed + (out?.list ?? 0))} />,
  ];
  // A seat billed by usage all through the window (0151): its meter is not
  // its money, the usage charges are (below). One that changed basis keeps
  // the real figure of the seat's part, as the server priced it.
  const usageOnly = f.real == null && f.usage != null && f.usage.regimes.length === 1 && f.usage.regimes[0] === "usage";
  if (isSeat(f) && !usageOnly) {
    const r = f.real;
    // Points read under the usage basis: not the seat's, said beside it.
    const seatPart = r && r.usagePoints > 0 ? ` · ${t("costs.basis.realSeatPart", { n: fmtNumber(r.usagePoints, 0) })}` : "";
    // The price per point the server priced the movement at, on average.
    const pp = r && r.points > 0 ? r.value / r.points : null;
    // The real figure covers the meter's span, which can begin days before
    // the window: say so wherever it (or a share of it) stands.
    const span = realSpan(t, f);
    if (r && !r.priced) {
      // Points without a plan price: no figure, never "≈ $0.00".
      tiles.push(<Tile key="real" label={t("costs.fig.realEstimate")} value="–"
        sub={r.unpricedPoints > 0 ? t("costs.fig.noPlanPricePoints", { n: r.unpricedPoints, m: r.points }) : t("costs.fig.noPlan")}
        color={SOURCE_COLOR.real} hatched />);
    } else if (filtered && r) {
      // With outside sessions on the meter, of covey's part only, the
      // server's per-chunk estimate (F3c); without a split step, at most
      // this much (the meter may carry outside use nothing reports).
      const known = f.agents.filter((x) => x.realShare != null);
      const share = known.reduce((a, x) => a + x.realShare!, 0);
      // A part is an estimate (≈); only a multiple is a bound (≥) (L3).
      const value = known.length === 0 ? "–" : `≈ ${fmtUSD(share)}`;
      const sub = known.length === 0 ? t("costs.split.agentsUnsplit")
        : f.split ? t("costs.split.shareOfCoveySub", { usd: fmtUSD(f.split.covey_real_usd), whole: fmtUSD(r.value) })
        : t("costs.fig.shareOfRealSub", { usd: fmtUSD(r.value) });
      tiles.push(<Tile key="real" label={t("costs.fig.shareOfReal")} value={value}
        sub={sub} span={span} note={t("costs.fig.accountWideCaveat")} color={SOURCE_COLOR.real} hatched />);
    } else if (r && r.below) {
      tiles.push(<Tile key="real" label={t("costs.fig.realEstimate")} value={fmtUSDBelow(r.high)}
        sub={t("costs.fig.realBelow") + seatPart} span={span} note={t("costs.fig.accountWideCaveat")} color={SOURCE_COLOR.real} hatched />);
    } else if (r) {
      tiles.push(<Tile key="real" label={t("costs.fig.realEstimate")} value={`≈ ${fmtUSD(r.value)}`}
        sub={t("costs.fig.realRange", { low: fmtUSD(r.low), high: fmtUSD(r.high), pts: r.points, pp: pp != null ? fmtUSDFixed(pp, 4) : "–" }) + seatPart}
        span={span} note={t("costs.fig.accountWideCaveat")} color={SOURCE_COLOR.real} hatched />);
    } else {
      tiles.push(<Tile key="real" label={t("costs.fig.realEstimate")} value="–" sub={t("costs.fig.noMeter")} color={SOURCE_COLOR.real} hatched />);
    }
    if (f.everything) {
      // Everything on this meter: covey's agents, the outside sessions and
      // the unattributed movement, a lower bound (#120).
      const e = f.everything;
      if (e.multiple && e.estimate != null) {
        // The outside part in its two parts (#163), each marked where it
        // holds recomputed calls; said once, in brackets, after the parts.
        const within = seatSplit ? withinSplit(e.outsideList, seatSplit) : null;
        const kinds = seatSplit ? ([["operator", seatSplit.operator, within!.operator], ["projects", seatSplit.projects, within!.projects]] as const)
          .filter(([, k]) => k && k.calls > 0) : [];
        const money = (usd: number, k: OutsideKpis) => withinMoney(t, usd, { priced: e.outsidePriced && kpiLead(k).lead != null, calls: k.calls,
          unpricedTokens: kpiLead(k).unpricedTokens, recomputed: kpiLead(k).recomputed }, false, within!.shared);
        // Both parts but no split to give (one has no price): one line for the two, said as not known.
        const outsideLines = within && !within.known
          ? [t("costs.incl.lineOutsideUnsplit", { usd: fmtUSD(e.outsideList) })]
          : kinds.map(([key, k, usd]) => t(key === "operator" ? "costs.incl.lineOperator" : "costs.incl.lineProjects", { usd: money(usd, k!) }));
        const recomputed = kinds.some(([, k]) => kpiLead(k!).recomputed > 0);
        const parts = [t(filtered ? "costs.incl.lineCoveySelected" : "costs.incl.lineCovey", { usd: fmtUSD(e.coveyList) }), ...outsideLines].join(" · ")
          + (recomputed ? ` (${t("costs.lead.recomputedMark")})` : "");
        tiles.push(<Tile key="mult" label={t("costs.incl.everything")} value={`≈ ${times(e.estimate)}`} orgWide={narrowed}
          parts={t("costs.incl.everythingFloor", { floor: times(e.multiple.low) })}
          sub={t("costs.incl.everythingSub", { list: fmtUSD(e.coveyList + e.outsideList), parts, real: fmtUSD(e.real) })}
          span={span} note={t("costs.incl.everythingNote", { slack: fmtUSD(e.slack) }) + (within?.shared && within.known ? ` ${t("costs.incl.everythingShared")}` : "")} />);
      }
    } else if (f.multiple?.kind === "estimate") {
      tiles.push(<Tile key="mult" label={t("costs.fig.multiple")} value={`≈ ${times(f.multiple.value)}`}
        sub={t("costs.fig.multipleRange", { low: times(f.multiple.low), high: times(f.multiple.high) })}
        span={span} note={t("costs.fig.multipleHint")} />);
    } else if (f.multiple?.kind === "floor") {
      tiles.push(<Tile key="mult" label={t("costs.fig.multiple")} value={`≥ ${times(f.multiple.low)}`}
        sub={t("costs.split.multipleFloor")} span={span} note={t("costs.fig.multipleHint")} />);
    }
  }
  if (f.kind === "api_key" && f.billed) {
    const b = f.billed;
    const status = b.status.state === "complete" ? t("costs.fig.billedComplete")
      : b.status.completeTo ? t("costs.fig.billedCompleteTo", { time: fmtClockUTC(b.status.completeTo) }) : t("costs.fig.billedPending");
    if (filtered) {
      tiles.push(<Tile key="billed" label={t("costs.fig.billed")} value="–" sub={t("costs.fig.filteredRealNote")} color={SOURCE_COLOR.billed} />);
    } else if (b.state !== "measured") {
      // No bill could be read: say why, never "$0.00".
      const why = b.state === "unconfigured" ? t("costs.fig.billedUnconfigured")
        : b.state === "unmapped" ? t("costs.fig.billedUnmapped")
        : b.state === "failed" ? t("costs.fig.billedSyncFailed", { error: b.error || "–" })
        : b.state === "not_landed" ? t("costs.fig.billedNotLanded")
        : t("costs.fig.billedUnavailable");
      tiles.push(<Tile key="billed" label={t("costs.fig.billed")} value="–" sub={why} color={SOURCE_COLOR.billed} />);
    } else {
      // The bill covers whole billed buckets; the reported and recomputed
      // figures of the same window stand beside it, not the page's — up to
      // compared_until, where the bill is complete, not up to now. Without a
      // date it would read as today, so a day other than the window's end
      // gets one.
      const w = b.window;
      const until = w && (w.comparedUntil.toISOString().slice(0, 10) === w.end.toISOString().slice(0, 10)
        ? fmtClockUTC(w.comparedUntil) : fmtBucket(w.comparedUntil.toISOString(), "hour"));
      const win = w ? t("costs.fig.billedWindow", {
        from: fmtBucket(w.start.toISOString(), "hour"),
        unit: t(w.width === "1h" ? "costs.fig.billedUnitHours" : "costs.fig.billedUnitDays"),
        reported: fmtUSD(b.reported), list: fmtUSD(b.recomputed), until,
      }) : undefined;
      const failed = b.error ? t("costs.fig.billedLastSyncFailed", { error: b.error }) : undefined;
      tiles.push(<Tile key="billed" label={t("costs.fig.billed")} value={fmtUSD(b.usd)} sub={status}
        note={[win, failed].filter(Boolean).join(" · ") || undefined} color={SOURCE_COLOR.billed} />);
      tiles.push(<Tile key="list" label={t("costs.fig.listPerAnthropic")} value={fmtUSD(b.listUsd)} sub={status} />);
      tiles.push(<Tile key="ratio" label={t("costs.fig.billedPerList")} value={b.ratio != null ? `${fmtNumber(b.ratio * 100, 1)} %` : "–"}
        sub={t("costs.fig.billedPerListSub")} />);
    }
  }
  // Usage charges (0151): each currency in its own, a dollar figure only
  // where every counted charge has one, never added across currencies.
  // Shown wherever a charge counted in the window — whatever the basis at
  // its end — and under a usage basis also when none did.
  const u = f.usage;
  if (u && (u.currencies.length > 0 || f.basis === "usage" || f.basis === "seat_and_usage")) {
    if (filtered) {
      tiles.push(<Tile key="usage" label={t("costs.basis.usageTile")} value="–" sub={t("costs.basis.usageFiltered")} color={SOURCE_COLOR.real} />);
    } else if (u.currencies.length === 0) {
      tiles.push(<Tile key="usage" label={t("costs.basis.usageTile")} value="–" sub={t("costs.basis.noCharge")} color={SOURCE_COLOR.real} />);
    } else {
      for (const c of u.currencies) {
        tiles.push(<Tile key={`usage-${c.currency}`} label={t("costs.basis.usageTile")} value={fmtMoney(c.counted, c.currency)}
          sub={usageSub(t, c)}
          color={SOURCE_COLOR.real} />);
      }
      // Seat and usage together: one figure only where both are in dollars.
      if ((f.basis === "seat_and_usage" || u.changed) && f.real?.priced && !f.real.below && u.usd != null) {
        tiles.push(<Tile key="both" label={t("costs.basis.bothTile")} value={`≈ ${fmtUSD(f.real.value + u.usd)}`}
          sub={t("costs.basis.bothSub", { seat: fmtUSD(f.real.value), usage: fmtUSD(u.usd) })} color={SOURCE_COLOR.real} hatched />);
      }
    }
    if (f.usageMultiple != null) {
      tiles.push(<Tile key="umult" label={t("costs.fig.multiple")} value={`${f.usageShared ? "≥ " : ""}${times(f.usageMultiple)}`}
        sub={t("costs.basis.usageMultipleSub", { list: fmtUSD(f.usageList ?? 0) })
          + (f.usageShared ? ` · ${t("costs.basis.usageMultipleShared", { sources: f.usageShared.join(", ") })}` : "")} />);
    }
  }
  return <div className="flex flex-wrap gap-3 mb-2">{tiles}</div>;
}

/** Why there is no multiple, in words. */
function NoMultiple({ f }: { f: CredentialFigures }) {
  const { t } = useTranslation();
  if (f.everything && f.everything.why !== "usage") {
    // The multiple of everything on the meter replaces covey's own. On a
    // seat billed by usage (0151) the usage multiple's rules stand instead.
    const e = f.everything;
    if (!e.why) return null;
    const text = e.why === "movement" ? t("costs.incl.everythingMovement", { pts: fmtNumber(e.points, 0) })
      : e.why === "unpriced" ? t("costs.incl.everythingUnpriced", { n: fmtNumber(e.unpricedPoints, 0) })
      : t("costs.incl.everythingNoList");
    return <p className="muted text-xs mb-2 mt-0" data-no-multiple data-everything-why={e.why}>{text}</p>;
  }
  if (f.kind === "api_key" || f.noMultiple == null || f.noMultiple === "meter") return null;
  let text: string;
  if (f.noMultiple === "agents") text = t("costs.fig.noMultipleAgents");
  else if (f.noMultiple === "outside") text = t("costs.split.noMultipleOutside");
  else if (f.noMultiple === "rate") text = t("costs.basis.noMultipleRate");
  else if (f.noMultiple === "noCharge") text = t("costs.basis.noMultipleNoCharge");
  else if (f.noMultiple === "spanList") text = t("costs.basis.noMultipleSpanList");
  else if (f.noMultiple === "basisChanged") text = t("costs.basis.noMultipleChanged");
  else if (f.noMultiple === "seatAndUsage") text = t("costs.basis.noMultipleBoth");
  else if (f.noMultiple === "unpriced") {
    text = f.real && f.real.unpricedPoints > 0
      ? t("costs.fig.noMultipleUnpriced", { n: f.real.unpricedPoints, m: f.real.points })
      : t("costs.fig.noMultipleNoPlan");
  } else if (f.real && f.real.points >= 1 && f.aligned) {
    const low = f.aligned / f.real.high, high = f.real.low > 0 ? f.aligned / f.real.low : Infinity;
    text = t("costs.fig.notEnoughMovement", { pts: f.real.points, low: times(low), high: times(high) });
  } else text = t("costs.fig.notEnoughMovementNone");
  return <p className="muted text-xs mb-2 mt-0" data-no-multiple>{text}</p>;
}

/** One credential: its tiles, its chart, its agents. */
function CredentialBlock({ f, data, billed, readings, canSetPlan, bucket, scrollKey, fmtPeriod, selected, src, split, types, layout, view, sharedScale, asTable, outside, full }: {
  f: CredentialFigures; data: CostCells; billed: BilledReport | undefined; readings: readonly UsageReading[]; canSetPlan: boolean; bucket: string; scrollKey?: string; fmtPeriod: (p: string) => string;
  selected: Set<string>; src: Record<Source, boolean>; split: Split; types: Set<string>; layout: Layout; view: View; sharedScale: boolean; asTable: boolean;
  /** The full view of covey's price table (the toggle): Reported and Recomputed apart. */
  full: boolean;
  /** The outside sessions, included (null: covey only). */
  outside: OutsideOnChart | null;
}) {
  const { t } = useTranslation();
  const planLabel = usePlanLabel();
  const filtered = selected.size > 0;
  const inScope = (id: string) => !filtered || selected.has(id);
  const agents = useMemo(() => rankAgents(data.agents), [data.agents]);
  const own = useMemo(() => data.cells.filter((c) => credId(c.secret_key, c.secret_slot) === f.id), [data.cells, f.id]);
  // The outside sources on this seat's meter that are switched on (#120):
  // stacked on the reported and recomputed bars, hatched per source.
  const mine = useMemo(() => (outside && f.secretKey != null
    ? seatSources(outside.ext, f.secretKey, f.slot!).filter((s) => outside.on.has(s.source)) : []), [outside, f.secretKey, f.slot]);
  const mineKpis = useMemo(() => (outside && mine.length > 0 ? outsideKpis(outside.ext, new Set(mine.map((s) => s.source))) : null), [outside, mine]);
  const mineSplit = useMemo(() => (outside && mine.length > 0 ? outsideSplit(outside.ext, new Set(mine.map((s) => s.source))) : null), [outside, mine]);
  // The everything tile is the whole seat's, whatever the chips say (as the
  // meter card's is): its parts are over every source on the seat, so they add
  // up to its figure with chips off (#163 review, M1).
  const seatSplit = useMemo(() => (outside && f.secretKey != null
    ? outsideSplit(outside.ext, new Set(seatSources(outside.ext, f.secretKey, f.slot!).map((s) => s.source))) : null), [outside, f.secretKey, f.slot]);
  const steps = useMemo(() => (f.meter ? (data.subscription ?? []).filter((s) => s.secret_key === f.meter!.secret_key
    && s.secret_slot === f.meter!.secret_slot && s.source === f.meter!.source && s.window_kind === f.meter!.window_kind) : []), [data.subscription, f.meter]);
  // The bill inside the tile's window, in the chart's buckets.
  const bill = useMemo(() => (f.kind === "api_key" && f.secretKey != null
    ? billedPerPeriod(billed, f.secretKey, f.slot!, bucket) : new Map<number, number>()), [billed, f.kind, f.secretKey, f.slot, bucket]);
  // The days of that window the bill is read for as a whole: no hour or
  // 10-hour bar holds them, so the bars fall short of the tile by this much.
  const billWhole = f.kind === "api_key" && f.secretKey != null ? billedWholeDays(billed, f.secretKey, f.slot!, bucket) : 0;

  // The periods: the cells', the meter's, the bill's — matched by instant.
  const periods = useMemo(() => {
    const m = new Map<number, string>();
    for (const c of own) m.set(Date.parse(c.period), c.period);
    for (const s of steps) if (!m.has(Date.parse(s.period))) m.set(Date.parse(s.period), s.period);
    for (const at of bill.keys()) if (!m.has(at)) m.set(at, new Date(at).toISOString());
    for (const s of mine) for (const [at, e] of outside!.per.get(s.source) ?? []) if (!m.has(at)) m.set(at, e.period);
    return [...m.entries()].sort((a, b) => a[0] - b[0]).map((e) => e[1]);
  }, [own, steps, bill, mine, outside]);
  const idx = useMemo(() => new Map(periods.map((p, i) => [Date.parse(p), i])), [periods]);

  // The cost-type filter belongs to the split by type, and its chips are
  // only on screen then: switched off (or per agent), every type counts.
  const byType = full && split === "type" && layout === "combined";
  const typeList = byType ? COST_TYPES.filter((k) => types.has(k)) : COST_TYPES;
  // Real bars only where every point has a plan price: a bar of the priced
  // part alone would read as the whole. At an hour a bar is the whole points
  // read in it since the previous reading (readings come when an agent
  // wakes), as the server priced them: 0 without a reading.
  const showReal = src.real && !filtered && f.kind !== "api_key" && f.meter != null && f.real?.priced === true;
  const vs = view === "vs" && layout === "combined" && showReal;
  const showBilled = src.billed && !filtered && f.kind === "api_key";
  const realPer = useMemo(() => {
    const v = new Array<number>(periods.length).fill(0);
    // The server priced each reading at the plan valid when it was read.
    for (const s of steps) {
      const i = idx.get(Date.parse(s.period));
      if (i != null) v[i] += s.usd ?? 0;
    }
    return v;
  }, [steps, idx, periods.length]);
  // Once sessions outside covey report on the meters (#120), the real bar is
  // split per reading by list-price share, each part where its reading
  // falls: covey's agents, the outside sessions, and the movement of
  // readings they did not report — split to nobody, never booked to covey
  // whole.
  const splitPer = useMemo(() => {
    if (!f.split || f.secretKey == null) return null;
    const by = splitPerPeriod(data.external, f.secretKey, f.slot!, bucket);
    return periods.map((p) => by.get(Date.parse(p)) ?? EMPTY_SPLIT_PERIOD);
  }, [f.split, f.secretKey, f.slot, data.external, bucket, periods]);
  // The unsplit part's label, by why its readings were not split: n and m
  // are the numbers as shown.
  const unsplitLabel = (sp: SplitPeriod) => {
    const unpriced = sp.unpricedSteps, notReported = sp.unsplitSteps - sp.unpricedSteps;
    if (unpriced === 0) return t("costs.split.unsplitReadings", { n: fmtNumber(notReported, 0) });
    if (notReported === 0) return t("costs.split.unsplitUnpricedReadings", { n: fmtNumber(unpriced, 0) });
    return t("costs.split.unsplitBoth", { n: fmtNumber(notReported, 0), m: fmtNumber(unpriced, 0) });
  };
  // Included, the outside part of the real bar is split per source (the
  // server's source_parts); covey only, it is one part, as before.
  const realBySource = useMemo(() => (outside && f.split && f.secretKey != null
    ? sourceRealPerPeriod(outside.ext, f.secretKey, f.slot!, bucket) : null), [outside, f.split, f.secretKey, f.slot, bucket]);
  const realSegs = (i: number): Seg[] => {
    const sp = splitPer?.[i];
    if (!sp) return [{ key: "real", label: t("costs.fig.realEstimate"), color: SOURCE_COLOR.real, value: realPer[i] }];
    const bySource = realBySource?.get(Date.parse(periods[i]));
    // Per source in its own pattern; a source the page does not list, or one
    // folded away, in "other sources" — never another source's (M4, M5).
    const outsideParts: Seg[] = bySource && bySource.size > 0
      ? sourceSeriesSegs(t, outside!.styles, SPLIT_COLOR.outside,
        [...bySource.entries()].sort((a, b) => a[0].localeCompare(b[0])).map(([source, value]) => ({ source, value })), { keyPrefix: "outside:" })
      : [{ key: "outside", label: t("costs.split.outside"), color: SPLIT_COLOR.outside, hatch: SPLIT_HATCH.outside, value: sp.outside }];
    return [
      { key: "covey", label: t("costs.split.covey"), color: SPLIT_COLOR.covey, value: sp.covey },
      ...outsideParts,
      // Neither covey's agents nor the reported sessions explain it (F3c):
      // booked to nobody.
      { key: "unattributed", label: t("costs.split.unattributed"), color: SPLIT_COLOR.unattributed, hatch: SPLIT_HATCH.unattributed, value: sp.unattributed },
      { key: "unsplit", label: unsplitLabel(sp),
        color: SPLIT_COLOR.unsplit, dashed: SPLIT_COLOR.unsplitEdge, value: sp.unsplit },
    ];
  };
  // The meter's steps of each period: a period's multiple needs its points.
  const stepsPer = useMemo(() => {
    const v = periods.map(() => [] as typeof steps);
    for (const s of steps) {
      const i = idx.get(Date.parse(s.period));
      if (i != null) v[i].push(s);
    }
    return v;
  }, [steps, idx, periods]);
  const billedPer = useMemo(() => {
    const v = new Array<number>(periods.length).fill(0);
    for (const [at, usd] of bill) {
      const i = idx.get(at);
      if (i != null) v[i] += usd;
    }
    return v;
  }, [bill, idx, periods.length]);
  // The bill per hour begins at hourly_from: at hour and 10 h a period
  // that starts before it has no bar or an incomplete one, and says so.
  const hourlyFrom = showBilled && (bucket === "hour" || bucket === "10hour") && billed?.hourly_from ? Date.parse(billed.hourly_from) : null;
  const billedShort = hourlyFrom != null && periods.some((p) => Date.parse(p) < hourlyFrom);
  const notes = useMemo(() => {
    if (f.kind !== "api_key" || hourlyFrom == null) return undefined;
    const m = new Map<number, string>();
    periods.forEach((p, i) => { if (Date.parse(p) < hourlyFrom) m.set(i, t("costs.fig.billedBeforeHourly")); });
    return m;
  }, [f.kind, t, hourlyFrom, periods]);

  // The bar's label says when it carries the outside sessions too.
  const withOut = (label: string, on: boolean) => (on && mine.length > 0
    ? t(filtered ? "costs.incl.barInclSelected" : "costs.incl.barIncl", { label }) : label);
  // The lead bar (#120): covey's lines at Claude Code's figure, each
  // outside source at its lead price, and covey's price table in a line
  // under it where the two differ (PRICE_CHECK).
  const leadBar = (cs: CostCell[], i: number, withMoney: boolean): Bar => {
    const at = Date.parse(periods[i]);
    const outSegs = withMoney && outside ? outsideSegs(t, outside, mine, at, "lead") : [];
    const segs: Seg[] = [{ key: "r", label: t("costs.lead.coveyPart"), color: SOURCE_COLOR.reported, value: cs.reduce((a, c) => a + leadOf(c), 0) }, ...outSegs];
    const table = cs.reduce((a, c) => a + recomputedOf(c), 0) + (withMoney && outside ? outsideTable(outside, mine, at) : 0);
    return { key: "reported", label: withOut(t("costs.lead.label"), withMoney), color: SOURCE_COLOR.reported, segs, note: priceTableLine(t, sum(segs), table) };
  };
  // The weekly meter as a line: a subscription's readings, by the server's
  // weeks. Not an API key's, and not in the per-agent charts: the meter
  // belongs to the credential, not to an agent. Not at 10 minutes either:
  // the meter is read per hour (costs.outside.tenMinNoMeter, as Real and
  // Billed), so every reading there would be a gap and the chart dots only.
  const meterLine = useMemo(() => {
    if (!f.meter || f.kind === "api_key" || bucket === "10min") return undefined;
    const points = meterPoints(readings, f.meter, periods, bucket);
    return points.length > 0 ? points : undefined;
  }, [f.meter, f.kind, readings, periods, bucket]);
  // One object per change of its inputs, so the chart's per-period readings
  // are not rebuilt on every hover or focus.
  const meterProp = useMemo(() => meterLine && {
    points: meterLine,
    label: t("costs.fig.meterLine"),
    name: t("costs.fig.meterName"),
    // A week, a month or 10 hours can span days (20:00 to 06:00): the reading names its day.
    fmtAt: (iso: string) => (bucket === "week" || bucket === "month" || bucket === "10hour" ? fmtBucket(iso, "hour") : fmtClockUTC(iso)),
  }, [meterLine, bucket, t]);

  const barsFor = (cs: CostCell[], i: number, withMoney: boolean): Bar[] => {
    const out: Bar[] = [];
    const at = Date.parse(periods[i]);
    const outSegs = (kind: "reported" | "list") => (withMoney && outside ? outsideSegs(t, outside, mine, at, kind) : []);
    if (vs && withMoney) {
      // The list bar and the real bar of the period, side by side; the real
      // value stands on its bar and the period's multiple above it. The
      // reported figure is not part of this comparison. The multiple is of
      // every cost type, as the tile's is: the meter crossed its points for
      // all of them, so a type switched off only shortens the list bar.
      // A period is a few points of ±1 each: it gets a multiple by the
      // tile's rule only (from 3 points, its range in the details).
      const list = cs.reduce((a, c) => a + recomputedOf(c, typeList), 0);
      const listAll = cs.reduce((a, c) => a + recomputedOf(c), 0);
      const lead = cs.reduce((a, c) => a + leadOf(c), 0);
      // With outside sessions on the meter, covey's list over the whole
      // movement is no multiple: the headline has the split ones. The
      // multiple is of the bar drawn: the lead figure, or covey's price
      // table in the full view.
      const m = f.split || f.outsideUnreported ? null : periodMultiple(full ? listAll : lead, stepsPer[i]);
      if (full) {
        const segs = byType
          ? typeList.map((k) => ({ key: k, label: t(`costs.stack.${k}`), color: TYPE_COLOR[k], value: cs.reduce((a, c) => a + (c.measured[k] ?? 0), 0) }))
          : [{ key: "m", label: t("costs.fig.recomputed"), color: SOURCE_COLOR.recomputed, value: list }];
        out.push({ key: "recomputed", label: withOut(t("costs.fig.recomputed"), withMoney), color: SOURCE_COLOR.recomputed, segs: [...segs, ...outSegs("list")] });
      } else {
        out.push(leadBar(cs, i, withMoney));
      }
      out.push({ key: "real", label: t("costs.fig.realEstimate"), color: SOURCE_COLOR.real, hatched: true, valueLabel: true,
        tag: m?.kind === "estimate" ? `≈ ${times(m.value)}` : undefined,
        tagDetail: m ? t("costs.fig.multipleRange", { low: times(m.low), high: times(m.high) }) : undefined,
        segs: realSegs(i) });
      return out;
    }
    if (src.reported && !full) {
      out.push(leadBar(cs, i, withMoney));
    } else if (src.reported) {
      out.push({ key: "reported", label: withOut(t("costs.fig.reported"), withMoney), color: SOURCE_COLOR.reported,
        segs: [{ key: "r", label: t("costs.fig.reported"), color: SOURCE_COLOR.reported, value: cs.reduce((a, c) => a + (c.engine_usd ?? 0), 0) },
          ...outSegs("reported")] });
    }
    if (src.recomputed && full) {
      const segs = byType
        ? typeList.map((k) => ({ key: k, label: t(`costs.stack.${k}`), color: TYPE_COLOR[k], value: cs.reduce((a, c) => a + (c.measured[k] ?? 0), 0) }))
        : [{ key: "m", label: t("costs.fig.recomputed"), color: SOURCE_COLOR.recomputed, value: cs.reduce((a, c) => a + recomputedOf(c, typeList), 0) }];
      out.push({ key: "recomputed", label: withOut(t("costs.fig.recomputed"), withMoney), color: SOURCE_COLOR.recomputed, segs: [...segs, ...outSegs("list")] });
    }
    if (withMoney && showReal) {
      out.push({ key: "real", label: t("costs.fig.realEstimate"), color: SOURCE_COLOR.real, hatched: true, segs: realSegs(i) });
    }
    if (withMoney && showBilled) {
      out.push({ key: "billed", label: t("costs.fig.billed"), color: SOURCE_COLOR.billed,
        segs: [{ key: "billed", label: t("costs.fig.billed"), color: SOURCE_COLOR.billed, value: billedPer[i] }] });
    }
    return out;
  };
  const cellsAt = (i: number, agent?: string) => own.filter((c) => Date.parse(c.period) === Date.parse(periods[i])
    && (agent ? c.agent_id === agent : inScope(c.agent_id)));
  const combined = periods.map((_, i) => barsFor(cellsAt(i), i, true));
  const perAgent = f.agents.filter((a) => a.recomputed > 0 || a.reported > 0).map((a) => ({
    a, groups: periods.map((_, i) => barsFor(cellsAt(i, a.id), i, false)),
  }));
  // In the per-agent layout each outside source is a row of its own, marked.
  const perSource = layout === "agents" && outside ? mine.map((s) => ({
    s, groups: periods.map((_, i): Bar[] => {
      const at = Date.parse(periods[i]);
      const bars: Bar[] = [];
      if (src.reported && !full) {
        const segs = outsideSegs(t, outside, [s], at, "lead");
        bars.push({ key: "reported", label: t("costs.lead.label"), color: SOURCE_COLOR.reported, segs,
          note: priceTableLine(t, sum(segs), outsideTable(outside, [s], at)) });
      } else if (src.reported) bars.push({ key: "reported", label: t("costs.fig.reported"), color: SOURCE_COLOR.reported, segs: outsideSegs(t, outside, [s], at, "reported") });
      if (src.recomputed && full) bars.push({ key: "recomputed", label: t("costs.fig.recomputed"), color: SOURCE_COLOR.recomputed, segs: outsideSegs(t, outside, [s], at, "list") });
      return bars;
    }),
  })) : [];
  const sharedMax = Math.max(0, ...[...perAgent, ...perSource].flatMap((r) => r.groups.flatMap((g) => g.map((b) => sum(b.segs)))));
  const name = f.secretKey == null ? t("costs.fig.notAttributable") : `${f.secretKey} #${f.slot}`;
  const kind = t(`costs.fig.kind_${f.kind || "unknown"}`);
  const plan = planLabel(f);
  const subscription = isSeat(f);
  // The seat's split totals, split steps or not; of its unsplit readings,
  // the ones the outside sessions had not reported (the rest had outside
  // calls with no price at all).
  const seatTotal = f.secretKey == null ? null : meterSplit(data.external, f.secretKey, f.slot!);
  const splitNotReported = f.split ? f.split.unsplit_steps - f.split.outside_unpriced_steps : 0;
  // Which machine is behind where readings were not reported (#120 review).
  const behind = useMemo(() => (f.secretKey != null ? hostsBehind(data.external, f.secretKey, f.slot!) : []), [data.external, f.secretKey, f.slot]);
  // What a point was priced at, for the hour note: named only when every
  // priced step has the same rate, since an average over two plans fits no
  // bar. Without movement, the plan's own price per point.
  const rate = stepRate(steps);
  const pointPrice = rate.mixed ? null : rate.rate ?? (f.plan ? pricePerPoint(f.plan) : null);

  return (
    <section className="mb-5" aria-label={name} data-credential={f.id}>
      <div className="flex flex-wrap items-baseline gap-2 mb-2">
        <h3 style={{ fontSize: 15, fontWeight: 600 }}>{name}</h3>
        <span className="muted text-xs">{kind}{plan && !subscription ? ` · ${plan}` : ""}</span>
      </div>
      {subscription && <PlanPrice f={f} name={name} readings={readings} editable={canSetPlan} />}
      {f.secretKey != null && <CostBasisPanel f={f} name={name} editable={canSetPlan} />}
      <MonthlyMeters f={f} readings={readings} />
      <CredentialTiles f={f} filtered={filtered || layout === "agents"} narrowed={filtered} out={mineKpis} split={mineSplit} seatSplit={seatSplit} full={full} />
      <NoMultiple f={f} />
      {/* Outside every reading's span: before the meter's first reading
          (no meter data, never a real share) and after its last (pending). */}
      {f.everything && (["before", "after"] as const).map((k) => {
        const p = f.everything![k];
        return (p.covey ?? 0) + p.outside >= 0.005 && (
          <p key={k} className="muted text-xs mb-2 mt-0" data-unmetered-note={k}>{t(k === "before" ? "costs.incl.unmeteredBefore" : "costs.incl.unmeteredAfter", {
            covey: p.covey != null ? fmtUSD(p.covey) : "–", outside: fmtUSD(p.outside) })}</p>
        );
      })}
      {f.bookingCheck.length > 0 && (
        <p className="text-xs mb-2 mt-0" role="note" style={{ color: "var(--text-warning)" }}>
          {t("costs.fig.bookingCheck", {
            count: f.bookingCheck.reduce((a, b) => a + b.entries, 0),
            usd: fmtUSD(f.bookingCheck.reduce((a, b) => a + b.usd, 0)),
            bases: [...new Set(f.bookingCheck.map((b) => b.basis))].join(", "),
          })}
        </p>
      )}
      {f.bookedWithoutLine > 0.0005 && <p className="muted text-xs mb-2 mt-0">{t("costs.fig.bookedWithoutLine", { usd: fmtUSD(f.bookedWithoutLine) })}</p>}

      {periods.length === 0 ? (
        <div className="muted text-sm" style={{ padding: "20px 0", textAlign: "center" }}>{t("costs.empty")}</div>
      ) : layout === "combined" ? (
        <>
          {meterLine && !asTable && (
            <div className="muted text-xs mb-1" data-meter-legend style={{ display: "flex", alignItems: "center", gap: 6 }}>
              <MeterSwatch /> {t("costs.fig.meterLine")}
            </div>
          )}
          <BarChart title={`${t("costs.chartCost")} · ${name}`} periods={periods} groups={combined} fmtPeriod={fmtPeriod}
            fmtValue={fmtUSD} fmtAxis={fmtUSDAxis} notes={notes} notesLabel={t("costs.outside.colNote")} asTable={asTable} wide={vs}
            meter={meterProp} scrollKey={scrollKey} />
        </>
      ) : (
        <div style={{ display: "grid", gap: 6 }}>
          {perAgent.map((r, i) => (
            <div key={r.a.id}>
              <div className="text-xs" style={{ display: "flex", alignItems: "center", gap: 6 }}>
                <Swatch color={agentColor(agents, r.a.id)} /> <b>{r.a.label}</b>
              </div>
              <BarChart title={`${t("costs.chartCost")} · ${name} · ${r.a.label}`} periods={periods} groups={r.groups} fmtPeriod={fmtPeriod}
                fmtValue={fmtUSD} fmtAxis={fmtUSDAxis} height={i === perAgent.length - 1 && perSource.length === 0 ? 120 : 92}
                showX={i === perAgent.length - 1 && perSource.length === 0} max={sharedScale ? sharedMax : undefined} asTable={asTable} />
            </div>
          ))}
          {perSource.map((r, i) => (
            <div key={`out:${r.s.source}`} data-outside-row={r.s.source}>
              <div className="text-xs" style={{ display: "flex", alignItems: "center", gap: 6 }}>
                <Swatch color={SOURCE_COLOR.recomputed} {...outside!.styles.style(r.s.source)} /> <b>{r.s.source}</b>
                <OutsideMark />
              </div>
              <BarChart title={`${t("costs.chartCost")} · ${name} · ${t("costs.incl.seg", { source: r.s.source })}`} periods={periods} groups={r.groups}
                fmtPeriod={fmtPeriod} fmtValue={fmtUSD} fmtAxis={fmtUSDAxis} height={i === perSource.length - 1 ? 120 : 92}
                showX={i === perSource.length - 1} max={sharedScale ? sharedMax : undefined} asTable={asTable} />
            </div>
          ))}
          {perAgent.length === 0 && perSource.length === 0 && <div className="muted text-sm">{t("costs.empty")}</div>}
        </div>
      )}
      {/* In the table too: it is the only word on what the comparison means. */}
      {vs && layout === "combined" && <p className="muted text-xs mt-1 mb-0" data-vs-note>{t("costs.fig.vsNote")}</p>}
      {subscription && bucket === "hour" && showReal && (rate.mixed || pointPrice != null) && <p className="muted text-xs mt-1 mb-0" data-hour-note>
        {rate.mixed ? t("costs.fig.hourRealNoteMixed") : t("costs.fig.hourRealNote", { usd: fmtUSD(pointPrice ?? 0) })}
      </p>}
      {/* The comparison was asked for but is not drawn here: say why. An agent filter has its own note below. */}
      {billWhole >= 0.005 && <p className="muted text-xs mt-1 mb-0" data-billed-whole-days>
        {t("costs.fig.billedWholeDays", { usd: fmtUSD(billWhole) })}</p>}
      {billedShort && <p className="muted text-xs mt-1 mb-0" data-billed-hourly-from>
        {t("costs.fig.billedHourlyFrom", { at: fmtBucket(billed!.hourly_from!, "hour") })}</p>}
      {view === "vs" && layout === "combined" && !vs && !(filtered && (subscription || f.kind === "api_key")) && (
        <p className="muted text-xs mt-1 mb-0" data-vs-absent>{t("costs.fig.vsAbsent")}</p>
      )}
      {showReal && f.split && <p className="muted text-xs mt-1 mb-0" data-split-note>
        {t(f.unlinked ? "costs.split.chartNoteUnlinked" : "costs.split.chartNote")}
        {(f.split.unattributed_real_usd ?? 0) >= 0.005 && <> {t("costs.split.unattributedNote", { usd: fmtUSD(f.split.unattributed_real_usd) })}</>}
        {splitNotReported > 0 && <> {t("costs.split.unsplitNote", { n: fmtNumber(splitNotReported, 0),
          usd: fmtUSD(f.split.unsplit_real_usd - f.split.outside_unpriced_real_usd) })}</>}
        {behind.length > 0 && <span data-hosts-behind> {hostsBehindText(t, behind)}</span>}
        {f.split.outside_unpriced_steps > 0 && <> {t("costs.split.unsplitUnpricedNote", {
          n: fmtNumber(f.split.outside_unpriced_steps, 0), usd: fmtUSD(f.split.outside_unpriced_real_usd) })}</>}
      </p>}
      {/* A seat no source names, split by the server between covey's agents
          and the unattributed part: its meter may still carry use outside
          covey nothing reports. */}
      {subscription && f.unlinked && f.split && <p className="muted text-xs mt-1 mb-0" data-outside-unreported>
        {t("costs.split.unlinkedSeat")}
      </p>}
      {subscription && f.meterOnUsage && <p className="muted text-xs mt-1 mb-0" data-meter-on-usage>{t("costs.split.usageSeat")}</p>}
      {subscription && f.outsideUnreported && <p className="muted text-xs mt-1 mb-0" data-outside-unreported>
        {t(seatTotal == null || seatTotal.sources.length === 0 ? "costs.split.mayIncludeOutside"
          : seatTotal.unsplit_steps > 0 && seatTotal.outside_unpriced_steps === seatTotal.unsplit_steps
            ? "costs.split.unpricedSeat" : "costs.split.notReportedSeat")}
        {!f.split && behind.length > 0 && <span data-hosts-behind> {hostsBehindText(t, behind)}</span>}
      </p>}
      {filtered && (subscription || f.kind === "api_key") && <p className="muted text-xs mt-1 mb-0">{t("costs.fig.filteredRealNote")}</p>}

      {(f.agents.length > 0 || mine.length > 0) && <AgentTable f={f} agents={agents} subscription={subscription}
        outside={outside ? mine.map((s) => ({ s, k: sourceKpis(s, outside), style: outside.styles.style(s.source), before: beforeRange(s, outside.real) })) : []}
        filtered={filtered} full={full} />}
    </section>
  );
}

/** The agents of one credential side by side, by recomputed list price. */
/** "outside covey", beside a source wherever it stands among the agents. */
export function OutsideMark() {
  const { t } = useTranslation();
  return (
    <span className="muted text-xs" data-outside-mark style={{
      marginLeft: 6, padding: "0 5px", borderRadius: 4, border: "1px dashed var(--border-strong)", fontWeight: 400, whiteSpace: "nowrap",
    }}>{t("costs.incl.mark")}</span>
  );
}

function AgentTable({ f, agents, subscription, outside, filtered, full }: {
  f: CredentialFigures; agents: CellAgent[]; subscription: boolean;
  /** The outside sources on this seat, included: a row each and a total with them. */
  outside: { s: ExternalSource; k: OutsideKpis; style: { hatch: number; ink?: string }; before: boolean }[];
  filtered: boolean;
  /** Reported and Recomputed columns with their gap (the toggle); else one lead column and the check where it differs. */
  full: boolean;
}) {
  const { t } = useTranslation();
  // Included, the shares are of the seat's whole list price, agents and
  // outside sessions together, so the rows add up to 100 %.
  const outList = outside.reduce((a, o) => a + (o.k.list ?? 0), 0);
  const whole = outside.length > 0 ? f.listAll + outList : null;
  const shareOf = (a: { share: number | null; recomputed: number }) => (whole != null ? (whole > 0 ? a.recomputed / whole : null) : a.share);
  const parts = new Map((f.split?.source_parts ?? []).map((p) => [p.source, p.real_usd]));
  const agentsReal = f.agents.filter((a) => a.realShare != null).reduce((a, x) => a + x.realShare!, 0);
  // The totals follow the chips: only the sources with a row (L1).
  const outReal = outside.reduce((a, o) => a + (parts.get(o.s.source) ?? 0), 0);
  const repUnpriced = outside.some((o) => partUnpriced(o.k, "reported"));
  const listUnpriced = outside.some((o) => partUnpriced(o.k, "list"));
  // At the lead price (#120): one column, and covey's price table beside it
  // only in the rows where it differs (PRICE_CHECK).
  const outLead = outside.reduce((a, o) => a + (kpiLead(o.k).lead ?? 0), 0);
  const leadUnpriced = outside.some((o) => partUnpriced(o.k, "lead"));
  const leadMarked = outside.some((o) => kpiLead(o.k).recomputed > 0);
  const outCheck = (k: OutsideKpis) => {
    const l = kpiLead(k).lead;
    return l != null && k.list != null ? priceTableCell(l, k.list) : "";
  };
  const totalLead = f.reported + outLead;
  const checks = [...f.agents.filter((a) => !a.beforeWindow).map((a) => priceTableCell(a.reported, a.recomputed)),
    ...outside.map((o) => outCheck(o.k)), outside.length > 0 ? priceTableCell(totalLead, f.recomputed + outList) : ""];
  const checkCol = !full && checks.some(Boolean);
  const th: React.CSSProperties = { padding: "4px 8px 6px 0", fontWeight: 500, whiteSpace: "nowrap", textAlign: "right" };
  const td: React.CSSProperties = { padding: "3px 8px 3px 0", textAlign: "right", fontVariantNumeric: "tabular-nums", whiteSpace: "nowrap" };
  return (
    <div style={{ overflowX: "auto", marginTop: 8 }}>
      <table style={{ width: "100%", borderCollapse: "collapse", fontSize: 12 }}>
        <caption className="muted text-xs" style={{ textAlign: "left", paddingBottom: 4 }}>
          {t("costs.fig.agentsTitle")}
          {subscription && f.real?.priced && f.meter && <span data-span> · {realSpan(t, f, "costs.fig.shareOfRealSpan")}</span>}
          {subscription && f.real?.priced && f.split && <span data-split-share> · {t("costs.split.agentsShare", { usd: fmtUSD(f.split.covey_real_usd) })}</span>}
          {subscription && f.real?.priced && f.outsideUnreported && <span data-split-share> · {t("costs.split.agentsUpTo")}</span>}
        </caption>
        <thead>
          <tr className="muted">
            <th scope="col" style={{ ...th, textAlign: "left" }}>{t("costs.fig.agent")}</th>
            {full ? <>
              <th scope="col" style={th}>{t("costs.fig.reported")}</th>
              <th scope="col" style={th}>{t("costs.fig.recomputed")}</th>
              <th scope="col" style={th}>{t("costs.fig.gap")}</th>
            </> : <th scope="col" style={th} data-lead-col>{t("costs.lead.label")}</th>}
            {checkCol && <th scope="col" style={th} data-check-col>{t("costs.lead.colCheck")}</th>}
            <th scope="col" style={th}>{t("costs.fig.share")}</th>
            {subscription && <th scope="col" style={th}>{t("costs.fig.shareOfReal")}</th>}
          </tr>
        </thead>
        <tbody>
          {f.agents.map((a) => (
            <tr key={a.id} style={{ borderTop: "1px solid var(--border)" }}>
              <th scope="row" style={{ ...td, textAlign: "left", fontWeight: 400 }}>
                <span style={{ display: "inline-flex", alignItems: "center", gap: 6 }}><Swatch color={agentColor(agents, a.id)} /> {a.label}</span>
                {a.beforeWindow && <span className="muted" data-before-window style={{ display: "block", fontSize: 11 }}>{t("costs.split.agentBeforeWindow")}</span>}
              </th>
              {full ? <>
                <td style={td}>{a.beforeWindow ? "–" : fmtUSD(a.reported)}</td>
                <td style={td}>{a.beforeWindow ? "–" : fmtUSD(a.recomputed)}</td>
                <td style={td}>{a.gapPct != null ? pct(a.gapPct) : "–"}</td>
              </> : <td style={td} data-lead>{a.beforeWindow ? "–" : fmtUSD(a.reported)}</td>}
              {checkCol && <td style={td} data-check>{a.beforeWindow ? "" : priceTableCell(a.reported, a.recomputed)}</td>}
              <td style={td}>{shareOf(a) != null ? `${fmtNumber(shareOf(a)! * 100, 1)} %` : "–"}</td>
              {subscription && <td style={td} title={a.realWhy === "unsplit" ? t("costs.split.agentUnsplit") : undefined} data-real-share>
                {a.realShare != null ? `≈ ${fmtUSD(a.realShare)}` : "–"}
              </td>}
            </tr>
          ))}
          {outside.map(({ s, k, style, before }) => {
            const real = parts.get(s.source);
            return (
              <tr key={`out:${s.source}`} data-outside-row={s.source} style={{ borderTop: "1px solid var(--border)" }}>
                <th scope="row" style={{ ...td, textAlign: "left", fontWeight: 400 }}>
                  <span style={{ display: "inline-flex", alignItems: "center", gap: 6 }}>
                    <Swatch color={SOURCE_COLOR.recomputed} hatch={style.hatch} ink={style.ink} /> {s.source}
                  </span>
                  <OutsideMark />
                  {before && <span className="muted" data-before-range style={{ display: "block", fontSize: 11 }}>{t("costs.incl.beforeRange")}</span>}
                </th>
                {full ? <>
                  <td style={td}>{outsideMoney(t, k, "reported")}</td>
                  <td style={td}>{outsideMoney(t, k, "list")}</td>
                  <td style={td}>{k.reported != null && k.reported > 0 && k.list != null && k.reportedCalls === k.calls ? pct(((k.list - k.reported) / k.reported) * 100) : "–"}</td>
                </> : <td style={td} data-lead>{outsideMoney(t, k, "lead")}</td>}
                {checkCol && <td style={td} data-check>{outCheck(k)}</td>}
                <td style={td}>{whole != null && whole > 0 && k.list != null ? `${fmtNumber((k.list / whole) * 100, 1)} %` : "–"}</td>
                {subscription && <td style={td} data-outside-real>
                  {f.split && real != null ? `≈ ${fmtUSD(real)}` : "–"}
                </td>}
              </tr>
            );
          })}
          {outside.length > 0 && (
            <tr data-incl-total style={{ borderTop: "1px solid var(--border-strong)", fontWeight: 600 }}>
              <th scope="row" style={{ ...td, textAlign: "left" }}>{t(filtered ? "costs.incl.totalSelected" : "costs.incl.total")}</th>
              {full ? <>
                <td style={td} data-incl-reported>{withUnpriced(t, f.reported + outside.reduce((a, o) => a + (o.k.reported ?? 0), 0), repUnpriced)}</td>
                <td style={td} data-incl-list>{withUnpriced(t, f.recomputed + outList, listUnpriced)}</td>
                <td style={td}>–</td>
              </> : <td style={td} data-incl-lead>{leadMarked ? t("costs.lead.part", { usd: withUnpriced(t, totalLead, leadUnpriced) })
                : withUnpriced(t, totalLead, leadUnpriced)}</td>}
              {checkCol && <td style={td} data-check>{priceTableCell(totalLead, f.recomputed + outList)}</td>}
              <td style={td}>{whole != null && whole > 0 ? `${fmtNumber(((f.recomputed + outList) / whole) * 100, 1)} %` : "–"}</td>
              {subscription && <td style={td} data-incl-real>
                {f.real?.priced && (f.split || f.agents.some((a) => a.realShare != null))
                  ? `≈ ${fmtUSD(agentsReal + (f.split ? outReal : 0))}` : "–"}
              </td>}
            </tr>
          )}
        </tbody>
      </table>
    </div>
  );
}

/** The colour code of the page, explained once. */
function ColourKey({ split, sources = [], other = [], full = true, list = full }: {
  split: boolean; sources?: { name: string; hatch: number; ink?: string }[];
  /** The folded sources, drawn as one "other sources" series (M5). */
  other?: string[];
  /** The full view of covey's price table: Reported and Recomputed apart. Else grey is the lead figure. */
  full?: boolean;
  /** covey's price table is drawn (the full view's Recomputed bars). */
  list?: boolean;
}) {
  const { t } = useTranslation();
  const items: [Source, string, boolean?][] = [
    ["reported", full ? t("costs.fig.keyReported") : t("costs.lead.key")],
    ...(list ? [["recomputed", t("costs.fig.keyRecomputed")] as [Source, string]] : []),
    ["real", t("costs.fig.keyReal"), true],
    ["billed", t("costs.fig.keyBilled")],
  ];
  return (
    <div className="flex flex-wrap items-center gap-3 text-xs muted mb-3" role="note" aria-label={t("costs.fig.key")}>
      {items.map(([s, label, h]) => (
        <span key={s} style={{ display: "inline-flex", alignItems: "center", gap: 6 }}><Swatch color={SOURCE_COLOR[s]} hatched={h} /> {label}</span>
      ))}
      {split && (["outside", "unattributed", "unsplit"] as const).map((k) => (
        <span key={k} style={{ display: "inline-flex", alignItems: "center", gap: 6 }} data-split-key={k}>
          <Swatch color={SPLIT_COLOR[k]} hatched dashed={k === "unsplit" ? SPLIT_COLOR.unsplitEdge : undefined}
            hatch={k === "unsplit" ? undefined : SPLIT_HATCH[k]} /> {t(`costs.split.key_${k}`)}
        </span>
      ))}
      {/* Each outside source: its own hatch, in the reported and the recomputed colour. */}
      {sources.map((x) => (
        <span key={x.name} style={{ display: "inline-flex", alignItems: "center", gap: 4 }} data-outside-key={x.name}>
          <Swatch color={SOURCE_COLOR.reported} hatch={x.hatch} ink={x.ink} />{list && <Swatch color={SOURCE_COLOR.recomputed} hatch={x.hatch} ink={x.ink} />}
          {" "}{t("costs.incl.key", { source: x.name })}
        </span>
      ))}
      {other.length > 0 && (
        <span style={{ display: "inline-flex", alignItems: "center", gap: 4 }} data-outside-key={OTHER_SOURCES}>
          <Swatch color={SOURCE_COLOR.reported} hatch={DOT_HATCH} />{list && <Swatch color={SOURCE_COLOR.recomputed} hatch={DOT_HATCH} />}
          {" "}{t("costs.incl.otherKey", { n: other.length, sources: other.join(", ") })}
        </span>
      )}
    </div>
  );
}

export function CostExplorer({ data, billed, readings = [], canSetPlan = false, selected, bucket, scrollKey, fmtPeriod, outside = null, full: fullProp, onFull }: {
  data: CostCells | undefined; billed?: BilledReport; readings?: readonly UsageReading[]; canSetPlan?: boolean;
  selected: Set<string>; bucket: string; fmtPeriod: (p: string) => string;
  /** The chart's view, range and bucket: where it changes, a chart with the meter scrolls to its newest period. */
  scrollKey?: string;
  /** The sessions outside covey, included (#120, the default); null: covey only. */
  outside?: ExternalUsage | null;
  /** The full view of covey's price table (Reported and Recomputed apart),
   *  held by the page (in its URL); without it the explorer holds it. */
  full?: boolean;
  onFull?: (v: boolean) => void;
}) {
  const { t } = useTranslation();
  // One list-price figure leads (#120): the lead series. covey's price
  // table is a check under it; the full view is the secondary toggle.
  const [ownFull, setOwnFull] = useState(false);
  const full = fullProp ?? ownFull;
  const setFull = onFull ?? setOwnFull;
  const [layout, setLayout] = useState<Layout>("combined");
  const [view, setView] = useState<View>("amounts");
  const [split, setSplit] = useState<Split>("none");
  const [sharedScale, setSharedScale] = useState(true);
  const [asTable, setAsTable] = useState(false);
  const [src, setSrc] = useState<Record<Source, boolean>>({ reported: true, recomputed: true, real: true, billed: true });
  const [types, setTypes] = useState<Set<string>>(new Set(COST_TYPES));

  const figures = useMemo(() => (data ? credentialFigures({
    cells: data, billed, bucketMs: BUCKET_MS[bucket] ?? BUCKET_MS.day, windowEnd: new Date(), agents: selected, layout,
    includeOutside: outside != null,
  }) : []), [data, billed, bucket, selected, layout, outside]);
  // The outside sources with calls in the range, each a series of its own,
  // on by default; a chip switches one off.
  const [offSources, setOffSources] = useState<Set<string>>(new Set());
  const listed = useMemo(() => listedSources(outside), [outside]);
  const onChart = useMemo<OutsideOnChart | null>(() => (outside ? outsideOnChart(outside, offSources) : null), [outside, offSources]);
  const toggleSource = (n: string) => setOffSources((v) => { const x = new Set(v); if (x.has(n)) x.delete(n); else x.add(n); return x; });
  // Sources whose seat has no block here (billed per use, or a seat with
  // neither lines nor readings in the range): a block of their own.
  const loose = onChart ? sourcesWithoutSeat(outside, new Set(figures.map((f) => f.id))).filter((x) => onChart.on.has(x.source)) : [];

  const toggleType = (k: string) => setTypes((s) => { const n = new Set(s); if (n.has(k)) n.delete(k); else n.add(k); return n; });
  const toggleSrc = (s: Source) => setSrc((v) => {
    const n = { ...v, [s]: !v[s] };
    // Keep at least one on show: Recomputed counts only in the full view.
    return n.reported || (full && n.recomputed) || n.real || n.billed ? n : v;
  });
  // The meter and the bill are hourly (#120): at 10 minutes Real and Billed
  // are off and say why; the user's choice comes back at a coarser bucket.
  const hourly = (s: Source) => bucket === "10min" && (s === "real" || s === "billed");
  const tenMinId = `${useId().replace(/[^a-zA-Z0-9_-]/g, "")}-tenmin`;
  const own: Record<Source, boolean> = full ? src : { ...src, recomputed: false };
  const shown: Record<Source, boolean> = bucket === "10min"
    ? full ? { ...own, real: false, billed: false, recomputed: own.recomputed || !own.reported } : { ...own, real: false, billed: false, reported: true }
    : own;
  const chips = full ? SOURCES : SOURCES.filter((s) => s !== "recomputed");
  const chipLabel = (s: Source) => (!full && s === "reported" ? t("costs.lead.label") : sourceLabel(t, s));

  return (
    <div>
      <ColourKey split={figures.some((f) => f.split != null)} full={full} list={full && (shown.recomputed || (layout === "combined" && view === "vs"))}
        sources={onChart ? listed.filter((x) => onChart.on.has(x.source) && onChart.styles.series(x.source) !== OTHER_SOURCES)
          .map((x) => ({ name: x.source, ...onChart.styles.style(x.source) })) : []}
        other={onChart ? onChart.styles.folded.filter((n) => onChart.on.has(n)) : []} />
      <Row label={t("costs.filter.source")}>
        {chips.map((s) => (
          <Chip key={s} on={shown[s]} color={SOURCE_COLOR[s]} hatched={s === "real"} onClick={() => toggleSrc(s)}
            disabled={hourly(s)} title={hourly(s) ? t("costs.outside.tenMinNoMeter") : undefined}
            describedBy={hourly(s) ? tenMinId : undefined}>
            {chipLabel(s)}
          </Chip>
        ))}
        {bucket === "10min" && <span id={tenMinId} className="muted text-xs" data-ten-min-note>{t("costs.outside.tenMinNoMeter")}</span>}
        {/* The secondary toggle: covey's price table as a figure of its own. */}
        <label className="text-xs muted" data-full-prices title={t("costs.lead.toggleTitle")}
          style={{ display: "inline-flex", alignItems: "center", gap: 6, marginLeft: 8, cursor: "pointer" }}>
          <input type="checkbox" checked={full} onChange={() => setFull(!full)} />
          {t("costs.lead.toggle")}
        </label>
      </Row>
      {onChart && listed.length > 0 && (
        <Row label={t("costs.incl.chips")}>
          {listed.map((x) => (
            <Chip key={x.source} on={onChart.on.has(x.source)} color={SOURCE_COLOR.recomputed} {...onChart.styles.style(x.source)}
              onClick={() => toggleSource(x.source)}>
              {x.source} <span style={{ opacity: 0.7 }}>{beforeRange(x, onChart.real) ? t("costs.incl.beforeRange")
                : x.list_usd != null ? fmtUSD(x.list_usd) : t("costs.incl.noList")}</span>
            </Chip>
          ))}
        </Row>
      )}
      <details className="text-xs muted mb-2" style={{ marginLeft: 92 }}>
        <summary style={{ cursor: "pointer" }}>{t("costs.filter.sourceHints")}</summary>
        <ul style={{ margin: "4px 0 0 0", paddingLeft: 16, maxWidth: 760 }}>
          {!full && <li>{t("costs.lead.hint")}</li>}
          {(full ? SOURCES : SOURCES.filter((s) => s !== "reported" && s !== "recomputed")).map((s) => <li key={s}>{sourceHint(t, s)}</li>)}
        </ul>
      </details>
      {layout === "combined" && (
        <Row label={t("costs.filter.compare")}>
          <Chip on={view === "amounts"} onClick={() => setView("amounts")}>{t("costs.filter.view_amounts")}</Chip>
          <Chip on={view === "vs"} onClick={() => setView("vs")}>{t("costs.filter.view_vs")}</Chip>
        </Row>
      )}
      <Row label={t("costs.filter.layout")}>
        <Chip on={layout === "combined"} onClick={() => setLayout("combined")}>{t("costs.filter.layout_combined")}</Chip>
        <Chip on={layout === "agents"} onClick={() => setLayout("agents")}>{t("costs.filter.layout_agents")}</Chip>
        {layout === "agents" && <>
          <span className="muted text-xs" style={{ marginLeft: 8 }}>{t("costs.fig.scale")}</span>
          <Chip on={sharedScale} onClick={() => setSharedScale(true)}>{t("costs.fig.scaleShared")}</Chip>
          <Chip on={!sharedScale} onClick={() => setSharedScale(false)}>{t("costs.fig.scaleOwn")}</Chip>
        </>}
        <span style={{ flex: 1 }} />
        <Chip on={asTable} onClick={() => setAsTable((v) => !v)}>{t("costs.fig.showAsTable")}</Chip>
      </Row>
      {layout === "combined" && full && (
        <Row label={t("costs.filter.split")}>
          {(["none", "type"] as Split[]).map((s) => (
            <Chip key={s} on={split === s} onClick={() => setSplit(s)}>{t(`costs.filter.split_${s}`)}</Chip>
          ))}
        </Row>
      )}
      {full && (shown.recomputed || view === "vs") && layout === "combined" && split === "type" && (
        <Row label={t("costs.filter.types")}>
          {COST_TYPES.map((k) => (
            <Chip key={k} on={types.has(k)} color={TYPE_COLOR[k]} onClick={() => toggleType(k)}>{t(`costs.stack.${k}`)}</Chip>
          ))}
        </Row>
      )}

      {figures.length === 0 && loose.length === 0 ? (
        <div className="muted text-sm" style={{ padding: "40px 0", textAlign: "center" }}>{t("costs.empty")}</div>
      ) : figures.map((f) => (
        <CredentialBlock key={f.id} f={f} data={data!} billed={billed} readings={readings} canSetPlan={canSetPlan} bucket={bucket} scrollKey={scrollKey} fmtPeriod={fmtPeriod} selected={selected}
          src={shown} split={split} types={types} layout={layout} view={view} sharedScale={sharedScale} asTable={asTable} outside={onChart} full={full} />
      ))}
      {onChart && loose.length > 0 && (
        <OutsideBlock o={onChart} sources={loose} src={shown} fmtPeriod={fmtPeriod} asTable={asTable} filtered={selected.size > 0} full={full} />
      )}
    </div>
  );
}

/** The outside sources whose seat has no block on the page (billed per use,
 *  or linked to a seat the range has neither lines nor readings for): their
 *  reported cost and list price, hatched per source, and a row each. No real
 *  cost: no meter of theirs is read here. */
function OutsideBlock({ o, sources, src, fmtPeriod, asTable, filtered, full }: {
  o: OutsideOnChart; sources: ExternalSource[]; src: Record<Source, boolean>; fmtPeriod: (p: string) => string; asTable: boolean; filtered: boolean;
  full: boolean;
}) {
  const { t } = useTranslation();
  const title = t("costs.incl.noSeatTitle");
  const periods = useMemo(() => {
    const m = new Map<number, string>();
    for (const x of sources) for (const [at, e] of o.per.get(x.source) ?? []) m.set(at, e.period);
    return [...m.entries()].sort((a, b) => a[0] - b[0]).map((e) => e[1]);
  }, [o, sources]);
  const groups = periods.map((p): Bar[] => {
    const at = Date.parse(p);
    const bars: Bar[] = [];
    if (src.reported && !full) {
      const segs = outsideSegs(t, o, sources, at, "lead");
      bars.push({ key: "reported", label: t("costs.lead.label"), color: SOURCE_COLOR.reported, segs, note: priceTableLine(t, sum(segs), outsideTable(o, sources, at)) });
    } else if (src.reported) bars.push({ key: "reported", label: t("costs.fig.reported"), color: SOURCE_COLOR.reported, segs: outsideSegs(t, o, sources, at, "reported") });
    if (src.recomputed && full) bars.push({ key: "recomputed", label: t("costs.fig.recomputed"), color: SOURCE_COLOR.recomputed, segs: outsideSegs(t, o, sources, at, "list") });
    return bars;
  });
  const ks = sources.map((x) => ({ x, k: sourceKpis(x, o) }));
  // At the lead price: one column, covey's price table only where it differs.
  const check = (k: OutsideKpis) => {
    const l = kpiLead(k).lead;
    return l != null && k.list != null ? priceTableCell(l, k.list) : "";
  };
  const leadSum = ks.reduce((a, e) => a + (kpiLead(e.k).lead ?? 0), 0);
  const listSum = ks.reduce((a, e) => a + (e.k.list ?? 0), 0);
  const totalCheck = ks.some((e) => e.k.list != null) ? priceTableCell(leadSum, listSum) : "";
  const checkCol = !full && (ks.some((e) => check(e.k)) || totalCheck !== "");
  const leadTotal = ks.some((e) => kpiLead(e.k).lead != null)
    ? withUnpriced(t, leadSum, ks.some((e) => partUnpriced(e.k, "lead"))) : t("costs.incl.noList");
  const td: React.CSSProperties = { padding: "3px 8px 3px 0", textAlign: "right", fontVariantNumeric: "tabular-nums", whiteSpace: "nowrap" };
  return (
    <section className="mb-5" aria-label={title} data-outside-block>
      <div className="flex flex-wrap items-baseline gap-2 mb-2">
        <h3 style={{ fontSize: 15, fontWeight: 600 }}>{title}</h3>
        <OutsideMark />
      </div>
      <p className="muted text-xs mb-2 mt-0" style={{ maxWidth: 760 }}>{t("costs.incl.noSeatHint")}</p>
      {periods.length > 0 && (src.reported || (full && src.recomputed)) && (
        <BarChart title={`${t("costs.chartCost")} · ${title}`} periods={periods} groups={groups} fmtPeriod={fmtPeriod}
          fmtValue={fmtUSD} fmtAxis={fmtUSDAxis} asTable={asTable} height={200} />
      )}
      <div style={{ overflowX: "auto", marginTop: 8 }}>
        <table style={{ width: "100%", borderCollapse: "collapse", fontSize: 12 }}>
          <thead>
            <tr className="muted">
              <th scope="col" style={{ ...td, textAlign: "left", fontWeight: 500 }}>{t("costs.outside.source")}</th>
              {full ? <>
                <th scope="col" style={{ ...td, fontWeight: 500 }}>{t("costs.fig.reported")}</th>
                <th scope="col" style={{ ...td, fontWeight: 500 }}>{t("costs.fig.recomputed")}</th>
              </> : <th scope="col" style={{ ...td, fontWeight: 500 }} data-lead-col>{t("costs.lead.label")}</th>}
              {checkCol && <th scope="col" style={{ ...td, fontWeight: 500 }} data-check-col>{t("costs.lead.colCheck")}</th>}
            </tr>
          </thead>
          <tbody>
            {ks.map(({ x, k }) => (
              <tr key={x.source} data-outside-row={x.source} style={{ borderTop: "1px solid var(--border)" }}>
                <th scope="row" style={{ ...td, textAlign: "left", fontWeight: 400 }}>
                  <span style={{ display: "inline-flex", alignItems: "center", gap: 6 }}>
                    <Swatch color={SOURCE_COLOR.recomputed} {...o.styles.style(x.source)} /> {x.source}
                  </span>
                  <OutsideMark />
                </th>
                {full ? <>
                  <td style={td}>{outsideMoney(t, k, "reported")}</td>
                  <td style={td}>{outsideMoney(t, k, "list")}</td>
                </> : <td style={td} data-lead>{outsideMoney(t, k, "lead")}</td>}
                {checkCol && <td style={td} data-check>{check(k)}</td>}
              </tr>
            ))}
            <tr data-incl-total style={{ borderTop: "1px solid var(--border-strong)", fontWeight: 600 }}>
              <th scope="row" style={{ ...td, textAlign: "left" }}>{t(filtered ? "costs.incl.totalOutsideSelected" : "costs.incl.totalOutside")}</th>
              {full ? <>
                <td style={td}>{ks.some((e) => e.k.reported != null)
                  ? withUnpriced(t, ks.reduce((a, e) => a + (e.k.reported ?? 0), 0), ks.some((e) => partUnpriced(e.k, "reported"))) : t("costs.incl.noReported")}</td>
                <td style={td}>{ks.some((e) => e.k.list != null)
                  ? withUnpriced(t, ks.reduce((a, e) => a + (e.k.list ?? 0), 0), ks.some((e) => partUnpriced(e.k, "list"))) : t("costs.incl.noList")}</td>
              </> : <td style={td} data-incl-lead>{ks.some((e) => kpiLead(e.k).recomputed > 0) ? t("costs.lead.part", { usd: leadTotal }) : leadTotal}</td>}
              {checkCol && <td style={td} data-check>{totalCheck}</td>}
            </tr>
          </tbody>
        </table>
      </div>
    </section>
  );
}

/** Tokens per type over time, from the same lines as the dollars above, over
 *  every credential, narrowed by the agent filter. With the outside sessions
 *  included, a hatched bar of theirs beside the agents' in each period. */
export function TokenExplorer({ data, selected, fmtPeriod, outside = null }: {
  data: CostCells | undefined; selected: Set<string>; fmtPeriod: (p: string) => string;
  outside?: ExternalUsage | null;
}) {
  const { t } = useTranslation();
  const [asTable, setAsTable] = useState(false);
  const { periods, groups } = useMemo(() => {
    const by = new Map<number, { p: string; tok: Record<string, number>; out: Record<string, number> | null }>();
    for (const c of data?.cells ?? []) {
      if (selected.size > 0 && !selected.has(c.agent_id)) continue;
      const k = Date.parse(c.period);
      const e = by.get(k) ?? { p: c.period, tok: {}, out: null };
      for (const [ty, v] of Object.entries(c.tokens ?? {})) e.tok[ty] = (e.tok[ty] ?? 0) + v;
      by.set(k, e);
    }
    const names = new Set(listedSources(outside).map((x) => x.source));
    for (const c of outside?.cells ?? []) {
      if (!names.has(c.source)) continue;
      const k = Date.parse(c.period);
      const e = by.get(k) ?? { p: c.period, tok: {}, out: null };
      e.out = e.out ?? {};
      for (const [ty, v] of Object.entries(c.tokens ?? {})) e.out[ty] = (e.out[ty] ?? 0) + v;
      by.set(k, e);
    }
    const rows = [...by.entries()].sort((a, b) => a[0] - b[0]).map((e) => e[1]);
    const label = outside ? t(selected.size > 0 ? "costs.incl.tokensSelected" : "costs.incl.tokensCovey") : t("costs.mTokens");
    return {
      periods: rows.map((r) => r.p),
      groups: rows.map((r) => [{
        key: "tokens", label, color: TYPE_COLOR.output,
        segs: TOKEN_TYPES.map((ty) => ({ key: ty, label: t(`costs.tok.${ty}`), color: TYPE_COLOR[ty], value: r.tok[ty] ?? 0 })),
      }, ...(outside ? [{
        key: "outside", label: t("costs.incl.tokensOutside"), color: TYPE_COLOR.output, hatched: true,
        segs: TOKEN_TYPES.map((ty) => ({ key: ty, label: t(`costs.tok.${ty}`), color: TYPE_COLOR[ty], value: r.out?.[ty] ?? 0 })),
      }] : [])] as Bar[]),
    };
  }, [data, selected, outside, t]);
  if (periods.length === 0) return <div className="muted text-sm" style={{ padding: "40px 0", textAlign: "center" }}>{t("costs.empty")}</div>;
  return (
    <div>
      <div className="flex flex-wrap items-center gap-3 text-xs muted mb-2">
        {TOKEN_TYPES.map((ty) => (
          <span key={ty} style={{ display: "inline-flex", alignItems: "center", gap: 6 }}><Swatch color={TYPE_COLOR[ty]} /> {t(`costs.tok.${ty}`)}</span>
        ))}
        {outside && <span style={{ display: "inline-flex", alignItems: "center", gap: 6 }} data-outside-key="tokens">
          <Swatch color={TYPE_COLOR.output} hatched /> {t("costs.incl.tokensKey")}</span>}
        <span style={{ flex: 1 }} />
        <Chip on={asTable} onClick={() => setAsTable((v) => !v)}>{t("costs.fig.showAsTable")}</Chip>
      </div>
      <BarChart title={t("costs.chartTokens")} periods={periods} groups={groups} fmtPeriod={fmtPeriod}
        fmtValue={(v) => `${fmtCount(v)} ${t("costs.tok.unit")}`} fmtAxis={fmtCountAxis} asTable={asTable} />
    </div>
  );
}

/** Totals over the cells for the KPI row: dollars and tokens of the same lines. */
export function cellTotals(data: CostCells | undefined, selected: Set<string>) {
  const out = { reported: 0, recomputed: 0, tokens: {} as Record<string, number> };
  for (const c of data?.cells ?? []) {
    if (selected.size > 0 && !selected.has(c.agent_id)) continue;
    out.reported += c.engine_usd ?? 0;
    out.recomputed += recomputedOf(c);
    for (const [k, v] of Object.entries(c.tokens ?? {})) out.tokens[k] = (out.tokens[k] ?? 0) + v;
  }
  return out;
}
