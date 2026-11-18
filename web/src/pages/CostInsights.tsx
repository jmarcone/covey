import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { api, type ModelUsageReport, type UsageReading } from "../api";
import { fmtBucket, fmtCount, fmtNumber, fmtUSD } from "../format";
import { usableReadings, type CredentialFigures } from "./costFigures";
import { OrgWide, SOURCE_COLOR, Swatch } from "./StackedCostChart";

/* Cost insights.
 *
 * Two questions the totals above cannot answer:
 *  1. What did each MODEL consume? Claude Code reports usage per model on
 *     every result line — an Opus run also makes small Haiku calls — with the
 *     cache writes split by lifetime. The table sets Claude Code's own price
 *     next to the same tokens priced with covey's list: a price-table check.
 *  2. What does a point of the subscription meter buy? Per credential, the
 *     same figures as its tiles above (costFigures.ts): the meter's span,
 *     the real cost of its movement, the list price of the credential's
 *     lines over the same span, and the multiple. The meter is
 *     account-wide: other use of the account moves it too. */

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
  // A model the list does not know enters the recomputed figure at Claude
  // Code's own price; without it here the list total falls short of the
  // headline by exactly that, with nothing saying so.
  const unpricedUSD = rows.filter((r) => r.list_usd === null).reduce((a, r) => a + r.engine_usd, 0);
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
            {unpriced > 0 && (
              <tr data-unpriced style={{ borderTop: "1px solid var(--border)" }}>
                <td style={{ ...td, whiteSpace: "normal" }} colSpan={6} className="muted">{t("costs.insights.unpricedRow", { count: unpriced })}</td>
                <td style={num}>{fmtUSD(unpricedUSD)}</td>
                <td style={num}>{fmtUSD(unpricedUSD)}</td>
              </tr>
            )}
            <tr data-total style={{ borderTop: "1px solid var(--border)", fontWeight: 600 }}>
              <td style={td} colSpan={6}>{t(unpriced > 0 ? "costs.insights.totalWithUnpriced" : "costs.insights.total")}</td>
              <td style={num}>{fmtUSD(engine)}</td>
              <td style={num}>{fmtUSD(list + unpricedUSD)}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <p className="muted text-xs mt-2 mb-0">
        {t("costs.insights.priceTableCheck", { pct: `${check >= 0 ? "+" : ""}${fmtNumber(check, 2)}` })}
        {unpriced > 0 && <> · {t("costs.insights.unpriced", { count: unpriced })}</>}
        {rep.runs_without_breakdown > 0 && (
          <> · {t("costs.insights.without", { count: rep.runs_without_breakdown, usd: fmtUSD(rep.usd_without_breakdown) })}</>
        )}
      </p>
    </div>
  );
}

/** One credential's meter with the figures of its tiles above: the span the
 *  meter was read over, the real cost of its movement (or why there is
 *  none), the list price of the credential's lines over the same span, and
 *  the multiple. No chart: two independent scales stretched to the same
 *  height agreed by construction, not by the data. */
function MeterFigures({ f, readings }: { f: CredentialFigures; readings: UsageReading[] }) {
  const { t } = useTranslation();
  const m = f.meter!;
  const r = f.real;
  const source = t(`costs.insights.source_${m.source}`, { defaultValue: m.source });
  const own = readings.filter((x) => x.secret_key === m.secret_key && x.secret_slot === m.secret_slot);
  const sameWindow = own.filter((x) => x.source === m.source && (x.window_kind === "seven_day" || x.window_kind === ""));
  const dropped = sameWindow.length - usableReadings(own, m.secret_key, m.secret_slot, m.source).length;
  const five = own.filter((x) => x.supported && !x.stale && x.window_percent != null).at(-1);

  let real: string;
  if (!r) real = t("costs.fig.noMeter");
  else if (!r.priced) real = r.unpricedPoints > 0 ? t("costs.fig.noPlanPricePoints", { n: r.unpricedPoints, m: r.points }) : t("costs.fig.noPlan");
  else if (r.below) real = `< ${fmtUSD(r.high)} · ${t("costs.fig.realBelow")}`;
  else real = `≈ ${fmtUSD(r.value)} · ${t("costs.fig.realRange", { low: fmtUSD(r.low), high: fmtUSD(r.high), pts: r.points, pp: fmtUSD(r.value / r.points) })}`;

  let mult: string;
  if (f.multiple?.kind === "estimate") {
    mult = `≈ ${times(f.multiple.value)} · ${t("costs.fig.multipleRange", { low: times(f.multiple.low), high: times(f.multiple.high) })}`;
  } else if (f.noMultiple === "unpriced") {
    mult = r && r.unpricedPoints > 0 ? t("costs.fig.noMultipleUnpriced", { n: r.unpricedPoints, m: r.points }) : t("costs.fig.noMultipleNoPlan");
  } else if (f.noMultiple === "movement" && r && r.points >= 1 && f.aligned) {
    mult = t("costs.fig.notEnoughMovement", { pts: r.points, low: times(f.aligned / r.high), high: times(r.low > 0 ? f.aligned / r.low : Infinity) });
  } else if (f.noMultiple === "movement") {
    mult = t("costs.fig.notEnoughMovementNone");
  } else mult = "–";

  const row = (label: string, value: string, color?: string, hatched?: boolean) => (
    <div style={{ display: "flex", gap: 8, alignItems: "baseline", padding: "2px 0" }}>
      <span className="muted text-xs" style={{ display: "inline-flex", alignItems: "center", gap: 6, width: 190, flexShrink: 0 }}>
        {color && <Swatch color={color} hatched={hatched} />}{label}
      </span>
      <span className="text-sm" style={{ fontVariantNumeric: "tabular-nums" }}>{value}</span>
    </div>
  );
  return (
    <div className="mb-4" data-meter={f.id}>
      <div className="text-xs" style={{ fontWeight: 600, marginBottom: 4 }}>{`${m.secret_key} #${m.secret_slot} · ${source}`}</div>
      {row(t("costs.insights.meterSpan"), t("costs.insights.meterSpanValue", {
        from: m.baseline_percent, fromAt: fmtBucket(m.baseline_at, "hour"), to: m.last_percent, toAt: fmtBucket(m.last_at, "hour"),
        pts: m.points, n: m.readings,
      }))}
      {row(t("costs.fig.realEstimate"), real, SOURCE_COLOR.real, true)}
      {row(t("costs.insights.meterList"), f.aligned != null ? fmtUSD(f.aligned) : "–", SOURCE_COLOR.recomputed)}
      {row(t("costs.fig.multiple"), mult)}
      <p className="muted text-xs mt-1 mb-0">
        {five
          ? t("costs.insights.fiveHour", { pct: five.window_percent, at: fmtBucket(five.fetched_at, "hour") })
          : t("costs.insights.fiveHourNotReported")}
        {dropped > 0 && <> · {t("costs.insights.dropped", { count: dropped })}</>}
      </p>
    </div>
  );
}

const times = (v: number) => (Number.isFinite(v) ? `${fmtNumber(v, 1)}×` : "∞");

/** The cards below the chart. `figures` are the credentials' figures over the
 *  whole organisation — the same the tiles show without an agent filter. */
export function CostInsights({ days, figures, filtered = false }: { days: number; figures: CredentialFigures[]; filtered?: boolean }) {
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
  const metered = figures.filter((f) => f.meter != null && f.kind !== "api_key");

  return (
    <div className="grid gap-4 mt-4" style={{ gridTemplateColumns: "repeat(auto-fit, minmax(min(420px, 100%), 1fr))" }}>
      <div className="card">
        <div style={{ fontWeight: 600, marginBottom: 4 }}>{t("costs.insights.modelsTitle")}{filtered && <OrgWide />}</div>
        <p className="muted text-xs mb-3">{t("costs.insights.modelsHint")}</p>
        {models.data ? <ModelTable rep={models.data} /> : <div className="muted text-sm">…</div>}
      </div>
      <div className="card">
        <div style={{ fontWeight: 600, marginBottom: 4 }}>{t("costs.insights.meterTitle")}{filtered && <OrgWide />}</div>
        <p className="muted text-xs mb-3">{t("costs.insights.meterHint")}</p>
        {metered.length === 0 && <div className="muted text-sm">{t("costs.insights.noReadings")}</div>}
        {metered.map((f) => <MeterFigures key={f.id} f={f} readings={rs} />)}
        <p className="muted text-xs mt-2 mb-0">{t("costs.fig.accountWideCaveat")}</p>
      </div>
    </div>
  );
}
