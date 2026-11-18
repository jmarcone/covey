import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { api, type ModelUsageReport, type UsageReading } from "../api";
import { fmtBucket, fmtCount, fmtUSD, fmtUSDAxis } from "../format";
import { credId, meterRatio, usableReadings } from "./costFigures";
import { SOURCE_COLOR, Swatch } from "./StackedCostChart";

/* Cost insights.
 *
 * Two questions the totals above cannot answer:
 *  1. What did each MODEL consume? Claude Code reports usage per model on
 *     every result line — an Opus run also makes small Haiku calls — with the
 *     cache writes split by lifetime. The table sets Claude Code's own price
 *     next to the same tokens priced with covey's list: a price-table check.
 *  2. What does a point of the subscription meter buy? Per credential and
 *     source, the provider's weekly meter against the list price of the work
 *     on that credential — one axis each, the meter as the whole-percent
 *     steps it moves in. The meter is account-wide: other use of the account
 *     moves it too. */

const th: React.CSSProperties = { padding: "4px 8px 8px 0", fontWeight: 500, whiteSpace: "nowrap" };
const td: React.CSSProperties = { padding: "4px 8px 4px 0", whiteSpace: "nowrap" };
const num: React.CSSProperties = { ...td, textAlign: "right", fontVariantNumeric: "tabular-nums" };

function shortModel(m: string) {
  return m.replace(/^claude-/, "");
}

function ModelTable({ rep }: { rep: ModelUsageReport }) {
  const { t } = useTranslation();
  const rows = [...(rep.rows ?? [])].sort((a, b) => (b.list_usd ?? b.engine_usd) - (a.list_usd ?? a.engine_usd));
  const engine = rows.reduce((a, r) => a + r.engine_usd, 0);
  const list = rows.reduce((a, r) => a + (r.list_usd ?? 0), 0);
  const priced = rows.filter((r) => r.list_usd !== null).reduce((a, r) => a + r.engine_usd, 0);
  const unpriced = rows.filter((r) => r.list_usd === null).length;
  if (rows.length === 0) return <div className="muted text-sm">{t("costs.empty")}</div>;
  const check = priced > 0 ? ((list - priced) / priced) * 100 : 0;
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
              <th style={{ ...th, textAlign: "right" }}>{t("costs.insights.claudeCode")}</th>
              <th style={{ ...th, textAlign: "right" }}>{t("costs.insights.list")}</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr key={`${r.agent_id}/${r.model}/${r.credential_kind}`} style={{ borderTop: "1px solid var(--border)" }}>
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
        {t("costs.insights.priceTableCheck", { pct: `${check >= 0 ? "+" : ""}${check.toFixed(2)}` })}
        {unpriced > 0 && <> · {t("costs.insights.unpriced", { count: unpriced })}</>}
        {rep.runs_without_breakdown > 0 && (
          <> · {t("costs.insights.without", { count: rep.runs_without_breakdown, usd: fmtUSD(rep.usd_without_breakdown) })}</>
        )}
      </p>
    </div>
  );
}

/** The meter of one credential and source: week % (y) against the list $ of
 *  the work on the credential (x), one axis each. A dashed step line — the
 *  meter moves in whole percents, so it stays level until the next reading
 *  says otherwise — broken at every new week, labelled at its end. */
function MeterSteps({ readings, label }: { readings: UsageReading[]; label: string }) {
  const { t } = useTranslation();
  const W = 560, H = 170, L = 44, R = 96, T = 12, B = 30;
  const xs = readings.map((r) => r.cost_usd ?? 0);
  const ps = readings.map((r) => r.week_percent ?? 0);
  const xMax = Math.max(1e-9, ...xs), pMin = Math.min(...ps), pMax = Math.max(pMin + 1, ...ps);
  const x = (v: number) => L + (v / xMax) * (W - L - R);
  const y = (v: number) => T + (1 - (v - pMin) / (pMax - pMin)) * (H - T - B);
  // A new week begins where the meter collapsed: the line breaks there.
  const paths: string[] = [];
  let d = "";
  readings.forEach((r, i) => {
    const px = x(xs[i]), py = y(ps[i]);
    if (i === 0 || ps[i] < ps[i - 1] / 2 && ps[i] <= ps[i - 1] - 5) {
      if (d) paths.push(d);
      d = `M${px.toFixed(1)},${py.toFixed(1)}`;
    } else {
      d += ` H${px.toFixed(1)} V${py.toFixed(1)}`;
    }
  });
  if (d) paths.push(d);
  const last = readings[readings.length - 1];
  return (
    <svg viewBox={`0 0 ${W} ${H}`} style={{ width: "100%", height: "auto" }} role="img"
      aria-label={t("costs.insights.meterAria", { label, from: pMin, to: pMax, usd: fmtUSD(xMax) })}>
      <title>{label}</title>
      <line x1={L} y1={H - B} x2={W - R} y2={H - B} stroke="var(--border-strong)" />
      <line x1={L} y1={T} x2={L} y2={H - B} stroke="var(--border-strong)" />
      {paths.map((p, i) => <path key={i} d={p} fill="none" stroke={SOURCE_COLOR.real} strokeWidth={2} strokeDasharray="5 3" />)}
      {readings.map((r, i) => <circle key={i} cx={x(xs[i])} cy={y(ps[i])} r={2.5} fill={SOURCE_COLOR.real} />)}
      <text x={x(xs[xs.length - 1]) + 6} y={y(ps[ps.length - 1]) + 4} fontSize={10.5} fill="var(--text-primary)">
        {label} · {last.week_percent} %
      </text>
      <text x={L - 6} y={y(pMax) + 4} fontSize={10} fill="var(--text-muted)" textAnchor="end">{pMax} %</text>
      <text x={L - 6} y={y(pMin) + 4} fontSize={10} fill="var(--text-muted)" textAnchor="end">{pMin} %</text>
      <text x={L} y={H - B + 14} fontSize={10} fill="var(--text-muted)">{fmtUSDAxis(0)}</text>
      <text x={W - R} y={H - B + 14} fontSize={10} fill="var(--text-muted)" textAnchor="end">{fmtUSD(xMax)}</text>
      <text x={(L + W - R) / 2} y={H - 4} fontSize={10} fill="var(--text-muted)" textAnchor="middle">{t("costs.insights.meterAxisX")}</text>
    </svg>
  );
}

type MeterGroup = { key: string; secretKey: string; slot: number; source: string; usable: UsageReading[]; dropped: number };

export function CostInsights({ days }: { days: number }) {
  const { t } = useTranslation();
  const models = useQuery({
    queryKey: ["cost", "models", days],
    queryFn: () => api<ModelUsageReport>(`/cost/models?days=${days}`),
    refetchInterval: 30000,
  });
  const readings = useQuery({
    queryKey: ["cost", "usage-readings", days],
    queryFn: () => api<UsageReading[] | null>(`/cost/usage-readings?days=${days}`),
    refetchInterval: 30000,
  });
  const rs = useMemo(() => readings.data ?? [], [readings.data]);

  // Per credential and source: the usable readings, and how many were dropped.
  const groups = useMemo(() => {
    const m = new Map<string, MeterGroup>();
    for (const r of rs) {
      if (r.secret_key == null || r.secret_slot == null) continue;
      if (r.window_kind !== "seven_day" && r.window_kind !== "") continue;
      const key = `${credId(r.secret_key, r.secret_slot)}/${r.source}`;
      if (!m.has(key)) m.set(key, { key, secretKey: r.secret_key, slot: r.secret_slot, source: r.source, usable: [], dropped: 0 });
    }
    for (const g of m.values()) {
      g.usable = usableReadings(rs, g.secretKey, g.slot, g.source);
      g.dropped = rs.filter((r) => r.secret_key === g.secretKey && r.secret_slot === g.slot && r.source === g.source
        && (r.window_kind === "seven_day" || r.window_kind === "")).length - g.usable.length;
    }
    return [...m.values()];
  }, [rs]);
  // The five-hour window of a credential, when any reading reported it.
  const fiveHour = (key: string, slot: number) => {
    const w = rs.filter((r) => r.secret_key === key && r.secret_slot === slot && r.supported && !r.stale && r.window_percent != null);
    return w.length ? w[w.length - 1] : null;
  };

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
        {groups.length === 0 && <div className="muted text-sm">{t("costs.insights.noReadings")}</div>}
        {groups.map((g) => {
          const ratio = meterRatio(g.usable);
          const five = fiveHour(g.secretKey, g.slot);
          const label = `${g.secretKey} #${g.slot} · ${t(`costs.insights.source_${g.source}`, { defaultValue: g.source })}`;
          return (
            <div key={g.key} className="mb-4">
              <div className="text-xs" style={{ display: "flex", alignItems: "center", gap: 6, fontWeight: 600 }}>
                <Swatch color={SOURCE_COLOR.real} /> {label}
              </div>
              {g.usable.length >= 2 ? <MeterSteps readings={g.usable} label={t(`costs.insights.source_${g.source}`, { defaultValue: g.source })} />
                : <p className="muted text-xs">{t("costs.insights.noUsable")}</p>}
              {ratio && (
                <p className="text-sm mt-1 mb-0">
                  {ratio.perPoint !== null
                    ? t("costs.insights.ratioPer", {
                      usd: fmtUSD(ratio.perPoint), low: fmtUSD(ratio.low),
                      high: Number.isFinite(ratio.high) ? fmtUSD(ratio.high) : "∞", n: ratio.readings,
                    })
                    : t("costs.insights.ratioFlat", { dc: fmtUSD(ratio.usd), pct: g.usable[g.usable.length - 1].week_percent })}
                </p>
              )}
              <p className="muted text-xs mt-1 mb-0">
                {five
                  ? t("costs.insights.fiveHour", { pct: five.window_percent, at: fmtBucket(five.fetched_at, "hour") })
                  : t("costs.insights.fiveHourNotReported")}
                {g.dropped > 0 && <> · {t("costs.insights.dropped", { count: g.dropped })}</>}
              </p>
            </div>
          );
        })}
        <p className="muted text-xs mt-2 mb-0">{t("costs.fig.accountWideCaveat")}</p>
      </div>
    </div>
  );
}
