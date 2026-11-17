import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { api } from "../api";
import { fmtCount, fmtUSD } from "../format";

/* Cost insights (migration 0142).
 *
 * Two questions the totals above cannot answer:
 *  1. What did each MODEL consume? Claude Code reports usage per model in its
 *     final line — an Opus run also makes small Haiku calls — with the cache
 *     writes split by lifetime. The table sets the engine's own price next to
 *     the same tokens priced with covey's list, so a drift shows up.
 *  2. What did it cost the SUBSCRIPTION? On a flat-rate seat the dollar figure
 *     is what the same work would have cost through the API. The provider's own
 *     meter (the weekly share, asked on every wake) is read against it here. */

interface ModelUsageRow {
  agent_id: string;
  agent_slug: string;
  agent_name: string;
  model: string;
  runs: number;
  input_tokens: number;
  output_tokens: number;
  thinking_tokens: number;
  cache_read_tokens: number;
  cache_write_5m_tokens: number;
  cache_write_1h_tokens: number;
  web_search_requests: number;
  engine_usd: number;
  list_usd: number | null;
}

interface ModelUsageReport {
  rows: ModelUsageRow[];
  runs_without_breakdown: number;
  usd_without_breakdown: number;
}

interface UsageReading {
  runtime_id: string;
  credential_ord: number;
  window_percent: number | null;
  week_percent: number | null;
  week_resets?: string;
  stale: boolean;
  fetched_at: string;
  cost_usd: number;
}

const th: React.CSSProperties = { padding: "4px 8px 8px 0", fontWeight: 500, whiteSpace: "nowrap" };
const td: React.CSSProperties = { padding: "4px 8px 4px 0", whiteSpace: "nowrap" };
const num: React.CSSProperties = { ...td, textAlign: "right", fontVariantNumeric: "tabular-nums" };

function shortModel(m: string) {
  return m.replace(/^claude-/, "");
}

function ModelTable({ rep }: { rep: ModelUsageReport }) {
  const { t } = useTranslation();
  const rows = rep.rows;
  const engine = rows.reduce((a, r) => a + r.engine_usd, 0);
  const list = rows.reduce((a, r) => a + (r.list_usd ?? 0), 0);
  const unpriced = rows.filter((r) => r.list_usd === null).length;
  if (rows.length === 0) return <div className="muted text-sm">{t("costs.empty")}</div>;
  const drift = engine > 0 ? ((list - engine) / engine) * 100 : 0;
  return (
    <div>
      <div style={{ overflowX: "auto" }}>
        <table style={{ width: "100%", borderCollapse: "collapse", fontSize: 13 }}>
          <thead>
            <tr className="muted" style={{ textAlign: "left" }}>
              <th style={th}>{t("costs.insights.agent")}</th>
              <th style={th}>{t("costs.insights.model")}</th>
              <th style={{ ...th, textAlign: "right" }}>{t("costs.insights.runs")}</th>
              <th style={{ ...th, textAlign: "right" }}>{t("costs.insights.output")}</th>
              <th style={{ ...th, textAlign: "right" }}>{t("costs.insights.cacheRead")}</th>
              <th style={{ ...th, textAlign: "right" }}>{t("costs.insights.cacheWrite")}</th>
              <th style={{ ...th, textAlign: "right" }}>{t("costs.insights.engine")}</th>
              <th style={{ ...th, textAlign: "right" }}>{t("costs.insights.list")}</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr key={`${r.agent_id}/${r.model}`} style={{ borderTop: "1px solid var(--border)" }}>
                <td style={td}>
                  <Link to={`/agents/${r.agent_id}`}>{r.agent_name || r.agent_slug}</Link>
                </td>
                <td style={td}>{shortModel(r.model)}</td>
                <td style={num}>{r.runs}</td>
                <td style={num} title={t("costs.insights.thinkingOf", { n: fmtCount(r.thinking_tokens) })}>
                  {fmtCount(r.output_tokens)}
                </td>
                <td style={num}>{fmtCount(r.cache_read_tokens)}</td>
                <td style={num} title={`5m ${fmtCount(r.cache_write_5m_tokens)} · 1h ${fmtCount(r.cache_write_1h_tokens)}`}>
                  {fmtCount(r.cache_write_5m_tokens + r.cache_write_1h_tokens)}
                </td>
                <td style={num}>{fmtUSD(r.engine_usd)}</td>
                <td style={num}>{r.list_usd === null ? "–" : fmtUSD(r.list_usd)}</td>
              </tr>
            ))}
            <tr style={{ borderTop: "1px solid var(--border)", fontWeight: 600 }}>
              <td style={td} colSpan={6}>{t("costs.insights.total")}</td>
              <td style={num}>{fmtUSD(engine)}</td>
              <td style={num}>{fmtUSD(list)}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <p className="muted text-xs mt-2 mb-0">
        {t("costs.insights.drift", { pct: drift.toFixed(2) })}
        {unpriced > 0 && <> · {t("costs.insights.unpriced", { count: unpriced })}</>}
        {rep.runs_without_breakdown > 0 && (
          <> · {t("costs.insights.without", { count: rep.runs_without_breakdown, usd: fmtUSD(rep.usd_without_breakdown) })}</>
        )}
      </p>
    </div>
  );
}

/** Meter (weekly %) and booked list-price cost over the same hours, two lines on
 *  their own scales; the ratio is taken over the whole window, because the
 *  provider reports whole percents and a single step says little. */
function MeterChart({ readings, locale }: { readings: UsageReading[]; locale: string }) {
  const { t } = useTranslation();
  const pts = readings.filter((r) => r.week_percent !== null);
  if (pts.length === 0) return <div className="muted text-sm">{t("costs.insights.noReadings")}</div>;
  const W = 560, H = 160, P = 28;
  const t0 = new Date(pts[0].fetched_at).getTime();
  const t1 = Math.max(t0 + 1, new Date(pts[pts.length - 1].fetched_at).getTime());
  const pMin = Math.min(...pts.map((r) => r.week_percent!));
  const pMax = Math.max(pMin + 1, ...pts.map((r) => r.week_percent!));
  const cMax = Math.max(1, ...pts.map((r) => r.cost_usd));
  const x = (r: UsageReading) => P + ((new Date(r.fetched_at).getTime() - t0) / (t1 - t0)) * (W - 2 * P);
  const yp = (v: number) => H - P - ((v - pMin) / (pMax - pMin)) * (H - 2 * P);
  const yc = (v: number) => H - P - (v / cMax) * (H - 2 * P);
  const meterLine = pts.map((r, i) => `${i ? "L" : "M"}${x(r).toFixed(1)},${yp(r.week_percent!).toFixed(1)}`).join(" ");
  const costLine = pts.map((r, i) => `${i ? "L" : "M"}${x(r).toFixed(1)},${yc(r.cost_usd).toFixed(1)}`).join(" ");
  const fmtT = (s: string) => new Date(s).toLocaleString(locale, { day: "2-digit", month: "2-digit", hour: "2-digit", minute: "2-digit" });
  return (
    <div>
      <div className="flex items-center gap-3 text-xs muted mb-2">
        <span><span style={{ color: "var(--clay)" }}>■</span> {t("costs.insights.meter")}</span>
        <span><span style={{ color: "var(--text-accent)" }}>■</span> {t("costs.insights.listCost")}</span>
      </div>
      <svg viewBox={`0 0 ${W} ${H}`} style={{ width: "100%", height: "auto" }} role="img" aria-label={t("costs.insights.meterTitle")}>
        <line x1={P} y1={H - P} x2={W - P} y2={H - P} stroke="var(--border)" />
        <path d={costLine} fill="none" stroke="var(--text-accent)" strokeWidth={2} />
        <path d={meterLine} fill="none" stroke="var(--clay)" strokeWidth={2} />
        <text x={P} y={H - 8} fontSize={10} fill="currentColor" opacity={0.6}>{fmtT(pts[0].fetched_at)}</text>
        <text x={W - P} y={H - 8} fontSize={10} fill="currentColor" opacity={0.6} textAnchor="end">{fmtT(pts[pts.length - 1].fetched_at)}</text>
        <text x={4} y={yp(pMax) + 4} fontSize={10} fill="var(--clay)">{pMax}%</text>
        <text x={4} y={yp(pMin) + 4} fontSize={10} fill="var(--clay)">{pMin}%</text>
        <text x={W - 4} y={yc(cMax) + 4} fontSize={10} fill="var(--text-accent)" textAnchor="end">{fmtUSD(cMax)}</text>
      </svg>
    </div>
  );
}

export function CostInsights({ days, locale }: { days: number; locale: string }) {
  const { t } = useTranslation();
  const models = useQuery({
    queryKey: ["cost", "models", days],
    queryFn: () => api<ModelUsageReport>(`/cost/models?days=${days}`),
    refetchInterval: 30000,
  });
  const readings = useQuery({
    queryKey: ["cost", "subscription", days],
    queryFn: () => api<UsageReading[] | null>(`/cost/subscription?days=${days}`),
    refetchInterval: 30000,
  });
  const rs = readings.data ?? [];
  const ratio = useMemo(() => {
    const w = rs.filter((r) => r.week_percent !== null);
    if (w.length < 2) return null;
    const dp = w[w.length - 1].week_percent! - w[0].week_percent!;
    const dc = w[w.length - 1].cost_usd - w[0].cost_usd;
    return { dp, dc, perPct: dp > 0 ? dc / dp : null, last: w[w.length - 1] };
  }, [rs]);

  return (
    <div className="grid gap-4 mt-4" style={{ gridTemplateColumns: "repeat(auto-fit, minmax(min(420px, 100%), 1fr))" }}>
      <div className="card">
        <div style={{ fontWeight: 600, marginBottom: 4 }}>{t("costs.insights.modelsTitle")}</div>
        <p className="muted text-xs mb-3">{t("costs.insights.modelsHint")}</p>
        {models.data ? <ModelTable rep={models.data} /> : <div className="muted text-sm">…</div>}
      </div>
      <div className="card">
        <div style={{ fontWeight: 600, marginBottom: 4 }}>{t("costs.insights.meterTitle")}</div>
        <p className="muted text-xs mb-3">{t("costs.insights.meterHint")}</p>
        <MeterChart readings={rs} locale={locale} />
        {ratio && (
          <p className="text-sm mt-2 mb-0">
            {ratio.perPct !== null
              ? t("costs.insights.ratio", { usd: fmtUSD(ratio.perPct), dp: ratio.dp, dc: fmtUSD(ratio.dc) })
              : t("costs.insights.ratioFlat", { dc: fmtUSD(ratio.dc), pct: ratio.last.week_percent })}
          </p>
        )}
      </div>
    </div>
  );
}
