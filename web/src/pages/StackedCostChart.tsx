import { useId, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import { api, type BilledReport, type CellAgent, type CostCell, type CostCells } from "../api";
import { fmtBucket, fmtClockUTC, fmtCount, fmtCountAxis, fmtNumber, fmtUSD, fmtUSDAxis } from "../format";
import {
  billedFetchBucket,
  billedPerPeriod,
  credentialFigures,
  credId,
  meterCrossings,
  recomputedOf,
  type CredentialFigures,
} from "./costFigures";

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
type Source = "reported" | "recomputed" | "real" | "billed";
type Layout = "combined" | "agents";

/** One colour per source, everywhere on the page (styles.css). */
export const SOURCE_COLOR: Record<Source, string> = {
  reported: "var(--cost-reported)",
  recomputed: "var(--cost-recomputed)",
  real: "var(--cost-real)",
  billed: "var(--cost-billed)",
};

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

export const BUCKET_MS: Record<string, number> = { hour: 3600_000, day: 86400_000, week: 7 * 86400_000, month: 30 * 86400_000 };

export function useCostCells(days: number, bucket: string) {
  return useQuery({
    queryKey: ["cost", "cells", days, bucket],
    queryFn: () => api<CostCells>(`/cost/cells?days=${days}&bucket=${bucket}`),
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

export function Chip({ on, color, hatched, children, onClick, onDoubleClick }: {
  on: boolean; color?: string; hatched?: boolean; children: React.ReactNode; onClick: () => void; onDoubleClick?: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      onDoubleClick={onDoubleClick}
      aria-pressed={on}
      className="text-xs"
      style={{
        display: "inline-flex", alignItems: "center", gap: 6, padding: "4px 10px", borderRadius: 6,
        border: `1px solid ${on ? "var(--border-strong)" : "var(--border)"}`,
        background: on ? "var(--surface-2)" : "transparent",
        color: on ? "var(--text-primary)" : "var(--text-muted)",
        cursor: "pointer", lineHeight: 1.2,
      }}
    >
      {color && <Swatch color={color} hatched={hatched} dim={!on} />}
      {children}
    </button>
  );
}

export function Swatch({ color, hatched, dim }: { color: string; hatched?: boolean; dim?: boolean }) {
  return (
    <span aria-hidden style={{
      width: 10, height: 10, borderRadius: 2, display: "inline-block", flexShrink: 0, opacity: dim ? 0.4 : 1,
      background: hatched ? `repeating-linear-gradient(45deg, ${color} 0 2px, transparent 2px 4px)` : color,
      border: hatched ? `1px solid ${color}` : undefined,
    }} />
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

/** The agent filter: chips, any combination; "All" = nobody narrowed out. */
export function AgentFilter({ agents, selected, onChange }: {
  agents: CellAgent[]; selected: Set<string>; onChange: (s: Set<string>) => void;
}) {
  const { t } = useTranslation();
  const ranked = rankAgents(agents);
  const toggle = (id: string) => {
    const n = new Set(selected);
    if (n.has(id)) n.delete(id);
    else n.add(id);
    onChange(n);
  };
  return (
    <Row label={t("costs.filter.agents")}>
      <Chip on={selected.size === 0} onClick={() => onChange(new Set())}>{t("costs.filter.all")}</Chip>
      {ranked.map((a) => (
        <Chip key={a.id} on={selected.has(a.id)} color={agentColor(ranked, a.id)}
          onClick={() => toggle(a.id)} onDoubleClick={() => onChange(new Set([a.id]))}>
          {a.label} <span style={{ opacity: 0.7 }}>{fmtUSD(a.measured_usd ?? 0)}</span>
        </Chip>
      ))}
      {ranked.length > 1 && <span className="muted text-xs">{t("costs.filter.toggleHint")}</span>}
    </Row>
  );
}

function Tile({ label, value, sub, span, note, color, hatched }: {
  label: string; value: string; sub?: string; span?: string; note?: string; color?: string; hatched?: boolean;
}) {
  return (
    <div role="group" aria-label={label} data-tile
      style={{ padding: "10px 14px", borderRadius: 8, border: "1px solid var(--border)", minWidth: 170, flex: "1 1 170px" }}>
      <div className="muted text-xs" style={{ display: "flex", alignItems: "center", gap: 6 }}>
        {color && <Swatch color={color} hatched={hatched} />}
        {label}
      </div>
      <div style={{ fontSize: 20, fontWeight: 600, marginTop: 2, fontVariantNumeric: "tabular-nums" }}>{value}</div>
      {sub && <div className="muted text-xs" style={{ marginTop: 2 }}>{sub}</div>}
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

export type Seg = { key: string; label: string; color: string; value: number };
export type Bar = { key: string; label: string; color: string; hatched?: boolean; segs: Seg[] };

const sum = (s: Seg[]) => s.reduce((a, x) => a + x.value, 0);

/** Groups of bars over the periods. Every period is focusable: focus or hover
 *  opens its details, Enter/Space (or a tap) pins them, Escape closes them.
 *  "Show as table" renders the same numbers as a table. */
export function BarChart({ title, periods, groups, fmtPeriod, fmtValue, fmtAxis, height = 280, max, notes, showX = true, asTable }: {
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
  /** A line per period, e.g. the meter's crossings. */
  notes?: Map<number, string>;
  showX?: boolean;
  asTable?: boolean;
}) {
  const { t } = useTranslation();
  const uid = useId().replace(/[^a-zA-Z0-9_-]/g, "");
  const [hover, setHover] = useState<number | null>(null);
  const [pinned, setPinned] = useState<number | null>(null);
  const open = pinned ?? hover;

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
  const desc = t("costs.fig.chartDesc", { totals: series.map((s) => `${s.label} ${fmtValue(s.total)}`).join(", ") });

  if (asTable) {
    return (
      <div style={{ overflowX: "auto" }}>
        <table style={{ width: "100%", borderCollapse: "collapse", fontSize: 12 }}>
          <caption className="muted text-xs" style={{ textAlign: "left", paddingBottom: 4 }}>{title}</caption>
          <thead>
            <tr className="muted" style={{ textAlign: "right" }}>
              <th scope="col" style={{ textAlign: "left", padding: "2px 8px 4px 0", fontWeight: 500 }}>{t("costs.fig.period")}</th>
              {series.map((s) => <th key={s.key} scope="col" style={{ padding: "2px 8px 4px 0", fontWeight: 500 }}>{s.label}</th>)}
              {notes && <th scope="col" style={{ padding: "2px 0 4px 0", fontWeight: 500, textAlign: "left" }}>{t("costs.fig.meterCol")}</th>}
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
                {notes && <td style={{ padding: "2px 0" }}>{notes.get(i) ?? ""}</td>}
              </tr>
            ))}
          </tbody>
          <tfoot>
            <tr data-totals style={{ borderTop: "1px solid var(--border-strong)", fontWeight: 600 }}>
              <th scope="row" style={{ textAlign: "left", padding: "4px 8px 2px 0" }}>{t("costs.fig.total")}</th>
              {series.map((s) => <td key={s.key} data-total style={{ textAlign: "right", padding: "4px 8px 2px 0", fontVariantNumeric: "tabular-nums" }}>{fmtValue(s.total)}</td>)}
              {notes && <td />}
            </tr>
          </tfoot>
        </table>
      </div>
    );
  }

  const padL = 64, padR = 16, padT = 18, padB = showX ? 34 : 8;
  const plotH = height - padT - padB;
  const n = periods.length;
  const nb = Math.max(1, ...groups.map((g) => g.length));
  const barW = nb === 1 ? 28 : nb === 2 ? 20 : 16, gap = 3;
  const slot = Math.max(nb * (barW + gap) + 24, n <= 1 ? 160 : Math.min(120, Math.round(900 / Math.max(1, n))));
  const W = Math.max(560, padL + padR + Math.max(1, n) * slot);
  const ax = niceAxis(max ?? Math.max(0, ...groups.flatMap((g) => g.map((b) => sum(b.segs)))));
  const y = (v: number) => padT + plotH - (v / ax.top) * plotH;
  const every = Math.ceil(n / Math.max(1, Math.floor((W - padL - padR) / 84)));
  const tipW = 250;
  const hx = open != null ? padL + open * slot + slot / 2 : 0;
  const tipLeft = open == null ? 0 : Math.max(0, Math.min(hx + 16 + tipW > W ? hx - 16 - tipW : hx + 16, W - tipW));
  const tipId = `${uid}-tip`;
  const hatches = [...new Set(groups.flatMap((g) => g.filter((b) => b.hatched).map((b) => b.color)))];
  const hatchId = (c: string) => `${uid}-h${hatches.indexOf(c)}`;
  const summary = (i: number) => (groups[i] ?? []).filter((b) => sum(b.segs) > 0).map((b) => `${b.label} ${fmtValue(sum(b.segs))}`).join(", ");
  const close = () => { setPinned(null); setHover(null); };

  return (
    <div style={{ position: "relative", overflowX: "auto" }}>
      <svg width={W} height={height} style={{ display: "block", maxWidth: "none" }} role="group"
        aria-labelledby={`${uid}-title`} aria-describedby={`${uid}-desc`}>
        <title id={`${uid}-title`}>{title}</title>
        <desc id={`${uid}-desc`} data-desc>{desc}</desc>
        <defs>
          {hatches.map((c) => (
            <pattern key={c} id={hatchId(c)} patternUnits="userSpaceOnUse" width={5} height={5} patternTransform="rotate(45)">
              <rect width={5} height={5} fill={c} opacity={0.3} />
              <line x1={0} y1={0} x2={0} y2={5} stroke={c} strokeWidth={3} />
            </pattern>
          ))}
        </defs>
        {ax.ticks.map((v, i) => (
          <g key={i} aria-hidden>
            <line x1={padL} x2={W - padR} y1={y(v)} y2={y(v)} stroke="var(--border)" strokeDasharray={i === 0 ? undefined : "2 4"} />
            <text x={padL - 8} y={y(v) + 4} textAnchor="end" fontSize={11} fill="var(--text-muted)">{fmtAxis(v, ax.step)}</text>
          </g>
        ))}
        {groups.map((bars, i) => {
          const groupW = bars.length * barW + (bars.length - 1) * gap;
          const x0 = padL + i * slot + slot / 2 - groupW / 2;
          const isOpen = open === i;
          const label = `${fmtPeriod(periods[i] ?? "")}: ${summary(i) || t("costs.fig.nothing")}${notes?.get(i) ? ` · ${notes.get(i)}` : ""}`;
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
                const total = sum(b.segs);
                return (
                  <g key={b.key}>
                    {b.segs.map((g) => {
                      if (g.value <= 0) return null;
                      const top = y(acc + g.value), h = Math.max(1, y(acc) - top);
                      acc += g.value;
                      return <rect key={g.key} x={bx} y={top} width={barW} height={h} rx={1.5}
                        fill={b.hatched ? `url(#${hatchId(b.color)})` : g.color} stroke={b.hatched ? b.color : undefined} />;
                    })}
                    {isOpen && total > 0 && (
                      <text x={bx + barW / 2} y={Math.max(10, y(total) - 4)} textAnchor="middle" fontSize={9.5} fill="var(--text-primary)">{fmtValue(total)}</text>
                    )}
                  </g>
                );
              })}
              {notes?.get(i) && (
                <text x={padL + i * slot + slot / 2} y={padT - 6} textAnchor="middle" fontSize={9.5} fill="var(--cost-real)">{notes.get(i)}</text>
              )}
            </g>
          );
        })}
        {showX && periods.map((p, i) =>
          i % every !== 0 && i !== n - 1 ? null : (
            <text key={p} aria-hidden x={padL + i * slot + slot / 2} y={height - padB + 18} textAnchor="middle" fontSize={11} fill="var(--text-muted)">{fmtPeriod(p)}</text>
          ),
        )}
      </svg>
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
              {b.segs.filter((x) => x.value > 0).length > 1 && b.segs.filter((x) => x.value > 0).map((x) => (
                <div key={x.key} className="muted" style={{ display: "flex", justifyContent: "space-between", gap: 12, paddingLeft: 16 }}>
                  <span style={{ display: "inline-flex", alignItems: "center", gap: 6 }}><Swatch color={x.color} /> {x.label}</span>
                  <span>{fmtValue(x.value)}</span>
                </div>
              ))}
            </div>
          ))}
          {notes?.get(open) && <div className="muted" style={{ fontSize: 11 }}>{t("costs.fig.meterCol")}: {notes.get(open)}</div>}
          {groups.at(open)!.every((b) => sum(b.segs) <= 0) && !notes?.get(open) && <div className="muted">{t("costs.fig.nothing")}</div>}
        </div>
      )}
    </div>
  );
}

// Through the shared formatter: the decimal separator of the UI, as in the
// amounts next to them.
const pct = (v: number) => `${v >= 0 ? "+" : ""}${fmtNumber(v, 2)} %`;
const times = (v: number) => (Number.isFinite(v) ? `${fmtNumber(v, 1)}×` : "∞");

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
function CredentialTiles({ f, filtered }: { f: CredentialFigures; filtered: boolean }) {
  const { t } = useTranslation();
  const tiles: React.ReactNode[] = [
    <Tile key="rep" label={t("costs.fig.reported")} value={fmtUSD(f.reported)} sub={t("costs.fig.reportedSub")} color={SOURCE_COLOR.reported} />,
    <Tile key="rec" label={t("costs.fig.recomputed")} value={fmtUSD(f.recomputed)}
      sub={f.check != null ? t("costs.fig.priceTableCheck", { pct: pct(f.check) }) : undefined} color={SOURCE_COLOR.recomputed} />,
  ];
  if (f.kind === "subscription" || (f.kind === "" && f.meter)) {
    const r = f.real;
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
      const share = f.agents.reduce((a, x) => a + (x.realShare ?? 0), 0);
      tiles.push(<Tile key="real" label={t("costs.fig.shareOfReal")} value={`≈ ${fmtUSD(share)}`}
        sub={t("costs.fig.shareOfRealSub", { usd: fmtUSD(r.value) })} span={span} note={t("costs.fig.accountWideCaveat")} color={SOURCE_COLOR.real} hatched />);
    } else if (r && r.below) {
      tiles.push(<Tile key="real" label={t("costs.fig.realEstimate")} value={`< ${fmtUSD(r.high)}`}
        sub={t("costs.fig.realBelow")} span={span} note={t("costs.fig.accountWideCaveat")} color={SOURCE_COLOR.real} hatched />);
    } else if (r) {
      tiles.push(<Tile key="real" label={t("costs.fig.realEstimate")} value={`≈ ${fmtUSD(r.value)}`}
        sub={t("costs.fig.realRange", { low: fmtUSD(r.low), high: fmtUSD(r.high), pts: r.points, pp: pp != null ? fmtUSD(pp) : "–" })}
        span={span} note={t("costs.fig.accountWideCaveat")} color={SOURCE_COLOR.real} hatched />);
    } else {
      tiles.push(<Tile key="real" label={t("costs.fig.realEstimate")} value="–" sub={t("costs.fig.noMeter")} color={SOURCE_COLOR.real} hatched />);
    }
    if (f.multiple?.kind === "estimate") {
      tiles.push(<Tile key="mult" label={t("costs.fig.multiple")} value={`≈ ${times(f.multiple.value)}`}
        sub={t("costs.fig.multipleRange", { low: times(f.multiple.low), high: times(f.multiple.high) })}
        span={span} note={t("costs.fig.multipleHint")} />);
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
      // The bill covers whole billed buckets; the same window's reported and
      // recomputed figures stand beside it, not the page's.
      const win = b.window ? t("costs.fig.billedWindow", {
        from: fmtBucket(b.window.start.toISOString(), "hour"),
        unit: t(b.window.width === "1h" ? "costs.fig.billedUnitHours" : "costs.fig.billedUnitDays"),
        reported: fmtUSD(b.reported), list: fmtUSD(b.recomputed),
      }) : undefined;
      const failed = b.error ? t("costs.fig.billedLastSyncFailed", { error: b.error }) : undefined;
      tiles.push(<Tile key="billed" label={t("costs.fig.billed")} value={fmtUSD(b.usd)} sub={status}
        note={[win, failed].filter(Boolean).join(" · ") || undefined} color={SOURCE_COLOR.billed} />);
      tiles.push(<Tile key="list" label={t("costs.fig.listPerAnthropic")} value={fmtUSD(b.listUsd)} sub={status} />);
      tiles.push(<Tile key="ratio" label={t("costs.fig.billedPerList")} value={b.ratio != null ? `${fmtNumber(b.ratio * 100, 1)} %` : "–"}
        sub={t("costs.fig.billedPerListSub")} />);
    }
  }
  return <div className="flex flex-wrap gap-3 mb-2">{tiles}</div>;
}

/** Why there is no multiple, in words. */
function NoMultiple({ f }: { f: CredentialFigures }) {
  const { t } = useTranslation();
  if (f.kind === "api_key" || f.noMultiple == null || f.noMultiple === "meter") return null;
  let text: string;
  if (f.noMultiple === "agents") text = t("costs.fig.noMultipleAgents");
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
function CredentialBlock({ f, data, billed, bucket, fmtPeriod, selected, src, split, types, layout, sharedScale, asTable }: {
  f: CredentialFigures; data: CostCells; billed: BilledReport | undefined; bucket: string; fmtPeriod: (p: string) => string;
  selected: Set<string>; src: Record<Source, boolean>; split: Split; types: Set<string>; layout: Layout; sharedScale: boolean; asTable: boolean;
}) {
  const { t } = useTranslation();
  const planLabel = usePlanLabel();
  const filtered = selected.size > 0;
  const inScope = (id: string) => !filtered || selected.has(id);
  const agents = useMemo(() => rankAgents(data.agents), [data.agents]);
  const own = useMemo(() => data.cells.filter((c) => credId(c.secret_key, c.secret_slot) === f.id), [data.cells, f.id]);
  const steps = useMemo(() => (f.meter ? (data.subscription ?? []).filter((s) => s.secret_key === f.meter!.secret_key
    && s.secret_slot === f.meter!.secret_slot && s.source === f.meter!.source && s.window_kind === f.meter!.window_kind) : []), [data.subscription, f.meter]);
  // The bill inside the tile's window, in the chart's buckets.
  const bill = useMemo(() => (f.kind === "api_key" && f.secretKey != null
    ? billedPerPeriod(billed, f.secretKey, f.slot!, bucket) : new Map<number, number>()), [billed, f.kind, f.secretKey, f.slot, bucket]);

  // The periods: the cells', the meter's, the bill's — matched by instant.
  const periods = useMemo(() => {
    const m = new Map<number, string>();
    for (const c of own) m.set(Date.parse(c.period), c.period);
    for (const s of steps) if (!m.has(Date.parse(s.period))) m.set(Date.parse(s.period), s.period);
    for (const at of bill.keys()) if (!m.has(at)) m.set(at, new Date(at).toISOString());
    return [...m.entries()].sort((a, b) => a[0] - b[0]).map((e) => e[1]);
  }, [own, steps, bill]);
  const idx = useMemo(() => new Map(periods.map((p, i) => [Date.parse(p), i])), [periods]);

  // The cost-type filter belongs to the split by type, and its chips are
  // only on screen then: switched off (or per agent), every type counts.
  const byType = split === "type" && layout === "combined";
  const typeList = byType ? COST_TYPES.filter((k) => types.has(k)) : COST_TYPES;
  // Real bars only where every point has a plan price: a bar of the priced
  // part alone would read as the whole.
  const showReal = src.real && !filtered && f.kind !== "api_key" && f.meter != null && bucket !== "hour" && f.real?.priced === true;
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
  const billedPer = useMemo(() => {
    const v = new Array<number>(periods.length).fill(0);
    for (const [at, usd] of bill) {
      const i = idx.get(at);
      if (i != null) v[i] += usd;
    }
    return v;
  }, [bill, idx, periods.length]);
  const notes = useMemo(() => {
    if (!f.meter || bucket !== "hour" || f.kind === "api_key") return undefined;
    const m = new Map<number, string>();
    for (const [p, x] of meterCrossings(f.meter, steps)) {
      const i = idx.get(Date.parse(p));
      if (i != null) m.set(i, x.reset ? t("costs.fig.crossingReset", { to: x.to }) : t("costs.fig.crossing", { from: x.from, to: x.to }));
    }
    return m;
  }, [f.meter, f.kind, bucket, steps, idx, t]);

  const barsFor = (cs: CostCell[], i: number, withMoney: boolean): Bar[] => {
    const out: Bar[] = [];
    if (src.reported) {
      out.push({ key: "reported", label: t("costs.fig.reported"), color: SOURCE_COLOR.reported,
        segs: [{ key: "r", label: t("costs.fig.reported"), color: SOURCE_COLOR.reported, value: cs.reduce((a, c) => a + (c.engine_usd ?? 0), 0) }] });
    }
    if (src.recomputed) {
      const segs = byType
        ? typeList.map((k) => ({ key: k, label: t(`costs.stack.${k}`), color: TYPE_COLOR[k], value: cs.reduce((a, c) => a + (c.measured[k] ?? 0), 0) }))
        : [{ key: "m", label: t("costs.fig.recomputed"), color: SOURCE_COLOR.recomputed, value: cs.reduce((a, c) => a + recomputedOf(c, typeList), 0) }];
      out.push({ key: "recomputed", label: t("costs.fig.recomputed"), color: SOURCE_COLOR.recomputed, segs });
    }
    if (withMoney && showReal) {
      out.push({ key: "real", label: t("costs.fig.realEstimate"), color: SOURCE_COLOR.real, hatched: true,
        segs: [{ key: "real", label: t("costs.fig.realEstimate"), color: SOURCE_COLOR.real, value: realPer[i] }] });
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
  const sharedMax = Math.max(0, ...perAgent.flatMap((r) => r.groups.flatMap((g) => g.map((b) => sum(b.segs)))));
  const name = f.secretKey == null ? t("costs.fig.notAttributable") : `${f.secretKey} #${f.slot}`;
  const kind = t(`costs.fig.kind_${f.kind || "unknown"}`);
  const plan = planLabel(f);
  const subscription = f.kind === "subscription" || (f.kind === "" && f.meter != null);

  return (
    <section className="mb-5" aria-label={name} data-credential={f.id}>
      <div className="flex flex-wrap items-baseline gap-2 mb-2">
        <h3 style={{ fontSize: 15, fontWeight: 600 }}>{name}</h3>
        <span className="muted text-xs">{kind}{plan ? ` · ${plan}` : ""}</span>
      </div>
      <CredentialTiles f={f} filtered={filtered || layout === "agents"} />
      <NoMultiple f={f} />
      {f.bookingCheck.length > 0 && (
        <p className="text-xs mb-2 mt-0" role="note" style={{ color: "var(--text-warning)" }}>
          {t("costs.fig.bookingCheck", {
            count: f.bookingCheck.reduce((a, b) => a + b.entries, 0),
            usd: fmtUSD(f.bookingCheck.reduce((a, b) => a + b.usd, 0)),
            bases: [...new Set(f.bookingCheck.map((b) => b.basis))].join(", "),
          })}
        </p>
      )}
      {f.unattributed > 0.0005 && <p className="muted text-xs mb-2 mt-0">{t("costs.fig.unattributed", { usd: fmtUSD(f.unattributed) })}</p>}

      {periods.length === 0 ? (
        <div className="muted text-sm" style={{ padding: "20px 0", textAlign: "center" }}>{t("costs.empty")}</div>
      ) : layout === "combined" ? (
        <BarChart title={`${t("costs.chartCost")} · ${name}`} periods={periods} groups={combined} fmtPeriod={fmtPeriod}
          fmtValue={fmtUSD} fmtAxis={fmtUSDAxis} notes={notes} asTable={asTable} />
      ) : (
        <div style={{ display: "grid", gap: 6 }}>
          {perAgent.map((r, i) => (
            <div key={r.a.id}>
              <div className="text-xs" style={{ display: "flex", alignItems: "center", gap: 6 }}>
                <Swatch color={agentColor(agents, r.a.id)} /> <b>{r.a.label}</b>
              </div>
              <BarChart title={`${t("costs.chartCost")} · ${name} · ${r.a.label}`} periods={periods} groups={r.groups} fmtPeriod={fmtPeriod}
                fmtValue={fmtUSD} fmtAxis={fmtUSDAxis} height={i === perAgent.length - 1 ? 120 : 92}
                showX={i === perAgent.length - 1} max={sharedScale ? sharedMax : undefined} asTable={asTable} />
            </div>
          ))}
          {perAgent.length === 0 && <div className="muted text-sm">{t("costs.empty")}</div>}
        </div>
      )}
      {subscription && bucket === "hour" && src.real && !filtered && <p className="muted text-xs mt-1 mb-0">{t("costs.fig.hourRealNote")}</p>}
      {filtered && (subscription || f.kind === "api_key") && <p className="muted text-xs mt-1 mb-0">{t("costs.fig.filteredRealNote")}</p>}

      {f.agents.length > 0 && <AgentTable f={f} agents={agents} subscription={subscription} />}
    </section>
  );
}

/** The agents of one credential side by side, by recomputed list price. */
function AgentTable({ f, agents, subscription }: { f: CredentialFigures; agents: CellAgent[]; subscription: boolean }) {
  const { t } = useTranslation();
  const th: React.CSSProperties = { padding: "4px 8px 6px 0", fontWeight: 500, whiteSpace: "nowrap", textAlign: "right" };
  const td: React.CSSProperties = { padding: "3px 8px 3px 0", textAlign: "right", fontVariantNumeric: "tabular-nums", whiteSpace: "nowrap" };
  return (
    <div style={{ overflowX: "auto", marginTop: 8 }}>
      <table style={{ width: "100%", borderCollapse: "collapse", fontSize: 12 }}>
        <caption className="muted text-xs" style={{ textAlign: "left", paddingBottom: 4 }}>
          {t("costs.fig.agentsTitle")}
          {subscription && f.real?.priced && f.meter && <span data-span> · {realSpan(t, f, "costs.fig.shareOfRealSpan")}</span>}
        </caption>
        <thead>
          <tr className="muted">
            <th scope="col" style={{ ...th, textAlign: "left" }}>{t("costs.fig.agent")}</th>
            <th scope="col" style={th}>{t("costs.fig.reported")}</th>
            <th scope="col" style={th}>{t("costs.fig.recomputed")}</th>
            <th scope="col" style={th}>{t("costs.fig.gap")}</th>
            <th scope="col" style={th}>{t("costs.fig.share")}</th>
            {subscription && <th scope="col" style={th}>{t("costs.fig.shareOfReal")}</th>}
          </tr>
        </thead>
        <tbody>
          {f.agents.map((a) => (
            <tr key={a.id} style={{ borderTop: "1px solid var(--border)" }}>
              <th scope="row" style={{ ...td, textAlign: "left", fontWeight: 400 }}>
                <span style={{ display: "inline-flex", alignItems: "center", gap: 6 }}><Swatch color={agentColor(agents, a.id)} /> {a.label}</span>
              </th>
              <td style={td}>{fmtUSD(a.reported)}</td>
              <td style={td}>{fmtUSD(a.recomputed)}</td>
              <td style={td}>{a.gapPct != null ? pct(a.gapPct) : "–"}</td>
              <td style={td}>{a.share != null ? `${fmtNumber(a.share * 100, 1)} %` : "–"}</td>
              {subscription && <td style={td}>{a.realShare != null ? `≈ ${fmtUSD(a.realShare)}` : "–"}</td>}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/** The colour code of the page, explained once. */
function ColourKey() {
  const { t } = useTranslation();
  const items: [Source, string, boolean?][] = [
    ["reported", t("costs.fig.keyReported")],
    ["recomputed", t("costs.fig.keyRecomputed")],
    ["real", t("costs.fig.keyReal"), true],
    ["billed", t("costs.fig.keyBilled")],
  ];
  return (
    <div className="flex flex-wrap items-center gap-3 text-xs muted mb-3" role="note" aria-label={t("costs.fig.key")}>
      {items.map(([s, label, h]) => (
        <span key={s} style={{ display: "inline-flex", alignItems: "center", gap: 6 }}><Swatch color={SOURCE_COLOR[s]} hatched={h} /> {label}</span>
      ))}
    </div>
  );
}

export function CostExplorer({ data, billed, selected, bucket, fmtPeriod }: {
  data: CostCells | undefined; billed?: BilledReport; selected: Set<string>; bucket: string; fmtPeriod: (p: string) => string;
}) {
  const { t } = useTranslation();
  const [layout, setLayout] = useState<Layout>("combined");
  const [split, setSplit] = useState<Split>("none");
  const [sharedScale, setSharedScale] = useState(true);
  const [asTable, setAsTable] = useState(false);
  const [src, setSrc] = useState<Record<Source, boolean>>({ reported: true, recomputed: true, real: true, billed: true });
  const [types, setTypes] = useState<Set<string>>(new Set(COST_TYPES));

  const figures = useMemo(() => (data ? credentialFigures({
    cells: data, billed, bucketMs: BUCKET_MS[bucket] ?? BUCKET_MS.day, windowEnd: new Date(), agents: selected, layout,
  }) : []), [data, billed, bucket, selected, layout]);

  const toggleType = (k: string) => setTypes((s) => { const n = new Set(s); if (n.has(k)) n.delete(k); else n.add(k); return n; });
  const toggleSrc = (s: Source) => setSrc((v) => {
    const n = { ...v, [s]: !v[s] };
    return n.reported || n.recomputed || n.real || n.billed ? n : v; // keep at least one
  });
  const hint = (s: Source) => t(s === "reported" ? "costs.filter.reportedHint" : s === "recomputed" ? "costs.filter.measuredHint"
    : s === "real" ? "costs.filter.subscriptionHint" : "costs.filter.billedHint");

  return (
    <div>
      <ColourKey />
      <Row label={t("costs.filter.source")}>
        {(["reported", "recomputed", "real", "billed"] as Source[]).map((s) => (
          <Chip key={s} on={src[s]} color={SOURCE_COLOR[s]} hatched={s === "real"} onClick={() => toggleSrc(s)}>
            {t(s === "real" ? "costs.fig.realEstimate" : `costs.fig.${s}`)}
          </Chip>
        ))}
      </Row>
      <details className="text-xs muted mb-2" style={{ marginLeft: 92 }}>
        <summary style={{ cursor: "pointer" }}>{t("costs.filter.sourceHints")}</summary>
        <ul style={{ margin: "4px 0 0 0", paddingLeft: 16, maxWidth: 760 }}>
          {(["reported", "recomputed", "real", "billed"] as Source[]).map((s) => <li key={s}>{hint(s)}</li>)}
        </ul>
      </details>
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
      {layout === "combined" && (
        <Row label={t("costs.filter.split")}>
          {(["none", "type"] as Split[]).map((s) => (
            <Chip key={s} on={split === s} onClick={() => setSplit(s)}>{t(`costs.filter.split_${s}`)}</Chip>
          ))}
        </Row>
      )}
      {src.recomputed && layout === "combined" && split === "type" && (
        <Row label={t("costs.filter.types")}>
          {COST_TYPES.map((k) => (
            <Chip key={k} on={types.has(k)} color={TYPE_COLOR[k]} onClick={() => toggleType(k)}>{t(`costs.stack.${k}`)}</Chip>
          ))}
        </Row>
      )}

      {figures.length === 0 ? (
        <div className="muted text-sm" style={{ padding: "40px 0", textAlign: "center" }}>{t("costs.empty")}</div>
      ) : figures.map((f) => (
        <CredentialBlock key={f.id} f={f} data={data!} billed={billed} bucket={bucket} fmtPeriod={fmtPeriod} selected={selected}
          src={src} split={split} types={types} layout={layout} sharedScale={sharedScale} asTable={asTable} />
      ))}
    </div>
  );
}

/** Tokens per type over time, from the same lines as the dollars above, over
 *  every credential, narrowed by the agent filter. */
export function TokenExplorer({ data, selected, fmtPeriod }: {
  data: CostCells | undefined; selected: Set<string>; fmtPeriod: (p: string) => string;
}) {
  const { t } = useTranslation();
  const [asTable, setAsTable] = useState(false);
  const { periods, groups } = useMemo(() => {
    const by = new Map<number, { p: string; tok: Record<string, number> }>();
    for (const c of data?.cells ?? []) {
      if (selected.size > 0 && !selected.has(c.agent_id)) continue;
      const k = Date.parse(c.period);
      const e = by.get(k) ?? { p: c.period, tok: {} };
      for (const [ty, v] of Object.entries(c.tokens ?? {})) e.tok[ty] = (e.tok[ty] ?? 0) + v;
      by.set(k, e);
    }
    const rows = [...by.entries()].sort((a, b) => a[0] - b[0]).map((e) => e[1]);
    return {
      periods: rows.map((r) => r.p),
      groups: rows.map((r) => [{
        key: "tokens", label: t("costs.mTokens"), color: TYPE_COLOR.output,
        segs: TOKEN_TYPES.map((ty) => ({ key: ty, label: t(`costs.tok.${ty}`), color: TYPE_COLOR[ty], value: r.tok[ty] ?? 0 })),
      }] as Bar[]),
    };
  }, [data, selected, t]);
  if (periods.length === 0) return <div className="muted text-sm" style={{ padding: "40px 0", textAlign: "center" }}>{t("costs.empty")}</div>;
  return (
    <div>
      <div className="flex flex-wrap items-center gap-3 text-xs muted mb-2">
        {TOKEN_TYPES.map((ty) => (
          <span key={ty} style={{ display: "inline-flex", alignItems: "center", gap: 6 }}><Swatch color={TYPE_COLOR[ty]} /> {t(`costs.tok.${ty}`)}</span>
        ))}
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
