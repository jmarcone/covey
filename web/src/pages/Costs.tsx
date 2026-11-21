import { useEffect, useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { asEnum, asSet, useParamState } from "./costView";
import { Seg } from "./Seg";
import { CostRangeControls, useCostRange } from "./costRangeState";
import { CostTabs } from "./CostHome";
import { isRangeCode, type RangeCode } from "./costRange";
import { api, ApiError, type IndicatorReport, type Principal, type RunCost } from "../api";
import { exact, fmtBucket, fmtCount, fmtUSD } from "../format";
import { PriceList } from "../components/PriceList";
import { CostInsights } from "./CostInsights";
import { CostAnswer } from "./CostAnswer";
import { answerTable } from "./costAnswerModel";
import { coveyElsewhere, credentialFigures, headline, payPerTokenHeadline, subscriptionDent } from "./costFigures";
import { PayPerTokenRow, SubscriptionDentRow } from "./SubscriptionDent";
import { canSetPlan } from "./PlanPrice";
import { OutsideOverview } from "./OutsideSessions";
import { CostHealthBadge, IncompleteMark, credentialIncomplete, incompleteParts, useCostHealth } from "./CostHealth";
import { COVEY_ONLY_PARAM, COVEY_ONLY_VALUE, PRICES_PARAM, PRICES_RECOMPUTED, beforeRange, kpiLead, listedSources,
  outsideKpis, outsideSplit, seatSources, sourceRealParts, type OutsideKpis } from "./outsideFigures";
import {
  AgentFilter,
  BUCKET_MS,
  Chip,
  CostExplorer,
  CustomRangeContext,
  type CredentialMarks,
  OrgWide,
  SOURCE_COLOR,
  Swatch,
  TokenExplorer,
  TYPE_COLOR,
  cellTotals,
  SplitParts,
  leadLines,
  splitLines,
  outsideMoney,
  priceTableLine,
  useBilled,
  useCostCells,
  useUsageReadings,
} from "./StackedCostChart";

// Number formatting lives in format.ts — one for the whole interface; dates
// too, in the UI's language, buckets in UTC as the server cuts them.
//
// Every figure on this page comes from the per-line usage (model_usage):
// what Claude Code reported for its lines, and the same lines' tokens priced
// with the list. Dollars and tokens therefore cover the same calls. What
// covey booked (cost_entries) is not a figure here; the explorer's
// booking-check says when it differs in kind.

type Metric = "cost" | "tokens";
const METRIC = asEnum<Metric>(["cost", "tokens"]);


// --- KPI tiles ---------------------------------------------------------------

function Kpi({ label, value, sub, color, parts, check }: { label: string; value: string; sub?: string; color?: string; parts?: React.ReactNode; check?: string }) {
  return (
    <div className="card" data-kpi={label} style={{ flex: 1, minWidth: 150 }}>
      <div className="muted text-xs" style={{ display: "flex", alignItems: "center", gap: 6 }}>
        {color && <Swatch color={color} />}
        {label}
      </div>
      <div data-kpi-value style={{ fontSize: 24, fontWeight: 600, marginTop: 2 }}>{value}</div>
      {sub && <div className="muted text-xs" style={{ marginTop: 2 }}>{sub}</div>}
      {/* Included, the figure's two parts, always named (#120). */}
      {parts && <div className="muted text-xs" data-parts style={{ marginTop: 2 }}>{parts}</div>}
      {/* covey's price table, only where it differs from the figure (#120). */}
      {check && <div className="muted text-xs" data-price-check style={{ marginTop: 2 }}>{check}</div>}
    </div>
  );
}

// --- Segmented switches ------------------------------------------------------

export { Seg };

// --- Page --------------------------------------------------------------------

// The ranges and buckets (10 min up to 2 days, hour up to 7, 10 h up to 30)
// live in outsideFigures.ts, shared with the outside sessions' own view.

/** The token types side by side. They stand here because the dollar figure
 *  alone hides the key observation: the weight lies almost entirely on the
 *  cached input side, not on what the agents write. Measured on covey.work:
 *  112M cache read against 3.2M output in 24 hours — the prompt is read anew
 *  in every turn. Whoever misses that optimizes the output length and
 *  wonders why nothing happens. The tokens are the lines' own, over every
 *  model, so they are the calls the dollars above are made of. */
function TokenMix({ tokens, split }: { tokens: Record<string, number>; split?: React.ReactNode }) {
  const { t } = useTranslation();
  const parts = [
    { key: "cache_read", value: tokens.cache_read ?? 0 },
    { key: "cache_write_5m", value: tokens.cache_write_5m ?? 0 },
    { key: "cache_write_1h", value: tokens.cache_write_1h ?? 0 },
    { key: "input", value: tokens.input ?? 0 },
    { key: "output", value: tokens.output ?? 0 },
  ];
  const sum = parts.reduce((a, p) => a + p.value, 0);
  if (sum === 0) return <div className="muted text-sm">{t("costs.empty")}</div>;
  return (
    <div>
      <div style={{ display: "flex", height: 10, borderRadius: 5, overflow: "hidden", marginBottom: 12 }}>
        {parts.map((p) => (
          <div key={p.key} style={{ width: `${(p.value / sum) * 100}%`, background: TYPE_COLOR[p.key] }} />
        ))}
      </div>
      {parts.map((p) => (
        <div key={p.key} className="flex items-center justify-between text-sm" style={{ padding: "3px 0" }}>
          <span className="flex items-center gap-2">
            <Swatch color={TYPE_COLOR[p.key]} />
            {t(`costs.tok.${p.key}`)}
          </span>
          <span className="muted" title={exact(p.value)}>
            {fmtCount(p.value)} · {Math.round((p.value / sum) * 100)}%
          </span>
        </div>
      ))}
      {split && <p className="muted text-xs mt-2 mb-0" data-parts>{split}</p>}
    </div>
  );
}

/** The most expensive runs, ranked by what their lines cost at the list.
 *  Answers the question the aggregates leave open: WHICH run burned the
 *  money. A run without actions is marked on purpose — it read, thought and
 *  went back to sleep. */
function ExpensiveRuns({ runs }: { runs: RunCost[] }) {
  const { t } = useTranslation();
  if (runs.length === 0) return <div className="muted text-sm">{t("costs.empty")}</div>;
  const ranked = [...runs].sort((a, b) => (b.measured_usd ?? -1) - (a.measured_usd ?? -1));
  return (
    <div style={{ overflowX: "auto" }}>
      <table style={{ width: "100%", borderCollapse: "collapse", fontSize: 13 }}>
        <thead>
          <tr className="muted" style={{ textAlign: "left" }}>
            <th style={{ padding: "4px 8px 8px 0" }}>{t("costs.runs.task")}</th>
            <th style={{ padding: "4px 8px 8px 0" }}>{t("costs.runs.agent")}</th>
            <th style={{ padding: "4px 8px 8px 0", textAlign: "right" }}>{t("costs.runs.recomputed")}</th>
            <th style={{ padding: "4px 8px 8px 0", textAlign: "right" }}>{t("costs.runs.cached")}</th>
            <th style={{ padding: "4px 0 8px 0", textAlign: "right" }}>{t("costs.runs.actions")}</th>
          </tr>
        </thead>
        <tbody>
          {ranked.map((r) => (
            <tr key={r.task_id} style={{ borderTop: "1px solid var(--border)" }}>
              <td style={{ padding: "6px 8px 6px 0" }}>
                <div style={{ maxWidth: 340, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
                  {r.title}
                </div>
                <span className="muted text-xs">
                  {r.origin} · {r.state} · {fmtBucket(r.started_at, "hour")}
                </span>
              </td>
              <td style={{ padding: "6px 8px 6px 0" }}>
                <Link to={`/agents/${r.agent_id}`}>{r.slug}</Link>
              </td>
              <td style={{ padding: "6px 8px 6px 0", textAlign: "right", fontWeight: 600 }}>
                {r.measured_usd != null ? fmtUSD(r.measured_usd) : <span className="muted" style={{ fontWeight: 400 }}>{t("costs.runs.noLines")}</span>}
              </td>
              <td style={{ padding: "6px 8px 6px 0", textAlign: "right" }} className="muted">
                {fmtCount(r.cache_read_tokens + r.cache_creation_tokens)}
              </td>
              <td style={{ padding: "6px 0", textAlign: "right" }}>
                {r.actions === 0 ? (
                  <span title={t("costs.runs.idleHint")} style={{ color: "var(--clay)" }}>
                    {t("costs.runs.idle")}
                  </span>
                ) : (
                  <span className="muted">{r.actions}</span>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/** No outside source switched off: the chips are gone in "covey only", and so is what they switched off. */
const NO_SOURCES: ReadonlySet<string> = new Set();

export default function Costs({ me }: { me?: Principal }) {
  const { t } = useTranslation();

  // The window is the cost pages' shared one (useCostRange): the home and this page read it the same way.
  const r = useCostRange();
  const { params, setParams, days, range, badRange, win, custom, wq, bucket, tz } = r;
  const [metric, setMetric] = useParamState<Metric>("metric", "cost", METRIC.parse, METRIC.show);
  // The agent filter replaces the scope tabs: none selected = the whole
  // organisation, any combination for the charts and the KPIs; exactly one
  // also narrows the cards below to that agent.
  const [selectedRO, setSelectedRO] = useParamState<ReadonlySet<string>>("agents", NO_SOURCES, asSet.parse, asSet.show);
  const selected = selectedRO as Set<string>;
  const setSelected = setSelectedRO as (v: Set<string> | ((prev: Set<string>) => Set<string>)) => void;
  // The sessions outside covey are included by default and labelled
  // everywhere (#120); ?only=covey shows covey's agents alone, as before.
  const coveyOnly = params.get(COVEY_ONLY_PARAM) === COVEY_ONLY_VALUE;
  const setCoveyOnly = (v: boolean) => setParams((prev) => {
    const n = new URLSearchParams(prev);
    if (v) n.set(COVEY_ONLY_PARAM, COVEY_ONLY_VALUE);
    else n.delete(COVEY_ONLY_PARAM);
    return n;
  }, { replace: true });
  // One list-price figure leads (#120); ?prices=recomputed shows covey's
  // price table as a figure of its own again, in the tiles, the chart and
  // its tables.
  const fullPrices = params.get(PRICES_PARAM) === PRICES_RECOMPUTED;
  const setFullPrices = (v: boolean) => setParams((prev) => {
    const n = new URLSearchParams(prev);
    if (v) n.set(PRICES_PARAM, PRICES_RECOMPUTED);
    else n.delete(PRICES_PARAM);
    return n;
  }, { replace: true });
  // The outside sources switched off in the chart's chips: the answer table's
  // rows follow them (#195).
  const [offSources, setOffSources] = useParamState<ReadonlySet<string>>("off", NO_SOURCES, asSet.parse, asSet.show);
  const toggleSource = (n: string) => setOffSources((v) => { const x = new Set(v); if (x.has(n)) x.delete(n); else x.add(n); return x; });
  const scope = selected.size === 1 ? [...selected][0] : "org";
  const cells = useCostCells(win, bucket, !badRange);
  const billed = useBilled(win, bucket, !badRange);
  const readings = useUsageReadings(win, !badRange);
  // The server refused the range (a 400 with its code): said in the panel, in
  // the reader's language.
  const apiCode: RangeCode | "unknown" | null = cells.error instanceof ApiError && cells.error.status === 400
    ? (isRangeCode(cells.error.code) ? cells.error.code : "unknown") : null;
  const rangeError: RangeCode | "unknown" | null = range.error ?? apiCode;
  const refused = badRange || apiCode != null;

  // A new range can lose agents the selection still names: they would narrow
  // every figure to nothing while no chip shows them. Drop them.
  // Writes the address only when it drops someone: a write is a navigation, and the data refetches every 30 s.
  useEffect(() => {
    if (!cells.data) return;
    const ids = new Set(cells.data.agents.map((a) => a.id));
    const kept = [...selected].filter((id) => ids.has(id));
    if (kept.length !== selected.size) setSelected(new Set(kept));
  }, [cells.data, selected, setSelected]);

  // The most expensive runs of the period — org-wide or for the chosen agent.
  const runs = useQuery({
    queryKey: ["cost", "runs", scope, wq],
    queryFn: () =>
      api<RunCost[] | null>(
        scope === "org" ? `/cost/runs?${wq}&limit=25` : `/agents/${scope}/cost/runs?${wq}&limit=25`,
      ),
    refetchInterval: 30000,
    enabled: !badRange,
  });

  // The price list hangs on the same scope and period as everything else on
  // this page — performance is no second topic next to the costs, but the
  // other half of the same one.
  const indicators = useQuery({
    queryKey: ["cost", "indicators", scope, days],
    queryFn: () =>
      api<IndicatorReport>(
        scope === "org" ? `/cost/indicators?days=${days}` : `/agents/${scope}/cost/indicators?days=${days}`,
      ),
    refetchInterval: 30000,
    // The metrics of ONE agent follow the work record (spec/21): controlling
    // gets a 403 on it. Without retry:false that would be three retries
    // every 30 seconds for an answer that does not change.
    // Performance.tsx does the same on the same route.
    retry: false,
    // The price list is over whole quick ranges (its trend sets the span
    // against the one before it): not asked for a custom range.
    enabled: !custom && !badRange,
  });
  // A role that may not see something should not get it as an empty box:
  // empty means "there are no metrics", and that is a different statement
  // than "you do not see them".
  const indicatorsForbidden = indicators.error instanceof ApiError && indicators.error.status === 403;


  // The credentials' figures over the whole organisation, for the meter card:
  // the same the tiles show without an agent filter.
  // The end of the window: now, or a custom range's end (cut to now).
  const end = useMemo(() => (cells.data?.until ? new Date(cells.data.until) : new Date()), [cells.data]);
  const orgFigures = useMemo(() => (cells.data ? credentialFigures({
    cells: cells.data, billed: billed.data, bucketMs: BUCKET_MS[bucket] ?? BUCKET_MS.day, windowEnd: end,
    includeOutside: !coveyOnly && listedSources(cells.data.external).length > 0,
  }) : []), [cells.data, billed.data, bucket, coveyOnly, end]);

  // The whole dent in the subscription meters, split between covey's agents
  // and the sessions outside covey (#120): null until a source names a meter.
  const dent = useMemo(() => subscriptionDent(cells.data?.external), [cells.data]);
  const head = useMemo(() => headline(cells.data), [cells.data]);
  // The answer table (#195): the headline's and the dent's figures as rows.
  const answer = useMemo(() => (head && dent && cells.data?.external ? answerTable(head, dent, cells.data.external, cells.data.meter ?? [], coveyOnly ? NO_SOURCES : offSources) : null),
    [head, dent, cells.data, offSources, coveyOnly]);
  const ppt = useMemo(() => payPerTokenHeadline(cells.data, orgFigures, BUCKET_MS[bucket] ?? BUCKET_MS.day, end),
    [cells.data, orgFigures, bucket, end]);

  // KPIs over the same lines as the charts, narrowed by the same filter.
  const totals = useMemo(() => cellTotals(cells.data, selected), [cells.data, selected]);
  // The outside sessions with calls in the range; null for covey only or none.
  const ext = cells.data?.external;
  const sourcesListed = useMemo(() => listedSources(ext), [ext]);
  const outside = useMemo(() => (coveyOnly ? null : outsideKpis(ext)), [ext, coveyOnly]);
  // The outside part in two (#163): the covey operator and the other projects.
  const split = useMemo(() => (coveyOnly ? null : outsideSplit(ext)), [ext, coveyOnly]);
  const realParts = useMemo(() => sourceRealParts(ext), [ext]);
  const included = outside != null ? ext! : null;
  const filtered = selected.size > 0;
  const coveyTok = totals.tokens;
  const tok = useMemo(() => {
    if (!outside) return coveyTok;
    const m: Record<string, number> = { ...coveyTok };
    for (const [k, v] of Object.entries(outside.tokens)) m[k] = (m[k] ?? 0) + v;
    return m;
  }, [coveyTok, outside]);
  const inputOf = (x: Record<string, number>) => (x.input ?? 0) + (x.cache_read ?? 0) + (x.cache_write_5m ?? 0) + (x.cache_write_1h ?? 0);
  const input = inputOf(tok);
  // The hit rate: what was read from the cache, of all input. A cache write
  // is a miss that pays extra to be a hit next time, not a hit.
  const cached = tok.cache_read ?? 0;
  const output = tok.output ?? 0;
  const parts = (covey: string, money: (o: OutsideKpis) => string) => (!outside || !split ? undefined
    : <SplitParts lines={splitLines(t, { filtered, covey, split, money })} />);
  const tokParts = (f: (x: Record<string, number>) => number) => parts(fmtCount(f(coveyTok)), (o) => fmtCount(f(o.tokens)));
  // The lead figure: covey's lines at Claude Code's figure, the outside
  // sessions at their lead price.
  const lead = totals.reported + (outside ? kpiLead(outside).lead ?? 0 : 0);

  const agentList = cells.data?.agents ?? [];
  const scopeName =
    selected.size === 0
      ? t("costs.wholeOrg")
      : selected.size === 1
        ? agentList.find((a) => a.id === scope)?.label ?? ""
        : t("costs.filter.nSelected", { count: selected.size });
  const fmtPeriod = (p: string, utc?: boolean) => fmtBucket(p, bucket, utc ? "utc" : tz);
  // A failed data check marks the part it affects "incomplete" (#178); the
  // figure stays as it is.
  // The token-price checks count where the page holds the calls they
  // concern: covey's lines always, the outside sessions where included.
  const health = useCostHealth();
  // The checks run at now. They say something about a window that reaches now (a quick range, or a custom one ending
  // within the hour); on a window in the past their marks would be today's, not its own (#200 merge review): none.
  const windowEndMs = cells.data?.until ? Date.parse(cells.data.until) : custom ? range.to! : Date.now();
  const windowIsNow = !custom || Date.now() - windowEndMs < 3_600_000;
  const healthData = windowIsNow ? health.data : undefined;
  const incomplete = incompleteParts(healthData, { agents: true, outside: included != null });
  const outsideMark = coveyOnly ? [] : incomplete.outside;
  // The answer card is the whole dent under "covey only" too (#195): its operator and project rows stand, so their
  // marks do as well, whatever the chips and tiles leave out (#179 merge review).
  const cardMarks = incompleteParts(healthData, { agents: true, outside: true });
  // Per credential: a reset nobody recorded on its meter, and a stopped
  // uploader of a source on its seat while the outside part is included,
  // leave its own real cost and multiples short — its block and its meter
  // in the insights say so where those figures stand.
  const credentialMarks = (key: string, slot: number): CredentialMarks => {
    const m = credentialIncomplete(healthData, `${key} #${slot}`, included ? seatSources(included, key, slot).map((x) => x.source) : null);
    return { multiple: <IncompleteMark checks={m.multiple} part="multiple" />, outside: <IncompleteMark checks={m.outside} part="outside" /> };
  };

  // The agent filter and the "covey only" chip. With the answer table they stand under it, next to the chart they
  // drive; its rows name the outside sources, so the inclusion note is only said where there is no table (#204).
  const hasAnswer = !!(answer && head && dent);
  const filters = (
    <>
      <div className="mb-4">
        <AgentFilter agents={agentList} selected={selected} onChange={setSelected} />
      </div>
      {/* The sessions outside covey: in every figure below by default, each
          figure naming its parts; "covey only" leaves them out (#120). */}
      {sourcesListed.length > 0 && (
        <div className="flex flex-wrap items-center gap-2 mb-4" data-inclusion>
          <Chip on={coveyOnly} onClick={() => setCoveyOnly(!coveyOnly)} title={t("costs.incl.coveyOnlyTitle")}>{t("costs.incl.coveyOnly")}</Chip>
          <IncompleteMark checks={outsideMark} part="outside" />
          {(!hasAnswer || coveyOnly || filtered) && (
            <span className="muted text-xs" data-inclusion-note style={{ maxWidth: 760 }}>
              {t(coveyOnly ? "costs.incl.coveyOnlyNote" : filtered ? "costs.incl.includedNoteSelected" : "costs.incl.includedNote",
                { sources: sourcesListed.map((s) => s.source).join(", ") })}
            </span>
          )}
        </div>
      )}
    </>
  );
  // covey's own work no row of the page covers: the headline's, less what the pay-per-token row shows of each credential.
  const elsewhere = head ? coveyElsewhere(head.coveyElsewhereBy, ppt) : 0;
  return (
    <div>
      <div className="flex items-baseline gap-3 mb-1">
        <h1 className="text-[22px]">{t("costs.title")}</h1>
        <CostTabs />
        <span className="muted">{scopeName}</span>
        <CostHealthBadge />
      </div>
      <p className="muted text-sm mb-4" style={{ maxWidth: 620 }}>{t("costs.subtitle")}</p>

      {/* Controls */}
      <CostRangeControls r={r} rangeError={rangeError} />
      {refused && <p className="muted text-sm" data-range-refused>{t("costs.range.refused")}</p>}

      {!refused && (
      <CustomRangeContext.Provider value={custom}>

      {/* Without an answer table the filters lead, as before; with one they follow it (#204: the answer first). */}
      {!(answer && head && dent) && filters}

      {/* The answer, first (#195): one row per consumer of the subscription at
          token prices and its share of the real cost, the total, the multiple
          and one sentence. What explains it is in the drawer. */}
      {answer && head && dent && (
        <CostAnswer a={answer} h={head} filtered={selected.size > 0} elsewhere={elsewhere}
          marks={cardMarks.multiple.length + cardMarks.outside.length + cardMarks.tokenPrice.length > 0 && <>
            <IncompleteMark checks={cardMarks.multiple} part="multiple" />
            <IncompleteMark checks={cardMarks.outside} part="outside" />
            <IncompleteMark checks={cardMarks.tokenPrice} part="tokenPrice" />
          </>}
          drawer={<SubscriptionDentRow d={dent} filtered={selected.size > 0} h={head}
            sourceParts={(cells.data?.external?.meter.source_parts ?? []).flatMap((p) => {
              const s = sourcesListed.find((x) => x.source === p.source);
              return s ? [{ ...p, before: beforeRange(s, realParts) }] : [];
            })} />} />
      )}

      {/* Pay per token (0152): an API key's bill, usage charges and the
          monthly meter — its own headline, never added to the seats'. Where
          the money is per token this is the answer, so it stays directly
          under the answer table (#195). */}
      {ppt && <PayPerTokenRow h={ppt} filtered={selected.size > 0} />}

      {incomplete.tokenPrice.length > 0 && !answer && <div className="mb-2" data-kpi-incomplete><IncompleteMark checks={incomplete.tokenPrice} part="tokenPrice" /></div>}
      {answer && head && dent && filters}

      {/* Without a subscription meter there is no answer table: the lead
          figure stands as a tile, as before. One list-price figure leads
          (#120); the toggle shows covey's price table apart. */}
      {!answer && (
        <div className="flex flex-wrap gap-3 mb-4">
          {fullPrices ? <>
            <Kpi label={t("costs.fig.reported")} value={fmtUSD(totals.reported + (outside?.reported ?? 0))} sub={t("costs.kpiAllCredentials")} color={SOURCE_COLOR.reported}
              parts={parts(fmtUSD(totals.reported), (o) => outsideMoney(t, o, "reported"))} />
            <Kpi label={t("costs.fig.recomputed")} value={fmtUSD(totals.recomputed + (outside?.list ?? 0))} sub={t("costs.kpiAllCredentials")} color={SOURCE_COLOR.recomputed}
              parts={parts(fmtUSD(totals.recomputed), (o) => outsideMoney(t, o, "list"))} />
          </> : (
            <Kpi label={t("costs.lead.label")} value={fmtUSD(lead)} sub={t("costs.kpiAllCredentials")} color={SOURCE_COLOR.reported}
              parts={outside && split ? <SplitParts stacked lines={leadLines(t, filtered, fmtUSD(totals.reported), split)} /> : undefined}
              check={priceTableLine(t, lead, totals.recomputed + (outside?.list ?? 0))} />
          )}
        </div>
      )}

      {/* Chart */}
      <div className="card mb-4">
        <div className="flex items-center justify-between mb-3 flex-wrap gap-2">
          <div style={{ fontWeight: 600, display: "inline-flex", alignItems: "center", gap: 6 }} data-chart-title>
            {metric === "cost" ? t("costs.chartCost") : t("costs.chartTokens")}
            {/* A missing list price leaves the token counts whole; a call booked twice does not. */}
            <IncompleteMark checks={metric === "cost" ? incomplete.tokenPrice : incomplete.tokenPrice.filter((c) => c.id === "c4")} part="tokenPrice" />
          </div>
          <Seg
            value={metric}
            onChange={setMetric}
            options={[{ v: "cost", label: t("costs.mCost") }, { v: "tokens", label: t("costs.mTokens") }]}
          />
        </div>
        {metric === "cost" ? (
          <CostExplorer data={cells.data} billed={billed.data} readings={readings.data ?? []} canSetPlan={canSetPlan(me?.Role ?? "")} selected={selected} bucket={bucket} fmtPeriod={fmtPeriod}
            outside={included} full={fullPrices} onFull={setFullPrices} off={offSources} onToggleSource={toggleSource} answered={answer?.covers}
            marks={credentialMarks} />
        ) : (
          <TokenExplorer data={cells.data} selected={selected} fmtPeriod={fmtPeriod} outside={included} />
        )}
      </div>

      {/* The sessions outside covey (#120): apart or combined, never folded in unannounced. */}
      <OutsideOverview data={cells.data} selected={selected} days={days} bucket={bucket} fmtPeriod={fmtPeriod} detailLink={!custom}
        incomplete={<IncompleteMark checks={incomplete.outside} part="outside" />} />

      {/* The token counts: collapsed, they answer another question (#195). */}
      <details className="card mb-4" data-tokens>
        <summary style={{ fontWeight: 600, cursor: "pointer" }}>
          {t("costs.answer.tokens")}
          {/* A call booked twice (c4) inflates the token counts: marked where they stand (#179 merge review). */}
          <span data-tokens-incomplete style={{ marginLeft: 8, fontWeight: 400 }}><IncompleteMark checks={incomplete.tokenPrice.filter((c) => c.id === "c4")} part="tokenPrice" /></span>
        </summary>
        <div className="flex flex-wrap gap-3 mt-3 mb-4">
          <Kpi
            label={t("costs.inputTokens")}
            value={fmtCount(input)}
            sub={t("costs.cachedShare", { pct: input > 0 ? Math.round((cached / input) * 100) : 0 })}
            parts={tokParts(inputOf)}
          />
          <Kpi label={t("costs.outputTokens")} value={fmtCount(output)} sub={exact(output)} parts={tokParts((x) => x.output ?? 0)} />
          <Kpi label={t("costs.totalTokens")} value={fmtCount(input + output)} parts={tokParts((x) => inputOf(x) + (x.output ?? 0))} />
        </div>
        <div style={{ fontWeight: 600, marginBottom: 4 }}>{t("costs.mix.title")}</div>
        <p className="muted text-xs mb-3" style={{ maxWidth: 620 }}>{t("costs.mix.hint")}</p>
        <TokenMix tokens={tok} split={tokParts((x) => inputOf(x) + (x.output ?? 0))} />
      </details>

      {/* Price list + token types + priciest runs: they hold for every scope */}
      <div className="grid gap-4 mb-4" style={{ gridTemplateColumns: "repeat(auto-fit, minmax(min(320px, 100%), 1fr))" }}>
        <div className="card">
          <div style={{ fontWeight: 600, marginBottom: 4 }}>{t("costs.indicators.title")}{scope === "org" && selected.size > 0 && <OrgWide />}</div>
          <p className="muted text-xs mb-3">{t("costs.indicators.hint")}</p>
          {custom ? (
            <p className="muted text-xs" data-indicators-custom>{t("costs.range.indicatorsCustom")}</p>
          ) : indicatorsForbidden ? (
            <p className="muted text-xs">{t("costs.indicators.restricted")}</p>
          ) : (
            <PriceList rep={indicators.data} />
          )}
        </div>
        <div className="card" style={{ gridColumn: "span 1" }}>
          <div style={{ fontWeight: 600, marginBottom: 4 }}>{t("costs.runs.title")}{scope === "org" && selected.size > 0 && <OrgWide />}</div>
          <p className="muted text-xs mb-3">{t("costs.runs.hint")}</p>
          <ExpensiveRuns runs={runs.data ?? []} />
        </div>
      </div>

      {/* Cost insights: per model as Claude Code reported it, and the
          subscription meter per credential. The model lines follow the agent
          filter; what is per credential stays org-wide and says so. */}
      <CostInsights days={win.days} win={custom ? win : undefined} figures={orgFigures} agents={selected} outside={included} answered={answer?.covers}
        incomplete={<IncompleteMark checks={incomplete.tokenPrice} part="tokenPrice" />} marks={credentialMarks} />
      </CustomRangeContext.Provider>
      )}
    </div>
  );
}
