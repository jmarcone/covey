import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Link, useSearchParams } from "react-router";
import { api, type ExternalModelUsage } from "../api";
import { exact, fmtBucket, fmtCount, fmtNumber, fmtUSD, fmtUSDAxis } from "../format";
import { Seg } from "./Costs";
import { BarChart, Chip, Swatch, type Bar } from "./StackedCostChart";
import { FigureTile, OUTSIDE_COLOR } from "./OutsideSessions";
import {
  ORIGINS,
  RANGES,
  TEN_HOUR_MAX_DAYS,
  TEN_MIN_MAX_DAYS,
  HOUR_MAX_DAYS,
  TOKEN_KINDS,
  bucketOptions,
  meterPerPeriod,
  originOf,
  perPeriodBy,
  rangeOptions,
  splitBy,
  type SplitRow,
} from "./outsideFigures";

/* The detailed view of the Claude Code sessions outside covey (#120, the
 * human's second comment): like an agent's cost view — list price over time
 * in buckets down to 10 minutes, per model and per origin (main session,
 * subagents, workflows), tokens per type with their dollars, and their share
 * of the subscription meter. Every figure is the server's
 * (GET /cost/external-usage): F1/F2 per cell, F3 per meter reading. */

const MODEL_COLORS = [1, 2, 3, 4, 5, 6, 7, 8].map((i) => `var(--agent-${i})`);
const ORIGIN_COLOR: Record<string, string> = {
  main: "var(--cost-origin-main)",
  subagent: "var(--cost-origin-subagent)",
  workflow: "var(--cost-origin-workflow)",
  unknown: "var(--cost-origin-unknown)",
};
const pct = (v: number) => `${fmtNumber(v * 100, 1)} %`;
const fineBucket = (b: string) => b === "10min" || b === "hour" || b === "10hour";

/** The range and bucket from the address, held to the page's rules. */
function useWindow() {
  const [params, setParams] = useSearchParams();
  const d = Number(params.get("days"));
  const days = RANGES.some((r) => r.v === d) ? d : 1;
  const want = params.get("bucket") ?? RANGES.find((r) => r.v === days)!.bucket;
  const tooFine = (want === "10min" && days > TEN_MIN_MAX_DAYS) || (want === "hour" && days > HOUR_MAX_DAYS)
    || (want === "10hour" && days > TEN_HOUR_MAX_DAYS) || !["10min", "hour", "10hour", "day", "week", "month"].includes(want);
  const bucket = tooFine ? RANGES.find((r) => r.v === days)!.bucket : want;
  const source = params.get("source") || null;
  const set = (next: { days?: number; bucket?: string; source?: string | null }) => {
    const p = new URLSearchParams(params);
    if (next.days != null) {
      p.set("days", String(next.days));
      p.set("bucket", RANGES.find((r) => r.v === next.days)!.bucket);
    }
    if (next.bucket != null) p.set("bucket", next.bucket);
    if (next.source !== undefined) {
      if (next.source) p.set("source", next.source);
      else p.delete("source");
    }
    setParams(p, { replace: true });
  };
  return { days, bucket, source, set };
}

export function useExternalUsage(days: number, bucket: string, source: string | null) {
  return useQuery({
    queryKey: ["cost", "external-usage", days, bucket, source],
    queryFn: () => api<ExternalModelUsage>(`/cost/external-usage?days=${days}&bucket=${bucket}${source ? `&source=${encodeURIComponent(source)}` : ""}`),
    refetchInterval: 30000,
  });
}

/** One split (per model or per origin): tokens per type with their $, list
 *  and reported $, and a total row that is the sum of the rows. */
function SplitTable({ title, rows, total, label, color, test }: {
  title: string; rows: SplitRow[]; total: SplitRow; label: (k: string) => string; color: (k: string) => string; test: string;
}) {
  const { t } = useTranslation();
  const th: React.CSSProperties = { padding: "4px 8px 6px 0", fontWeight: 500, whiteSpace: "nowrap", textAlign: "right" };
  const td: React.CSSProperties = { padding: "3px 8px 3px 0", textAlign: "right", fontVariantNumeric: "tabular-nums", whiteSpace: "nowrap" };
  const money = (r: SplitRow, k: string) => (r.typeUSD[k] != null ? fmtUSD(r.typeUSD[k]) : "–");
  const row = (r: SplitRow, head: React.ReactNode, isTotal = false) => (
    <tr key={r.key} data-row={isTotal ? undefined : r.key} data-total={isTotal || undefined}
      style={{ borderTop: `1px solid ${isTotal ? "var(--border-strong)" : "var(--border)"}`, fontWeight: isTotal ? 600 : 400 }}>
      <th scope="row" style={{ ...td, textAlign: "left", fontWeight: isTotal ? 600 : 400 }}>{head}</th>
      <td style={td}>{fmtNumber(r.calls, 0)}</td>
      {TOKEN_KINDS.map((k) => (
        <td key={k} style={td} title={exact(r.tokens[k] ?? 0)}>
          {fmtCount(r.tokens[k] ?? 0)}<div className="muted text-xs">{money(r, k)}</div>
        </td>
      ))}
      <td style={td} data-list>{r.list != null ? fmtUSD(r.list) : <span className="muted">{t("costs.outside.noPrice")}</span>}</td>
      <td style={td}>{r.reported != null ? fmtUSD(r.reported) : "–"}</td>
      <td style={td}>{r.unpricedTokens > 0 ? fmtCount(r.unpricedTokens) : "–"}</td>
    </tr>
  );
  return (
    <div style={{ overflowX: "auto", marginTop: 8 }}>
      <table style={{ width: "100%", borderCollapse: "collapse", fontSize: 12 }} data-split-table={test}>
        <caption className="muted text-xs" style={{ textAlign: "left", paddingBottom: 4 }}>{title}</caption>
        <thead>
          <tr className="muted">
            <th scope="col" style={{ ...th, textAlign: "left" }}>{test === "model" ? t("costs.outside.colModel") : t("costs.outside.colOrigin")}</th>
            <th scope="col" style={th}>{t("costs.outside.colCalls")}</th>
            {TOKEN_KINDS.map((k) => <th key={k} scope="col" style={th}>{t(`costs.tok.${k}`)}</th>)}
            <th scope="col" style={th}>{t("costs.outside.colList")}</th>
            <th scope="col" style={th}>{t("costs.fig.reported")}</th>
            <th scope="col" style={th}>{t("costs.outside.colUnpriced")}</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => row(r, <span style={{ display: "inline-flex", alignItems: "center", gap: 6 }}><Swatch color={color(r.key)} /> {label(r.key)}</span>))}
        </tbody>
        <tfoot>{row(total, t("costs.fig.total"), true)}</tfoot>
      </table>
    </div>
  );
}

export default function OutsideCosts() {
  const { t } = useTranslation();
  const { days, bucket, source, set } = useWindow();
  const q = useExternalUsage(days, bucket, source);
  const [stack, setStack] = useState<"model" | "origin">("model");
  const [asTable, setAsTable] = useState(false);
  const data = q.data;
  const cells = useMemo(() => data?.cells ?? [], [data]);

  const models = useMemo(() => splitBy(cells, (c) => c.model), [cells]);
  const origins = useMemo(() => splitBy(cells, (c) => originOf(c)), [cells]);
  const modelNames = useMemo(() => (data?.models ?? []).map((m) => m.model), [data]);
  const modelColor = (m: string) => MODEL_COLORS[Math.max(0, modelNames.indexOf(m)) % MODEL_COLORS.length];
  const originLabel = (o: string) => t(`costs.outside.origin_${o}`);

  const series = useMemo(() => perPeriodBy(cells, stack === "model" ? (c) => c.model : (c) => originOf(c)), [cells, stack]);
  const keys = stack === "model" ? models.rows.map((r) => r.key).reverse() : ORIGINS.filter((o) => origins.rows.some((r) => r.key === o));
  const groups: Bar[][] = series.map((p) => [{
    key: "list", label: t("costs.outside.colList"), color: OUTSIDE_COLOR,
    segs: keys.map((k) => ({ key: k, label: stack === "model" ? k : originLabel(k), color: stack === "model" ? modelColor(k) : ORIGIN_COLOR[k], value: p.v.get(k) ?? 0 })),
  }]);

  // The meter: the seats the chosen sources run on. A share is always of
  // the whole meter, every source on it included.
  const seats = useMemo(() => {
    if (!data) return [];
    const linked = data.sources.filter((s) => s.meter_credential && (source == null || s.source === source));
    return data.meter.credentials.filter((c) => linked.some((s) => s.meter_credential!.secret_key === c.secret_key && s.meter_credential!.secret_slot === c.secret_slot));
  }, [data, source]);
  const real = seats.reduce((a, c) => a + c.external_real_usd, 0);
  const splitReal = seats.reduce((a, c) => a + c.external_real_usd + c.covey_real_usd, 0);
  const meterRows = useMemo(() => (data ? meterPerPeriod(data, cells, bucket, source) : []), [data, cells, bucket, source]);
  const sourceRow = data?.sources.find((s) => s.source === source);
  const unpricedModels = (data?.models ?? []).filter((m) => !m.priced).map((m) => m.model);
  const fmtPeriod = (p: string) => fmtBucket(p, bucket);

  return (
    <div>
      <div className="mb-1"><Link to={`/costs`} className="text-sm">← {t("costs.outside.back")}</Link></div>
      <div className="flex items-baseline gap-3 mb-1">
        <h1 className="text-[22px]" style={{ display: "inline-flex", alignItems: "center", gap: 8 }}><Swatch color={OUTSIDE_COLOR} /> {t("costs.outside.series")}</h1>
        <span className="muted">{source ?? t("costs.outside.sourceAll")}</span>
      </div>
      <p className="muted text-sm mb-4" style={{ maxWidth: 680 }}>{t("costs.outside.pageHint")}</p>

      <div className="flex flex-wrap items-center gap-3 mb-3">
        <div style={{ flex: 1 }} />
        <Seg value={days} onChange={(v) => set({ days: v })} options={rangeOptions(t)} />
        <Seg value={bucket} onChange={(v) => set({ bucket: v })} options={bucketOptions(t, days)} />
      </div>
      <div className="flex flex-wrap items-center gap-2 mb-4" role="group" aria-label={t("costs.outside.source")}>
        <span className="muted text-xs" style={{ width: 84 }}>{t("costs.outside.source")}</span>
        <Chip on={source == null} onClick={() => set({ source: null })}>{t("costs.outside.sourceAll")}</Chip>
        {(data?.sources ?? []).map((s) => (
          <Chip key={s.source} on={source === s.source} onClick={() => set({ source: s.source })}>
            {s.source} <span style={{ opacity: 0.7 }}>{s.list_usd != null ? fmtUSD(s.list_usd) : "–"}</span>
          </Chip>
        ))}
      </div>

      {q.isError ? <p className="muted">{t("costs.outside.loadError")}</p> : !data ? null : cells.length === 0 ? (
        <div className="card muted text-sm" style={{ padding: "40px 0", textAlign: "center" }}>{t("costs.outside.empty")}</div>
      ) : <>
        <div className="flex flex-wrap gap-3 mb-4" data-outside-tiles>
          <FigureTile test="list" label={t("costs.outside.colList")} color={OUTSIDE_COLOR}
            value={models.total.list != null ? fmtUSD(models.total.list) : "–"}
            lines={[t("costs.outside.tileSub", { calls: fmtCount(models.total.calls) })]} />
          {models.total.reported != null && (
            <FigureTile test="reported" label={t("costs.fig.reported")} value={fmtUSD(models.total.reported)} />
          )}
          <FigureTile test="real" label={t("costs.outside.meterShare")} color={OUTSIDE_COLOR} hatched
            value={seats.length > 0 && splitReal > 0 ? `≈ ${fmtUSD(real)}` : "–"}
            lines={[
              seats.length === 0 ? t("costs.outside.noMeterLinked")
                : splitReal > 0 ? t("costs.outside.ofMeter", { pct: pct(real / splitReal) }) : t("costs.outside.noShare"),
            ]} />
          {models.total.unpricedTokens > 0 && (
            <FigureTile test="unpriced" label={t("costs.outside.colUnpriced")} value={fmtCount(models.total.unpricedTokens)}
              lines={[t("costs.outside.unpricedModels", { models: unpricedModels.join(", ") })]} />
          )}
        </div>

        <div className="card mb-4">
          <div className="flex items-center justify-between mb-3 flex-wrap gap-2">
            <div style={{ fontWeight: 600 }}>{t("costs.outside.chartTitle")}</div>
            <div className="flex flex-wrap items-center gap-2">
              <span className="muted text-xs">{t("costs.outside.stack")}</span>
              <Chip on={stack === "model"} onClick={() => setStack("model")}>{t("costs.outside.byModel")}</Chip>
              <Chip on={stack === "origin"} onClick={() => setStack("origin")}>{t("costs.outside.byOrigin")}</Chip>
              <Chip on={asTable} onClick={() => setAsTable((v) => !v)}>{t("costs.fig.showAsTable")}</Chip>
            </div>
          </div>
          <div className="flex flex-wrap items-center gap-3 text-xs muted mb-2" role="note">
            {keys.map((k) => (
              <span key={k} style={{ display: "inline-flex", alignItems: "center", gap: 6 }}>
                <Swatch color={stack === "model" ? modelColor(k) : ORIGIN_COLOR[k]} /> {stack === "model" ? k : originLabel(k)}
              </span>
            ))}
          </div>
          <BarChart title={t("costs.outside.chartTitle")} periods={series.map((p) => p.period)} groups={groups}
            fmtPeriod={fmtPeriod} fmtValue={fmtUSD} fmtAxis={fmtUSDAxis} asTable={asTable} />
          {unpricedModels.length > 0 && <p className="muted text-xs mt-1 mb-0">{t("costs.outside.chartUnpriced")}</p>}
        </div>

        <div className="card mb-4">
          <div style={{ fontWeight: 600, marginBottom: 4 }}>{t("costs.outside.tokensTitle")}</div>
          <p className="muted text-xs mb-2">{t("costs.outside.tokensHint")}</p>
          <SplitTable test="model" title={t("costs.outside.byModel")} rows={models.rows} total={models.total} label={(k) => k} color={modelColor} />
          <SplitTable test="origin" title={t("costs.outside.byOrigin")} rows={origins.rows} total={origins.total} label={originLabel} color={(k) => ORIGIN_COLOR[k]} />
        </div>

        <div className="card mb-4" data-outside-meter>
          <div style={{ fontWeight: 600, marginBottom: 4 }}>{t("costs.outside.meterTitle")}</div>
          <p className="muted text-xs mb-2" style={{ maxWidth: 760 }}>{t("costs.outside.meterHint")}</p>
          {seats.length === 0 ? (
            <p className="muted text-sm mb-0">{sourceRow && !sourceRow.meter_credential ? t("costs.outside.perUse") : t("costs.outside.noMeterLinked")}</p>
          ) : <>
            {seats.map((c) => (
              <p key={`${c.secret_key}#${c.secret_slot}`} className="text-sm mb-1" data-seat={`${c.secret_key}#${c.secret_slot}`}>
                <b>{c.secret_key} #{c.secret_slot}</b>{" · "}
                {c.share != null
                  ? t("costs.outside.seatLine", { real: fmtUSD(c.real_usd), outside: fmtUSD(c.external_real_usd), pct: pct(c.share), covey: fmtUSD(c.covey_real_usd) })
                  : t("costs.outside.seatNoShare", { real: fmtUSD(c.real_usd) })}
                {c.unsplit_steps > 0 && <> · {t("costs.outside.seatUnsplit", { count: c.unsplit_steps, usd: fmtUSD(c.unsplit_real_usd) })}</>}
                {c.unmetered_list_usd > 0 && <> · {t("costs.outside.unmetered", { usd: fmtUSD(c.unmetered_list_usd) })}</>}
                <span className="muted"> · {t("costs.outside.seatSources", { sources: c.sources.join(", ") })}</span>
              </p>
            ))}
            {source != null && <p className="muted text-xs mb-2">{t("costs.outside.sourceNote")}</p>}
            {!fineBucket(bucket) && <p className="muted text-xs mb-2">{t("costs.outside.meterCoarse")}</p>}
            <div style={{ overflowX: "auto" }}>
              <table style={{ width: "100%", borderCollapse: "collapse", fontSize: 12 }} data-meter-table>
                <thead>
                  <tr className="muted" style={{ textAlign: "right" }}>
                    <th scope="col" style={{ textAlign: "left", padding: "2px 8px 4px 0", fontWeight: 500 }}>{t("costs.fig.period")}</th>
                    <th scope="col" style={{ padding: "2px 8px 4px 0", fontWeight: 500 }}>{t("costs.outside.colReal")}</th>
                    <th scope="col" style={{ padding: "2px 8px 4px 0", fontWeight: 500 }}>{t("costs.outside.colOutsideReal")}</th>
                    <th scope="col" style={{ padding: "2px 8px 4px 0", fontWeight: 500 }}>{t("costs.outside.colCoveyReal")}</th>
                    <th scope="col" style={{ padding: "2px 0 4px 0", fontWeight: 500, textAlign: "left" }}>{t("costs.outside.colNote")}</th>
                  </tr>
                </thead>
                <tbody>
                  {meterRows.map((r) => {
                    // No reading, or a reading the outside sessions did not
                    // report: no figure for their share, never 0.
                    const noFigure = r.noReading || r.real == null;
                    const unsplitOnly = !noFigure && r.notReported > 0 && r.outside === 0 && r.covey === 0;
                    const cell = (v: number) => (noFigure || unsplitOnly ? "–" : fmtUSD(v));
                    const note = r.noReading ? t("costs.outside.noReading")
                      : r.real == null ? t("costs.outside.noPlanPrice")
                      : r.notReported > 0 ? t("costs.outside.notReportedHere", { usd: fmtUSD(r.unsplit) })
                      : "";
                    return (
                      <tr key={r.period} style={{ borderTop: "1px solid var(--border)" }} data-meter-row={r.noReading ? "no-reading" : r.notReported > 0 ? "not-reported" : "split"}>
                        <th scope="row" style={{ textAlign: "left", padding: "2px 8px 2px 0", fontWeight: 400 }}>{fmtPeriod(new Date(r.period).toISOString())}</th>
                        <td style={{ textAlign: "right", padding: "2px 8px 2px 0" }}>{r.real != null ? fmtUSD(r.real) : "–"}</td>
                        <td style={{ textAlign: "right", padding: "2px 8px 2px 0" }}>{cell(r.outside)}</td>
                        <td style={{ textAlign: "right", padding: "2px 8px 2px 0" }}>{cell(r.covey)}</td>
                        <td className="muted" style={{ padding: "2px 0" }}>{note}</td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          </>}
        </div>
      </>}
    </div>
  );
}
