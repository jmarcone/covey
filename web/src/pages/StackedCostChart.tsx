import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { api } from "../api";
import { fmtUSD } from "../format";

/* The cost explorer: one chart, combinable filters instead of tabs.
 *
 * Three sources, and the difference between them is the point of the page:
 *   reported      — the list price Claude Code states per run, as covey books it;
 *   recalculated  — the same runs' raw tokens priced per type with the list:
 *                   the check on the first (they should match);
 *   real          — what the work really used of a flat-rate seat: the
 *                   provider's weekly meter movement × the plan's weekly price.
 *                   The meter is account-wide: per agent it is allocated by each
 *                   agent's share of the period's list price, and labelled so.
 *
 * Two layouts: combined (one group of bars per period, optionally split by
 * agent or cost type) and per agent (a row per agent on the same time axis,
 * the three sources side by side in every period). */

interface CellAgent {
  id: string;
  label: string;
  reported_usd: number;
}
/** One period × agent × credential (the secret value that paid; null: not attributable). */
interface CostCell {
  period: string;
  agent_id: string;
  secret_key?: string | null;
  secret_slot?: number | null;
  credential_kind?: string;
  reported_usd: number;
  measured: Record<string, number>;
}
interface MeterStep {
  period: string;
  points: number;
}
export interface CostCells {
  periods: string[];
  agents: CellAgent[];
  types: string[];
  cells: CostCell[];
  subscription: MeterStep[] | null;
}

export type Split = "agent" | "type" | "none";
type Source = "reported" | "measured" | "subscription";
type Layout = "combined" | "agents";

/** Cost types: one blue family from light (cheap, plentiful) to dark (dear). Shared
 *  with the cost-types card and the token chart. Fixed values: the theme's accent
 *  and clay were both orange and could not be told apart. */
export const TYPE_COLOR: Record<string, string> = {
  cache_read: "#9db8de",
  cache_write: "#5f8fd1",
  input: "#b9bec7",
  output: "#2c5aa0",
  web_search: "#8c7cc8",
  unpriced: "#a3a8b0",
  unattributed: "#80858d",
};
/** Agents: a qualitative palette (Tableau 10): distinct, calm, colour-blind tolerant. */
const AGENT_PALETTE = ["#4e79a7", "#f28e2b", "#59a14f", "#e15759", "#76b7b2", "#edc948", "#b07aa1", "#ff9da7", "#9c755f", "#bab0ac"];
/** Sources: grey = list price as reported, blue = list price recalculated, green = real money. */
const SOURCE_COLOR: Record<Source, string> = { reported: "#8a8f98", measured: "#4e79a7", subscription: "#3aa76d" };

const PRICE_KEY = "covey.costs.planPriceMonthly";
function loadPrice(): number {
  try {
    const v = Number(localStorage.getItem(PRICE_KEY));
    return v > 0 ? v : 200;
  } catch {
    return 200;
  }
}

export function useCostCells(days: number, bucket: string) {
  return useQuery({
    queryKey: ["cost", "cells", days, bucket],
    queryFn: () => api<CostCells>(`/cost/cells?days=${days}&bucket=${bucket}`),
    refetchInterval: 30000,
  });
}

/** Agent colour by rank (the server ranks costliest first). */
export function agentColor(agents: { id: string }[], id: string) {
  const i = agents.findIndex((a) => a.id === id);
  return AGENT_PALETTE[(i < 0 ? 0 : i) % AGENT_PALETTE.length];
}

function Chip({ on, color, children, onClick, onDoubleClick, title }: {
  on: boolean; color?: string; children: React.ReactNode; onClick: () => void; onDoubleClick?: () => void; title?: string;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      onDoubleClick={onDoubleClick}
      aria-pressed={on}
      title={title}
      className="text-xs"
      style={{
        display: "inline-flex", alignItems: "center", gap: 6, padding: "4px 10px", borderRadius: 6,
        border: `1px solid ${on ? "var(--border-strong)" : "var(--border)"}`,
        background: on ? "var(--surface-2)" : "transparent",
        color: on ? "var(--text-primary)" : "var(--text-muted)",
        cursor: "pointer", lineHeight: 1.2,
      }}
    >
      {color && <span style={{ width: 8, height: 8, borderRadius: 2, background: color, opacity: on ? 1 : 0.4, display: "inline-block" }} />}
      {children}
    </button>
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
  const toggle = (id: string) => {
    const n = new Set(selected);
    if (n.has(id)) n.delete(id);
    else n.add(id);
    onChange(n);
  };
  return (
    <Row label={t("costs.filter.agents")}>
      <Chip on={selected.size === 0} onClick={() => onChange(new Set())}>{t("costs.filter.all")}</Chip>
      {agents.map((a) => (
        <Chip key={a.id} on={selected.has(a.id)} color={agentColor(agents, a.id)}
          onClick={() => toggle(a.id)} onDoubleClick={() => onChange(new Set([a.id]))} title={t("costs.filter.toggleHint")}>
          {a.label} <span style={{ opacity: 0.6 }}>{fmtUSD(a.reported_usd)}</span>
        </Chip>
      ))}
    </Row>
  );
}

function Tile({ label, value, sub, color }: { label: string; value: string; sub?: string; color?: string }) {
  return (
    <div style={{ padding: "10px 14px", borderRadius: 8, border: "1px solid var(--border)", minWidth: 170, flex: "1 1 170px" }}>
      <div className="muted text-xs" style={{ display: "flex", alignItems: "center", gap: 6 }}>
        {color && <span style={{ width: 8, height: 8, borderRadius: 2, background: color }} />}
        {label}
      </div>
      <div style={{ fontSize: 20, fontWeight: 600, marginTop: 2, fontVariantNumeric: "tabular-nums" }}>{value}</div>
      {sub && <div className="muted text-xs" style={{ marginTop: 2 }}>{sub}</div>}
    </div>
  );
}

/** A "nice" axis: the top and a step that read as round numbers. */
function niceAxis(max: number) {
  const raw = Math.max(1e-9, max) / 4;
  const mag = Math.pow(10, Math.floor(Math.log10(raw)));
  const n = raw / mag;
  const step = (n <= 1 ? 1 : n <= 2 ? 2 : n <= 2.5 ? 2.5 : n <= 5 ? 5 : 10) * mag;
  const top = Math.ceil(Math.max(1e-9, max) / step) * step;
  const decimals = step >= 1 ? (Number.isInteger(step) ? 0 : 1) : Math.min(4, Math.ceil(-Math.log10(step)) + (step * Math.pow(10, Math.ceil(-Math.log10(step))) % 1 ? 1 : 0));
  const ticks: number[] = [];
  for (let v = 0; v <= top + step / 2; v += step) ticks.push(v);
  return { top, ticks, fmt: (v: number) => `$${v.toFixed(decimals)}` };
}
const short = (v: number) => (v >= 100 ? `$${v.toFixed(0)}` : v >= 10 ? `$${v.toFixed(1)}` : v >= 0.1 ? `$${v.toFixed(2)}` : `$${v.toFixed(3)}`);

type Seg = { key: string; label: string; color: string; usd: number };
type Bar = { src: Source; segs: Seg[] };

/** One chart: groups of bars over the periods. Used once (combined) or once per agent. */
function Bars({ periods, groups, height, fmtPeriod, showX, allocatedNote }: {
  periods: string[]; groups: Bar[][]; height: number; fmtPeriod: (p: string) => string; showX: boolean; allocatedNote?: boolean;
}) {
  const { t } = useTranslation();
  const [hover, setHover] = useState<number | null>(null);
  const padL = 64, padR = 16, padT = 18, padB = showX ? 34 : 8;
  const plotH = height - padT - padB;
  const n = periods.length;
  const nb = Math.max(1, groups[0]?.length ?? 1);
  const barW = nb === 1 ? 28 : nb === 2 ? 20 : 16, gap = 3;
  const slot = Math.max(nb * (barW + gap) + 24, n <= 1 ? 160 : Math.min(120, Math.round(900 / Math.max(1, n))));
  const W = Math.max(560, padL + padR + Math.max(1, n) * slot);
  const sum = (s: Seg[]) => s.reduce((a, x) => a + x.usd, 0);
  const ax = niceAxis(Math.max(0, ...groups.flatMap((g) => g.map((b) => sum(b.segs)))));
  const y = (v: number) => padT + plotH - (v / ax.top) * plotH;
  const every = Math.ceil(n / Math.max(1, Math.floor((W - padL - padR) / 84)));
  const tipW = 240;
  const hx = hover != null ? padL + hover * slot + slot / 2 : 0;
  const tipLeft = hover == null ? 0 : Math.max(0, Math.min(hx + 16 + tipW > W ? hx - 16 - tipW : hx + 16, W - tipW));
  return (
    <div style={{ position: "relative", overflowX: "auto" }}>
      <svg width={W} height={height} style={{ display: "block", maxWidth: "none" }} role="img" aria-label={t("costs.chartCost")}>
        {ax.ticks.map((v, i) => (
          <g key={i}>
            <line x1={padL} x2={W - padR} y1={y(v)} y2={y(v)} stroke="var(--border)" strokeDasharray={i === 0 ? undefined : "2 4"} />
            <text x={padL - 8} y={y(v) + 4} textAnchor="end" fontSize={11} fill="var(--text-muted)">{ax.fmt(v)}</text>
          </g>
        ))}
        {groups.map((bars, i) => {
          const groupW = bars.length * barW + (bars.length - 1) * gap;
          const x0 = padL + i * slot + slot / 2 - groupW / 2;
          return (
            <g key={periods.at(i)} onMouseEnter={() => setHover(i)} onMouseLeave={() => setHover(null)}>
              <rect x={padL + i * slot} y={padT} width={slot} height={plotH} fill={hover === i ? "var(--surface-2)" : "transparent"} opacity={0.6} />
              {bars.map((b, bi) => {
                const bx = x0 + bi * (barW + gap);
                let acc = 0;
                const total = sum(b.segs);
                return (
                  <g key={b.src}>
                    {b.segs.map((g) => {
                      if (g.usd <= 0) return null;
                      const top = y(acc + g.usd), h = Math.max(1, y(acc) - top);
                      acc += g.usd;
                      return <rect key={g.key} x={bx} y={top} width={barW} height={h} rx={1.5} fill={g.color} />;
                    })}
                    {total > 0 && (
                      <text x={bx + barW / 2} y={Math.max(10, y(total) - 4)} textAnchor="middle" fontSize={9.5} fill="var(--text-muted)">{short(total)}</text>
                    )}
                  </g>
                );
              })}
            </g>
          );
        })}
        {showX && periods.map((p, i) =>
          i % every !== 0 && i !== n - 1 ? null : (
            <text key={p} x={padL + i * slot + slot / 2} y={height - padB + 18} textAnchor="middle" fontSize={11} fill="var(--text-muted)">{fmtPeriod(p)}</text>
          ),
        )}
      </svg>
      {hover != null && groups.at(hover) && (
        <div style={{
          position: "absolute", left: tipLeft, top: 4, width: tipW, pointerEvents: "none", zIndex: 5,
          background: "var(--surface-1)", border: "1px solid var(--border-strong)", borderRadius: 8,
          padding: "8px 10px", fontSize: 12, boxShadow: "0 6px 20px var(--shadow-md)",
        }}>
          <div className="muted" style={{ fontSize: 11, marginBottom: 6 }}>{fmtPeriod(periods.at(hover) ?? "")}</div>
          {groups.at(hover)!.map((b) => (
            <div key={b.src} style={{ marginBottom: 6 }}>
              <div style={{ display: "flex", justifyContent: "space-between", fontWeight: 600, gap: 8 }}>
                <span><span style={{ color: SOURCE_COLOR[b.src] }}>■</span> {t(`costs.filter.${b.src}`)}{b.src === "subscription" && allocatedNote ? " *" : ""}</span>
                <span>{fmtUSD(sum(b.segs))}</span>
              </div>
              {b.segs.filter((x) => x.usd > 0).length > 1 && b.segs.filter((x) => x.usd > 0).map((x) => (
                <div key={x.key} className="muted" style={{ display: "flex", justifyContent: "space-between", gap: 12, paddingLeft: 12 }}>
                  <span><span style={{ color: x.color }}>■</span> {x.label}</span>
                  <span>{fmtUSD(x.usd)}</span>
                </div>
              ))}
            </div>
          ))}
          {(() => {
            const g = groups.at(hover)!;
            const rep = g.find((b) => b.src === "reported"), sub = g.find((b) => b.src === "subscription");
            const r = rep ? sum(rep.segs) : 0, s = sub ? sum(sub.segs) : 0;
            return r > 0 && s > 0 ? <div className="muted" style={{ fontSize: 11 }}>{t("costs.filter.hourMultiple", { x: (r / s).toFixed(0) })}</div> : null;
          })()}
        </div>
      )}
    </div>
  );
}

export function CostExplorer({ data, selected, fmtPeriod }: {
  data: CostCells | undefined; selected: Set<string>; fmtPeriod: (p: string) => string;
}) {
  const { t } = useTranslation();
  const [layout, setLayout] = useState<Layout>("combined");
  const [split, setSplit] = useState<Split>("none");
  const [src, setSrc] = useState<Record<Source, boolean>>({ reported: true, measured: true, subscription: true });
  const [types, setTypes] = useState<Set<string>>(new Set(["cache_read", "cache_write", "input", "output", "web_search", "unpriced", "unattributed"]));
  const [price, setPriceState] = useState<number>(loadPrice);
  const setPrice = (v: number) => {
    setPriceState(v);
    try { localStorage.setItem(PRICE_KEY, String(v)); } catch { /* private window */ }
  };
  const perPoint = (price * 12) / 52 / 100; // $ per percentage point of the weekly limit

  const agents = data?.agents ?? [];
  const inScope = (id: string) => selected.size === 0 || selected.has(id);
  const typeList = (data?.types ?? []).filter((k) => types.has(k));
  const subByPeriod = useMemo(() => new Map((data?.subscription ?? []).map((s) => [s.period, s.points])), [data]);
  const periods = useMemo(() => {
    const s = new Set([...(data?.periods ?? []), ...(data?.subscription ?? []).map((x) => x.period)]);
    return [...s].sort();
  }, [data]);
  /** Cells per period, one per agent: the API splits an agent's period by credential,
   *  this chart does not, so those parts are added back together. */
  const cellsBy = useMemo(() => {
    const m = new Map<string, CostCell[]>();
    for (const c of data?.cells ?? []) {
      const l = m.get(c.period) ?? [];
      const same = l.find((x) => x.agent_id === c.agent_id);
      if (!same) {
        l.push({ ...c, measured: { ...c.measured } });
      } else {
        same.reported_usd += c.reported_usd;
        for (const [k, v] of Object.entries(c.measured)) same.measured[k] = (same.measured[k] ?? 0) + v;
      }
      m.set(c.period, l);
    }
    return m;
  }, [data]);
  const measuredOf = (c: CostCell) => typeList.reduce((a, k) => a + (c.measured[k] ?? 0), 0);
  /** Real cost of one period, allocated to one agent by its share of the period's reported cost. */
  const realOf = (p: string, agentId?: string) => {
    const real = (subByPeriod.get(p) ?? 0) * perPoint;
    if (!agentId) return real;
    const cs = cellsBy.get(p) ?? [];
    const all = cs.reduce((a, c) => a + c.reported_usd, 0);
    const mine = cs.find((c) => c.agent_id === agentId)?.reported_usd ?? 0;
    return all > 0 ? (real * mine) / all : 0;
  };

  const scoped = agents.filter((a) => inScope(a.id));
  const combined: Bar[][] = useMemo(() => periods.map((p) => {
    const cs = (cellsBy.get(p) ?? []).filter((c) => inScope(c.agent_id));
    const list = (s: "reported" | "measured"): Seg[] => {
      if (split === "agent" || (split === "type" && s === "reported")) {
        return scoped.map((a) => {
          const c = cs.find((x) => x.agent_id === a.id);
          return { key: a.id, label: a.label, color: agentColor(agents, a.id), usd: !c ? 0 : s === "reported" ? c.reported_usd : measuredOf(c) };
        });
      }
      if (split === "type") {
        return typeList.map((k) => ({ key: k, label: t(`costs.stack.${k}`), color: TYPE_COLOR[k] ?? "#888", usd: cs.reduce((a, c) => a + (c.measured[k] ?? 0), 0) }));
      }
      return [{ key: s, label: t(`costs.filter.${s}`), color: SOURCE_COLOR[s], usd: cs.reduce((a, c) => a + (s === "reported" ? c.reported_usd : measuredOf(c)), 0) }];
    };
    const real = selected.size === 0 ? realOf(p) : scoped.reduce((a, ag) => a + realOf(p, ag.id), 0);
    return [
      src.reported ? { src: "reported" as Source, segs: list("reported") } : null,
      src.measured ? { src: "measured" as Source, segs: list("measured") } : null,
      src.subscription ? { src: "subscription" as Source, segs: [{ key: "real", label: t("costs.filter.subscription"), color: SOURCE_COLOR.subscription, usd: real }] } : null,
    ].filter(Boolean) as Bar[];
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }), [periods, cellsBy, selected, split, src, types, perPoint, agents, t]);

  const perAgent = useMemo(() => scoped.map((a) => ({
    agent: a,
    groups: periods.map((p) => {
      const c = (cellsBy.get(p) ?? []).find((x) => x.agent_id === a.id);
      return [
        src.reported ? { src: "reported" as Source, segs: [{ key: "r", label: "", color: SOURCE_COLOR.reported, usd: c?.reported_usd ?? 0 }] } : null,
        src.measured ? { src: "measured" as Source, segs: typeList.map((k) => ({ key: k, label: t(`costs.stack.${k}`), color: TYPE_COLOR[k] ?? "#888", usd: c?.measured[k] ?? 0 })) } : null,
        src.subscription ? { src: "subscription" as Source, segs: [{ key: "s", label: "", color: SOURCE_COLOR.subscription, usd: realOf(p, a.id) }] } : null,
      ].filter(Boolean) as Bar[];
    }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
  })).filter((r) => r.groups.some((g) => g.some((b) => b.segs.some((s) => s.usd > 0)))), [scoped, periods, cellsBy, src, types, perPoint, t]);

  const totals = useMemo(() => {
    const out: Record<Source, number> = { reported: 0, measured: 0, subscription: 0 };
    for (const c of data?.cells ?? []) {
      if (!inScope(c.agent_id)) continue;
      out.reported += c.reported_usd;
      out.measured += measuredOf(c);
    }
    out.subscription = selected.size === 0 ? periods.reduce((a, p) => a + realOf(p), 0)
      : periods.reduce((a, p) => a + scoped.reduce((b, ag) => b + realOf(p, ag.id), 0), 0);
    return out;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [data, selected, types, perPoint, periods]);
  const points = (data?.subscription ?? []).reduce((a, s) => a + s.points, 0);
  const allTypes = types.size === (data?.types ?? []).length;
  const allocated = selected.size > 0 || layout === "agents";

  const toggleType = (k: string) => setTypes((s) => { const n = new Set(s); if (n.has(k)) n.delete(k); else n.add(k); return n; });
  const toggleSrc = (s: Source) => setSrc((v) => {
    const n = { ...v, [s]: !v[s] };
    return n.reported || n.measured || n.subscription ? n : v; // keep at least one
  });

  return (
    <div>
      {/* The three figures and the multiple: the finding first */}
      <div className="flex flex-wrap gap-3 mb-4">
        <Tile label={t("costs.filter.reported")} value={fmtUSD(totals.reported)} sub={t("costs.filter.listPrice")} color={SOURCE_COLOR.reported} />
        <Tile label={t("costs.filter.measured")} value={fmtUSD(totals.measured)}
          sub={allTypes && totals.reported > 0 ? t("costs.filter.diff", { pct: (((totals.measured - totals.reported) / totals.reported) * 100).toFixed(2) }) : t("costs.filter.listPrice")}
          color={SOURCE_COLOR.measured} />
        <Tile label={t("costs.filter.subscription") + (allocated ? " *" : "")} value={fmtUSD(totals.subscription)}
          sub={t("costs.filter.subSub", { pts: points.toFixed(0), per: fmtUSD(perPoint) })} color={SOURCE_COLOR.subscription} />
        {totals.subscription > 0 && (
          <Tile label={t("costs.filter.multiple")} value={`${(totals.reported / totals.subscription).toFixed(0)}×`} sub={t("costs.filter.multipleSub")} />
        )}
      </div>

      <Row label={t("costs.filter.layout")}>
        <Chip on={layout === "combined"} onClick={() => setLayout("combined")}>{t("costs.filter.layout_combined")}</Chip>
        <Chip on={layout === "agents"} onClick={() => setLayout("agents")}>{t("costs.filter.layout_agents")}</Chip>
      </Row>
      <Row label={t("costs.filter.source")}>
        {(["reported", "measured", "subscription"] as Source[]).map((s) => (
          <Chip key={s} on={src[s]} color={SOURCE_COLOR[s]} onClick={() => toggleSrc(s)} title={t(`costs.filter.${s}Hint`)}>{t(`costs.filter.${s}`)}</Chip>
        ))}
        <label className="text-xs muted" style={{ marginLeft: 8, display: "inline-flex", alignItems: "center", gap: 6 }}>
          {t("costs.filter.planPrice")}
          <input type="number" min={1} step={1} value={price} onChange={(e) => setPrice(Math.max(1, Number(e.target.value) || 1))}
            style={{ width: 72, padding: "3px 6px", borderRadius: 6, border: "1px solid var(--border)", background: "transparent", color: "inherit" }} />
        </label>
      </Row>
      {layout === "combined" && (
        <Row label={t("costs.filter.split")}>
          {(["none", "agent", "type"] as Split[]).map((s) => (
            <Chip key={s} on={split === s} onClick={() => setSplit(s)}>{t(`costs.filter.split_${s}`)}</Chip>
          ))}
        </Row>
      )}
      {src.measured && (layout === "agents" || split === "type") && (
        <Row label={t("costs.filter.types")}>
          {(data?.types ?? []).map((k) => (
            <Chip key={k} on={types.has(k)} color={TYPE_COLOR[k]} onClick={() => toggleType(k)}>{t(`costs.stack.${k}`)}</Chip>
          ))}
        </Row>
      )}

      {periods.length === 0 ? (
        <div className="muted text-sm" style={{ padding: "40px 0", textAlign: "center" }}>{t("costs.empty")}</div>
      ) : layout === "combined" ? (
        <Bars periods={periods} groups={combined} height={300} fmtPeriod={fmtPeriod} showX allocatedNote={allocated} />
      ) : (
        <div style={{ display: "grid", gap: 10, marginTop: 4 }}>
          {perAgent.map((r, i) => (
            <div key={r.agent.id}>
              <div className="text-xs" style={{ display: "flex", alignItems: "center", gap: 6, marginBottom: 2 }}>
                <span style={{ width: 8, height: 8, borderRadius: 2, background: agentColor(agents, r.agent.id) }} />
                <b>{r.agent.label}</b>
              </div>
              <Bars periods={periods} groups={r.groups} height={i === perAgent.length - 1 ? 150 : 118} fmtPeriod={fmtPeriod}
                showX={i === perAgent.length - 1} allocatedNote />
            </div>
          ))}
          {perAgent.length === 0 && <div className="muted text-sm">{t("costs.empty")}</div>}
        </div>
      )}
      <p className="muted text-xs mt-2 mb-0" style={{ maxWidth: 760 }}>
        {t("costs.filter.legendNote")}
        {allocated && <> {t("costs.filter.allocatedNote")}</>}
        {layout === "combined" && split === "type" && src.reported && <> {t("costs.filter.reportedNoTypes")}</>}
        {layout === "agents" && <> {t("costs.filter.agentsScaleNote")}</>}
      </p>
    </div>
  );
}
