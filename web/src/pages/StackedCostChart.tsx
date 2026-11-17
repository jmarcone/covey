import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { api } from "../api";
import { fmtUSD } from "../format";

/* The cost explorer: one chart, combinable filters instead of tabs.
 *
 *   agents  — any number (none selected = everyone); set on the page, shared
 *             with the cards below;
 *   split   — by agent, by cost type, or not at all;
 *   source  — Claude-reported (what the engine said and covey booked) and/or
 *             measured (raw tokens priced per type with covey's list); with
 *             both on, every period shows the two side by side;
 *   types   — which cost types count. Only the measured figure has types: the
 *             engine reports one dollar figure per model, not per token type.
 *
 * All filtering happens here on one data set (GET /cost/cells), so switching
 * is instant. */

interface CellAgent {
  id: string;
  label: string;
  reported_usd: number;
}
interface CostCell {
  period: string;
  agent_id: string;
  reported_usd: number;
  measured: Record<string, number>;
}
interface CostCells {
  periods: string[];
  agents: CellAgent[];
  types: string[];
  cells: CostCell[];
}

export type Split = "agent" | "type" | "none";

/** Cost-type colours, shared with the "Cost types" card and the token chart.
 *  Fixed values on purpose: the theme's accent and "clay" are both orange and
 *  could not be told apart side by side. */
export const TYPE_COLOR: Record<string, string> = {
  cache_read: "#e0a07a",
  cache_write: "#6fae8a",
  input: "#9a9a9a",
  output: "#6f8fd0",
  web_search: "#c9a227",
  unattributed: "#5a5a5a",
};
const AGENT_PALETTE = ["#d4876b", "#7aa98f", "#7f9cc9", "#c9a227", "#a98fc9", "#c97f9c", "#6bb8c9", "#9c9c6b", "#c9906b", "#8f8f8f"];
const REPORTED_COLOR = "var(--text-muted, #888)";

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
        display: "inline-flex", alignItems: "center", gap: 6, padding: "3px 9px", borderRadius: 999,
        border: "0.5px solid var(--border-strong)", background: on ? "var(--surface-2)" : "transparent",
        opacity: on ? 1 : 0.5, cursor: "pointer",
      }}
    >
      {color && <span style={{ width: 9, height: 9, borderRadius: 2, background: color, display: "inline-block" }} />}
      {children}
    </button>
  );
}

/** The agent filter: chips, any combination; none selected means everyone. */
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
    <div className="flex flex-wrap items-center gap-2" role="group" aria-label={t("costs.filter.agents")}>
      <span className="muted text-xs" style={{ minWidth: 64 }}>{t("costs.filter.agents")}</span>
      <Chip on={selected.size === 0} onClick={() => onChange(new Set())}>{t("costs.filter.all")}</Chip>
      {agents.map((a) => (
        <Chip key={a.id} on={selected.has(a.id)} color={agentColor(agents, a.id)}
          onClick={() => toggle(a.id)} onDoubleClick={() => onChange(new Set([a.id]))} title={t("costs.filter.toggleHint")}>
          {a.label} <span className="muted">{fmtUSD(a.reported_usd)}</span>
        </Chip>
      ))}
    </div>
  );
}

export function CostExplorer({ data, selected, fmtPeriod }: {
  data: CostCells | undefined; selected: Set<string>; fmtPeriod: (p: string) => string;
}) {
  const { t } = useTranslation();
  const [split, setSplit] = useState<Split>("agent");
  const [reported, setReported] = useState(true);
  const [measured, setMeasured] = useState(false);
  const [types, setTypes] = useState<Set<string>>(new Set(["cache_read", "cache_write", "input", "output", "web_search", "unattributed"]));
  const [hover, setHover] = useState<number | null>(null);

  const agents = data?.agents ?? [];
  const periods = data?.periods ?? [];
  const inScope = (id: string) => selected.size === 0 || selected.has(id);
  const typeList = (data?.types ?? []).filter((k) => types.has(k));

  // One stack per period and source: [{key, label, color, usd}].
  type Seg = { key: string; label: string; color: string; usd: number };
  const stacks = useMemo(() => {
    const byPeriod = new Map<string, CostCell[]>();
    for (const c of data?.cells ?? []) {
      if (!inScope(c.agent_id)) continue;
      const l = byPeriod.get(c.period) ?? [];
      l.push(c);
      byPeriod.set(c.period, l);
    }
    const measuredOf = (c: CostCell) => typeList.reduce((a, k) => a + (c.measured[k] ?? 0), 0);
    return periods.map((p) => {
      const cs = byPeriod.get(p) ?? [];
      const build = (src: "reported" | "measured"): Seg[] => {
        if (split === "agent" || (split === "type" && src === "reported")) {
          // Reported has no types: split by type falls back to per agent for it.
          return agents
            .filter((a) => inScope(a.id))
            .map((a) => {
              const c = cs.find((x) => x.agent_id === a.id);
              const usd = !c ? 0 : src === "reported" ? c.reported_usd : measuredOf(c);
              return { key: a.id, label: a.label, color: agentColor(agents, a.id), usd };
            });
        }
        if (split === "type") {
          return typeList.map((k) => ({
            key: k, label: t(`costs.stack.${k}`), color: TYPE_COLOR[k] ?? "#888",
            usd: cs.reduce((a, c) => a + (c.measured[k] ?? 0), 0),
          }));
        }
        const usd = cs.reduce((a, c) => a + (src === "reported" ? c.reported_usd : measuredOf(c)), 0);
        return [{ key: src, label: t(`costs.filter.${src}`), color: src === "reported" ? REPORTED_COLOR : "var(--text-accent)", usd }];
      };
      return {
        period: p,
        reported: reported ? build("reported") : null,
        measured: measured ? build("measured") : null,
      };
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [data, selected, split, reported, measured, types, t]);

  const sum = (s: Seg[] | null) => (s ?? []).reduce((a, x) => a + x.usd, 0);
  const totals = stacks.reduce((a, s) => ({ r: a.r + sum(s.reported), m: a.m + sum(s.measured) }), { r: 0, m: 0 });

  const toggleType = (k: string) => setTypes((s) => { const n = new Set(s); if (n.has(k)) n.delete(k); else n.add(k); return n; });

  // --- layout ---
  const H = 280, padL = 58, padR = 16, padT = 16, padB = 46;
  const plotH = H - padT - padB;
  const n = stacks.length;
  const both = reported && measured;
  const slot = n <= 1 ? 140 : Math.max(both ? 40 : 26, Math.min(80, Math.round(900 / Math.max(1, n))));
  const W = padL + padR + Math.max(1, n) * slot;
  const barW = Math.min(both ? 22 : 38, slot * (both ? 0.36 : 0.62));
  const max = Math.max(1e-9, ...stacks.map((s) => Math.max(sum(s.reported), sum(s.measured))));
  const mag = Math.pow(10, Math.floor(Math.log10(max)));
  const nrm = max / mag;
  const niceMax = (nrm <= 1 ? 1 : nrm <= 2 ? 2 : nrm <= 5 ? 5 : 10) * mag;
  const y = (v: number) => padT + plotH - (v / niceMax) * plotH;
  const every = Math.ceil(n / Math.max(1, Math.floor((W - padL - padR) / 70)));
  const hs = hover != null ? stacks[hover] : null;
  const hx = hover != null ? padL + hover * slot + slot / 2 : 0;

  const drawStack = (segs: Seg[] | null, x0: number, dim: boolean) => {
    if (!segs) return null;
    let acc = 0;
    return segs.map((s) => {
      if (s.usd <= 0) return null;
      const top = y(acc + s.usd), h = Math.max(1, y(acc) - top);
      acc += s.usd;
      return <rect key={s.key} x={x0} y={top} width={barW} height={h} fill={s.color} opacity={dim ? 0.55 : 0.9} />;
    });
  };

  return (
    <div>
      {/* Filters: split · source · cost types — all combine */}
      <div className="flex flex-wrap items-center gap-2 mb-2">
        <span className="muted text-xs" style={{ minWidth: 64 }}>{t("costs.filter.split")}</span>
        {(["agent", "type", "none"] as Split[]).map((s) => (
          <Chip key={s} on={split === s} onClick={() => setSplit(s)}>{t(`costs.filter.split_${s}`)}</Chip>
        ))}
        <span style={{ width: 12 }} />
        <span className="muted text-xs">{t("costs.filter.source")}</span>
        <Chip on={reported} color={REPORTED_COLOR} onClick={() => (measured || !reported) && setReported(!reported)}
          title={t("costs.filter.reportedHint")}>{t("costs.filter.reported")}</Chip>
        <Chip on={measured} color="var(--text-accent)" onClick={() => (reported || !measured) && setMeasured(!measured)}
          title={t("costs.filter.measuredHint")}>{t("costs.filter.measured")}</Chip>
      </div>
      {measured && (
        <div className="flex flex-wrap items-center gap-2 mb-2">
          <span className="muted text-xs" style={{ minWidth: 64 }}>{t("costs.filter.types")}</span>
          {(data?.types ?? []).map((k) => (
            <Chip key={k} on={types.has(k)} color={TYPE_COLOR[k]} onClick={() => toggleType(k)}>{t(`costs.stack.${k}`)}</Chip>
          ))}
        </div>
      )}
      <div className="text-sm mb-3">
        {reported && <span className="mr-4">{t("costs.filter.reported")}: <b>{fmtUSD(totals.r)}</b></span>}
        {measured && <span className="mr-4">{t("costs.filter.measured")}: <b>{fmtUSD(totals.m)}</b></span>}
        {both && totals.r > 0 && types.size === (data?.types ?? []).length && (
          <span className="muted">{t("costs.filter.diff", { pct: (((totals.m - totals.r) / totals.r) * 100).toFixed(2) })}</span>
        )}
      </div>

      {n === 0 ? (
        <div className="muted text-sm" style={{ padding: "40px 0", textAlign: "center" }}>{t("costs.empty")}</div>
      ) : (
        <div style={{ position: "relative", overflowX: "auto" }}>
          <svg width={W} height={H} style={{ display: "block", maxWidth: "none" }} role="img" aria-label={t("costs.chartCost")}>
            {[0, 0.25, 0.5, 0.75, 1].map((f, i) => (
              <g key={i}>
                <line x1={padL} x2={W - padR} y1={y(f * niceMax)} y2={y(f * niceMax)} stroke="var(--border)" />
                <text x={padL - 8} y={y(f * niceMax) + 4} textAnchor="end" fontSize={11} fill="var(--text-muted)">
                  {f * niceMax >= 1 ? (f * niceMax).toFixed(0) : (f * niceMax).toFixed(2)}
                </text>
              </g>
            ))}
            {stacks.map((s, i) => {
              const cx = padL + i * slot + slot / 2;
              const gap = both ? 2 : 0;
              return (
                <g key={s.period} onMouseEnter={() => setHover(i)} onMouseLeave={() => setHover(null)}>
                  <rect x={padL + i * slot} y={padT} width={slot} height={plotH} fill="transparent" />
                  {both ? (
                    <>
                      {drawStack(s.reported, cx - barW - gap, true)}
                      {drawStack(s.measured, cx + gap, false)}
                    </>
                  ) : (
                    drawStack(s.reported ?? s.measured, cx - barW / 2, false)
                  )}
                </g>
              );
            })}
            <line x1={padL} x2={W - padR} y1={padT + plotH} y2={padT + plotH} stroke="var(--border-strong)" />
            {stacks.map((s, i) =>
              i % every !== 0 && i !== n - 1 ? null : (
                <text key={s.period} x={padL + i * slot + slot / 2} y={H - padB + 18} textAnchor="middle" fontSize={11} fill="var(--text-muted)">
                  {fmtPeriod(s.period)}
                </text>
              ),
            )}
            {hs && <line x1={hx} x2={hx} y1={padT} y2={padT + plotH} stroke="var(--border-strong)" strokeDasharray="3 3" />}
          </svg>
          {hs && (
            <div style={{
              position: "absolute", left: Math.min(hx + 12, W - 240), top: padT + 4, pointerEvents: "none",
              background: "var(--surface-2)", border: "0.5px solid var(--border-strong)", borderRadius: 8,
              padding: "8px 10px", fontSize: 12, boxShadow: "0 4px 16px var(--shadow-md)", minWidth: 210,
            }}>
              <div className="muted" style={{ fontSize: 11, marginBottom: 4 }}>{fmtPeriod(hs.period)}</div>
              {(["reported", "measured"] as const).map((src) =>
                !hs[src] ? null : (
                  <div key={src} style={{ marginBottom: 4 }}>
                    <div style={{ fontWeight: 600 }}>{t(`costs.filter.${src}`)}: {fmtUSD(sum(hs[src]))}</div>
                    {hs[src]!.filter((x) => x.usd > 0).map((x) => (
                      <div key={x.key} style={{ display: "flex", justifyContent: "space-between", gap: 12 }}>
                        <span><span style={{ color: x.color }}>■</span> {x.label}</span>
                        <span>{fmtUSD(x.usd)}</span>
                      </div>
                    ))}
                  </div>
                ),
              )}
            </div>
          )}
        </div>
      )}
      {split === "type" && reported && (
        <p className="muted text-xs mt-2 mb-0">{t("costs.filter.reportedNoTypes")}</p>
      )}
    </div>
  );
}
