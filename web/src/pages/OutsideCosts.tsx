import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Link, useSearchParams } from "react-router";
import { api, type ExternalModelUsage } from "../api";
import { exact, fmtBucket, fmtCount, fmtNumber, fmtUSD, fmtUSDAxis } from "../format";
import { Seg } from "./Costs";
import { rateLine } from "./SubscriptionDent";
import { BarChart, Chip, Swatch, hostsBehindText, type Bar } from "./StackedCostChart";
import {
  FigureTile, OUTSIDE_COLOR, counted, incompleteLines, listLine, listValue, noCallLines, unattributedHalves, unattributedLine, unsplitLines,
} from "./OutsideSessions";
import {
  ORIGINS,
  meterShare,
  unsplitOf,
  unsplitSteps,
  unsplitUSD,
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
  listedSource,
  hostsBehind,
  type SplitRow,
} from "./outsideFigures";

/* The detailed view of the Claude Code sessions outside covey (#120, the
 * human's second comment): like an agent's cost view — list price over time
 * in buckets down to 10 minutes, per model and per origin (main session,
 * subagents, workflows), tokens per type with their dollars, and their share
 * of the subscription meter. Every figure is the server's
 * (GET /cost/external-usage): F1/F2 per cell, F3c per meter reading (each
 * at its chunk's share) — the outside sessions' and covey's parts
 * estimates (≈), the unattributed part (booked to nobody) at least beyond
 * the rate and ≈ where a chunk had no call at all. */

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
  // The $ per type cover the priced calls only, while the token counts above
  // them include the unpriced ones: such a figure carries a mark, and the
  // footnote under the tables says what it means.
  const money = (r: SplitRow, k: string) => (r.typeUSD[k] == null ? "–"
    : r.unpricedTokens > 0 ? <span data-priced-only>{fmtUSD(r.typeUSD[k])}*</span> : fmtUSD(r.typeUSD[k]));
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
      <td style={td} data-list>{r.list != null ? fmtUSD(r.list) : r.calls === 0 ? "–" : <span className="muted">{t("costs.outside.noPrice")}</span>}</td>
      <td style={td} data-reported>
        {r.reported != null ? fmtUSD(r.reported) : "–"}
        {r.reportedCalls > 0 && <div className="muted text-xs">{t("costs.outside.callsShort", counted(r.reportedCalls))}</div>}
      </td>
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
  // A period none of whose calls had a list price has no bar (never one of
  // $0) and says so.
  const groups: Bar[][] = series.map((p) => (!p.priced ? [] : [{
    key: "list", label: t("costs.outside.colList"), color: OUTSIDE_COLOR,
    segs: keys.map((k) => ({ key: k, label: stack === "model" ? k : originLabel(k), color: stack === "model" ? modelColor(k) : ORIGIN_COLOR[k], value: p.v.get(k) ?? 0 })),
  }]));
  const chartNotes = new Map(series.flatMap((p, i) => (p.priced ? [] : [[i, t("costs.outside.noPrice")] as [number, string]])));

  // The meter: the seats the chosen sources run on. A share is always of
  // the whole meter, every source on it included.
  const seats = useMemo(() => {
    if (!data) return [];
    const linked = data.sources.filter((s) => s.meter_credential && (source == null || s.source === source));
    return data.meter.credentials.filter((c) => linked.some((s) => s.meter_credential!.secret_key === c.secret_key && s.meter_credential!.secret_slot === c.secret_slot));
  }, [data, source]);
  // F3c: the outside part is an estimate (≈), and its share is of the whole
  // dent (unattributed and not split included).
  const share = useMemo(() => meterShare(seats), [seats]);
  const meterRows = useMemo(() => (data ? meterPerPeriod(data, cells, bucket, source) : []), [data, cells, bucket, source]);
  const sourceRow = data?.sources.find((s) => s.source === source);
  // With one source chosen, the share is still the whole meter's (the steps
  // carry no list price per source, so it cannot be narrowed honestly): the
  // tile names the other sources on the same seats.
  // Only sources with calls in the range are named (a project folder with
  // no usage sends claim-only requests: 0 calls).
  const listed = useMemo(() => new Set((data?.sources ?? []).filter(listedSource).map((s) => s.source)), [data]);
  const otherSources = source == null ? [] : [...new Set(seats.flatMap((c) => c.sources))].filter((s) => s !== source && listed.has(s)).sort();
  const seatSteps = (c: { secret_key: string; secret_slot: number }) =>
    unsplitOf(data?.meter.steps ?? [], new Set([`${c.secret_key}#${c.secret_slot}`]));
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
        {(data?.sources ?? []).filter((s) => listedSource(s) || s.source === source).map((s) => (
          <Chip key={s.source} on={source === s.source} onClick={() => set({ source: s.source })}>
            {s.source} <span style={{ opacity: 0.7 }}>{s.list_usd != null ? fmtUSD(s.list_usd) : "–"}</span>
          </Chip>
        ))}
      </div>

      {q.isError ? <p className="muted">{t("costs.outside.loadError")}</p> : !data ? null : cells.length === 0 && seats.length === 0 ? (
        <div className="card muted text-sm" style={{ padding: "40px 0", textAlign: "center" }}>{t("costs.outside.empty")}</div>
      ) : <>
        {/* No outside call in the range, but a seat they share has a dent:
            the meter section still shows it (#120 review). */}
        {cells.length === 0 ? (
          <div className="card muted text-sm mb-4" data-outside-empty style={{ padding: "24px 0", textAlign: "center" }}>{t("costs.outside.empty")}</div>
        ) : <>
        <div className="flex flex-wrap gap-3 mb-4" data-outside-tiles>
          <FigureTile test="list" label={t("costs.outside.colList")} color={OUTSIDE_COLOR}
            value={listValue(models.total.list)}
            lines={[listLine(t, models.total.list, models.total.calls)]} />
          {models.total.reported != null && (
            <FigureTile test="reported" label={t("costs.fig.reported")} value={fmtUSD(models.total.reported)}
              lines={[models.total.reportedCalls > 0 && t("costs.outside.reportedCalls", counted(models.total.reportedCalls))]} />
          )}
          <FigureTile test="real" label={t("costs.outside.meterShare")} color={OUTSIDE_COLOR} hatched
            value={seats.length > 0 && share.share != null ? `≈ ${fmtUSD(share.real)}` : "–"}
            lines={[
              seats.length === 0 ? t(sourceRow && !sourceRow.meter_credential ? "costs.outside.noMeterSource" : "costs.outside.noMeterLinked")
                : share.share != null ? t("costs.outside.ofMeter", { pct: pct(share.share), dent: fmtUSD(share.dent) }) : t("costs.outside.noShare"),
              // Beside the share, what the unattributed part is, its two
              // halves, and the chunks whose parts are approximate (F3c).
              seats.length > 0 && (share.share != null || share.unattributed > 0) && unattributedLine(t, share.unattributed),
              seats.length > 0 && share.unattributed > 0 && unattributedHalves(t, share, false).join(" · "),
              ...(seats.length > 0 ? incompleteLines(t, share.incomplete) : []),
              seats.length > 0 && otherSources.length > 0 && t("costs.outside.tileOtherSources", { sources: otherSources.join(", ") }),
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
            fmtPeriod={fmtPeriod} fmtValue={fmtUSD} fmtAxis={fmtUSDAxis} asTable={asTable} notes={chartNotes}
            notesLabel={t("costs.outside.colNote")} />
          {unpricedModels.length > 0 && <p className="muted text-xs mt-1 mb-0">{t("costs.outside.chartUnpriced")}</p>}
        </div>

        <div className="card mb-4">
          <div style={{ fontWeight: 600, marginBottom: 4 }}>{t("costs.outside.tokensTitle")}</div>
          <p className="muted text-xs mb-2">{t("costs.outside.tokensHint")}</p>
          <SplitTable test="model" title={t("costs.outside.byModel")} rows={models.rows} total={models.total} label={(k) => k} color={modelColor} />
          <SplitTable test="origin" title={t("costs.outside.byOrigin")} rows={origins.rows} total={origins.total} label={originLabel} color={(k) => ORIGIN_COLOR[k]} />
          {models.total.unpricedTokens > 0 && <p className="muted text-xs mt-2 mb-0" data-priced-only-note>{t("costs.outside.typePricedOnly")}</p>}
        </div>

        </>}

        <div className="card mb-4" data-outside-meter>
          <div style={{ fontWeight: 600, marginBottom: 4 }}>{t("costs.outside.meterTitle")}</div>
          <p className="muted text-xs mb-2" style={{ maxWidth: 760 }}>{t("costs.outside.meterHint")}</p>
          {seats.length === 0 ? (
            <p className="muted text-sm mb-0">{sourceRow && !sourceRow.meter_credential ? t("costs.outside.perUse") : t("costs.outside.noMeterLinked")}</p>
          ) : <>
            {seats.map((c) => (
              <p key={`${c.secret_key}#${c.secret_slot}`} className="text-sm mb-1" data-seat={`${c.secret_key}#${c.secret_slot}`}>
                <b>{c.secret_key} #{c.secret_slot}</b>{" · "}
                {(() => {
                  // Every reading at no plan price: no real cost, not $0. F3c:
                  // the outside and covey parts ≈, the unattributed part in
                  // its two halves; the share is of the seat's whole real cost.
                  const priced = c.step_count - c.unpriced_steps > 0;
                  const realText = priced ? fmtUSD(c.real_usd) : "–";
                  const sh = meterShare([c]);
                  // "beyond the rate" at $0 only where the seat has a rate to measure it by.
                  // Without k, and nothing moved with no call: "no rate yet", as the headline says.
                  const halves = unattributedHalves(t, sh, sh.share != null && c.points_per_list_usd != null);
                  const unattributed = halves.length > 0 ? halves.join(", ") : t("costs.dent.unattributedNoRate");
                  return sh.share != null
                    ? t("costs.outside.seatLine", { real: realText, outside: fmtUSD(c.external_real_usd), pct: pct(sh.share),
                      covey: fmtUSD(c.covey_real_usd), unattributed })
                    : (c.unattributed_real_usd ?? 0) > 0
                      ? t("costs.outside.seatNoShareUnattributed", { real: realText, unattributed })
                      : t("costs.outside.seatNoShare", { real: realText });
                })()}
                {(() => {
                  const s = seatSteps(c);
                  return [...incompleteLines(t, c.incomplete_chunks ?? 0), ...noCallLines(t, s.unattributed.noCall), ...unsplitLines(t, s.unsplit),
                    ...(s.unpricedSteps > 0 ? [t("costs.outside.unpricedReadings", counted(s.unpricedSteps))] : [])]
                    .map((l, i) => <span key={i} data-seat-unsplit> · {l}</span>);
                })()}
                {c.unmetered_list_usd > 0 && <> · {t("costs.outside.unmetered", { usd: fmtUSD(c.unmetered_list_usd) })}</>}
                {(() => {
                  const hs = hostsBehind(data, c.secret_key, c.secret_slot).filter((h) => listed.has(h.source));
                  return hs.length > 0 ? <span data-hosts-behind> · {hostsBehindText(t, hs)}</span> : null;
                })()}
                <span className="muted"> · {t("costs.outside.seatSources", { sources: c.sources.filter((x) => listed.has(x)).join(", ") })}</span>
                {/* The seat's rate (F3c's k at its latest plan price): what the part beyond the rate is measured by. */}
                {c.step_count > c.unpriced_steps && c.split_steps > 0 && (
                  <span className="muted text-xs" style={{ display: "block" }} data-seat-rate>
                    {rateLine(t, `${c.secret_key} #${c.secret_slot}`, c.points_per_list_usd, c.points_per_list_older_plan_usd ?? null)}
                  </span>
                )}
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
                    <th scope="col" style={{ padding: "2px 8px 4px 0", fontWeight: 500 }}>{t("costs.dent.unattributed")}</th>
                    <th scope="col" style={{ padding: "2px 8px 4px 0", fontWeight: 500 }}>{t("costs.dent.unsplit")}</th>
                    <th scope="col" style={{ padding: "2px 0 4px 0", fontWeight: 500, textAlign: "left" }}>{t("costs.outside.colNote")}</th>
                  </tr>
                </thead>
                <tbody>
                  {meterRows.map((r) => {
                    // The parts are a figure only where a step was split: a
                    // reading not split, idle or at no plan price has no
                    // figure for either part, never $0. Each reason says so.
                    // F3c: outside and covey ≈ (at their chunks' shares); the
                    // unattributed part in its two halves, booked to nobody —
                    // beyond the rate "≥" where a step was split on a seat
                    // with a rate (without one there is no bound), moved with
                    // no call "≈" where the server booked a reading to nobody
                    // whole; not split ≈ where a reading was not split. They
                    // add up to the real cost.
                    const unsplitN = unsplitSteps(r.unsplit);
                    const noCallN = r.unattributed.noCall.steps;
                    const cell = (v: number) => (r.splitSteps > 0 ? `≈ ${fmtUSD(v)}` : "–");
                    const beyond = r.splitSteps > 0 && r.rated;
                    const unattributedCell = !beyond && noCallN === 0 ? "–" : <>
                      {beyond && (
                        <div data-beyond title={t("costs.dent.unattributedBeyond", { usd: fmtUSD(r.unattributed.beyond) })}>
                          ≥ {fmtUSD(r.unattributed.beyond)}
                        </div>
                      )}
                      {noCallN > 0 && (
                        <div data-no-call title={t("costs.dent.unattributedNoCall", { usd: fmtUSD(r.unattributed.noCall.usd) })}>
                          ≈ {fmtUSD(r.unattributed.noCall.usd)}
                        </div>
                      )}
                    </>;
                    const unsplitCell = unsplitN > 0 ? `≈ ${fmtUSD(unsplitUSD(r.unsplit))}` : "–";
                    // No reading on one seat while another seat has one in
                    // the period: both are said.
                    const anyStep = r.splitSteps + noCallN + unsplitN + r.unpricedSteps + r.idleSteps > 0;
                    const notes = [
                      ...(r.noReading ? [t("costs.outside.noReading")] : []),
                      ...noCallLines(t, r.unattributed.noCall),
                      ...unsplitLines(t, r.unsplit),
                      ...(r.unpricedSteps > 0 ? [t("costs.outside.unpricedReadings", counted(r.unpricedSteps))] : []),
                      ...(r.splitSteps + noCallN + unsplitN + r.unpricedSteps === 0 && r.idleSteps > 0 ? [t("costs.outside.idleHere")] : []),
                    ];
                    const note = notes.join(" · ");
                    const kind = !anyStep ? "no-reading"
                      : r.splitSteps > 0 ? (unsplitN + noCallN > 0 ? "partial" : "split")
                      : unsplitN > 0 ? (noCallN > 0 ? "partial" : "unsplit")
                      : noCallN > 0 ? "unattributed" : r.unpricedSteps > 0 ? "unpriced" : "idle";
                    const td = { textAlign: "right", padding: "2px 8px 2px 0", whiteSpace: "nowrap" } as const;
                    return (
                      <tr key={r.period} style={{ borderTop: "1px solid var(--border)" }} data-meter-row={kind} data-meter-period={r.period}>
                        <th scope="row" style={{ textAlign: "left", padding: "2px 8px 2px 0", fontWeight: 400 }}>{fmtPeriod(new Date(r.period).toISOString())}</th>
                        <td style={td}>{r.real != null ? fmtUSD(r.real) : "–"}</td>
                        <td style={td}>{cell(r.outside)}</td>
                        <td style={td}>{cell(r.covey)}</td>
                        <td style={td} data-unattributed>{unattributedCell}</td>
                        <td style={td} data-unsplit>{unsplitCell}</td>
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
