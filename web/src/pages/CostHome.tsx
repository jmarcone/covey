import { useMemo, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Link, Navigate } from "react-router";
import type { TFunction } from "i18next";
import { CostTabs } from "./CostTabs";
import { api, ApiError, type CostHealthReport, type ModelUsageReport, type RunCost } from "../api";
import { fmtBucket, fmtNumber, fmtUSD, fmtUSDFixed } from "../format";
import { CostAnswer } from "./CostAnswer";
import { answerTable, billedKeys, type AnswerTable } from "./costAnswerModel";
import { basisSupport, useBasis, type Basis, type BasisSupport } from "./costBasisSwitch";
import { BasisLockChip, BasisTag } from "./BasisControl";
import { NoFigure } from "./NoFigure";
import { headline, subscriptionDent, type Headline } from "./costFigures";
import { isRangeCode, type RangeCode } from "./costRange";
import { CostFilterBar, ShareButton, WhoSummary, useCostRange } from "./costRangeState";
import { runUSD, useCostQuery } from "./costView";
import { CostHealthBadge, IncompleteMark, incompleteParts, useCostHealth } from "./CostHealth";
import { useBilled, useCostCells } from "./StackedCostChart";
import { SubscriptionDentRow } from "./SubscriptionDent";

/* The cost home (/costs, cost home PR 5): the one answer a C-level reader needs, read across the room on a 27"
 * screen, then doors to the views that answer the next questions. The hero is the equation itself: the multiple
 * never stands without what it divides and what it divides by. Beside it, whether the data can be trusted right
 * now; under it, which span the meter covers and how all the work at token prices reconciles to the metered part.
 * The parts table is the answer card's, its method in the drawer. Everything follows the page's one range. */

const NO_SOURCES: ReadonlySet<string> = new Set();
const times = (v: number) => `${fmtNumber(v, 1)}×`;

/** An age in minutes, in the unit a reader takes in at a glance: a stopped uploader reads "3 days ago", not "4320 min ago". */
function ago(t: TFunction, n: number): string {
  if (n < 90) return t("costs.home.minutesAgo", { n });
  if (n < 48 * 60) return t("costs.home.hoursAgo", { n: Math.round(n / 60) });
  return t("costs.home.daysAgo", { n: Math.round(n / 1440) });
}

/** The details page's own settings (PR 2): a link that carries them was made on the full page, which lived at /costs
 *  before the home did, so it opens there (#217 review). */
const DETAIL_KEYS = ["agents", "metric", "layout", "view", "table", "hide", "ttable", "off", "outside", "outmode", "prices", "only"];

export { CostTabs } from "./CostTabs";

function Hero({ a }: { a: AnswerTable }) {
  const { t } = useTranslation();
  const pts = a.points != null ? fmtNumber(a.points, Number.isInteger(a.points) ? 0 : 1) : null;
  if (a.real == null) {
    return (
      <div className="cost-home-eq" data-home-hero>
        <div><div className="cost-home-label">{t("costs.answer.heroList")}</div><div className="cost-home-fig">{fmtUSD(a.list)}</div></div>
      </div>
    );
  }
  const multiple = a.multiple;
  return (
    <div className="cost-home-eq" data-home-hero>
      <div>
        <div className="cost-home-label">{t(multiple != null ? "costs.answer.heroMetered" : "costs.answer.heroList")}</div>
        <div className="cost-home-fig" data-home-metered>{fmtUSD(multiple != null ? a.metered : a.list)}</div>
      </div>
      {multiple != null && <div className="cost-home-op" aria-hidden="true">÷</div>}
      <div>
        <div className="cost-home-label">{t("costs.answer.heroCapacity")}</div>
        <div className="cost-home-fig cost-home-capacity" data-home-capacity>≈ {fmtUSD(a.real)}</div>
        {pts && <div className="cost-home-sub">{a.rate != null ? t("costs.answer.totalSub", { pts, rate: fmtUSDFixed(a.rate, 4) }) : t("costs.answer.heroPoints", { pts })}</div>}
      </div>
      {multiple != null && <div className="cost-home-op" aria-hidden="true">=</div>}
      {multiple != null && (
        <div>
          <div className="cost-home-label">{t("costs.answer.heroMultiple")}</div>
          <div className="cost-home-fig" data-home-multiple>≈ {times(multiple)}</div>
          {a.floor != null && <div className="cost-home-sub">{t("costs.answer.floor", { floor: times(a.floor) })}</div>}
        </div>
      )}
    </div>
  );
}

/** Whether the data can be trusted right now, and how far the meter's week has gone. */
function Trust({ rep, seats }: { rep: CostHealthReport | undefined; seats: number }) {
  const { t } = useTranslation();
  const c1 = rep?.checks.find((c) => c.id === "c1"), c2 = rep?.checks.find((c) => c.id === "c2");
  const week = c2?.facts.week_percent;
  return (
    <aside className="cost-home-trust" aria-label={t("costs.home.trustLabel")} data-home-trust>
      <CostHealthBadge />
      {c1?.facts.last_push_minutes != null && <div className="cost-home-row"><span>{t("costs.home.uploader")}</span><span>{ago(t, c1.facts.last_push_minutes)}</span></div>}
      {c2?.facts.reading_minutes != null && <div className="cost-home-row"><span>{t("costs.home.meterRead")}</span><span>{ago(t, c2.facts.reading_minutes)}</span></div>}
      {week != null && (
        <div className="cost-home-gauge" data-home-week>
          {/* c2 speaks for the seat that decides the check: with several seats, it names that seat. */}
          <div className="cost-home-label">{seats > 1 && c2?.facts.credential
            ? t("costs.home.weekSeat", { seat: c2.facts.credential, pct: fmtNumber(week, 0) })
            : t("costs.home.week", { pct: fmtNumber(week, 0) })}</div>
          <div className="cost-home-bar" role="meter" aria-valuemin={0} aria-valuemax={100} aria-valuenow={week} aria-label={t("costs.home.weekLabel")}>
            <span style={{ width: `${Math.min(100, Math.max(0, week))}%` }} />
          </div>
        </div>
      )}
    </aside>
  );
}

/** Which span the meter covers: before its first reading, between its readings, after its last. */
function Coverage({ h, since, until, first, last, seats, basis = "token", real = null }: {
  h: Headline; since: number; until: number; first: number | null; last: number | null; seats: number;
  /** The page's basis: under capacity the metered span carries the capacity used (≈), the spans around it have none
   *  (before the meter, pending); under billed no span has a bill (bills are per API key). */
  basis?: Basis; real?: number | null;
}) {
  const { t } = useTranslation();
  if (first == null || last == null || !(until > since)) return null;
  // The amounts are per seat, against each seat's own readings: a strip drawn from several seats' merged span would
  // hide one seat's work before its first reading (#217 review). So the strip is drawn for one seat only.
  const strip = seats === 1;
  const span = until - since;
  const a = Math.max(0, Math.min(first, until) - since), b = Math.max(0, Math.min(last, until) - Math.max(first, since)), c = Math.max(0, until - Math.max(last, since));
  const pct = (v: number) => `${(v / span) * 100}%`;
  const at = (ms: number) => fmtBucket(new Date(ms).toISOString(), "hour");
  return (
    <div className="cost-home-coverage" data-home-coverage>
      {strip && <div className="cost-home-strip" aria-hidden="true">
        {a > 0 && <span className="none" style={{ width: pct(a) }} />}
        {b > 0 && <span className="metered" style={{ width: pct(b) }} />}
        {c > 0 && <span className="after" style={{ width: pct(c) }} />}
      </div>}
      <div className="cost-home-strip-labels">
        {(h.before >= 0.005 || (strip && a > 0)) && <span data-home-cov-before>{basis === "token"
          ? t("costs.home.covNone", { from: at(since), usd: fmtUSD(h.before) })
          : <>{t("costs.basisViews.home.covNone", { from: at(since) })} <NoFigure reason={basis === "capacity" ? "beforeMeter" : "notAtThisLevel"} /></>}</span>}
        <span data-home-cov-metered>{basis === "token"
          ? t("costs.home.covMetered", { from: at(Math.max(first, since)), to: at(Math.min(last, until)), usd: fmtUSD(h.metered) })
          : <>{t("costs.basisViews.home.covMetered", { from: at(Math.max(first, since)), to: at(Math.min(last, until)) })}{" "}
            {basis === "capacity" && real != null ? `≈ ${fmtUSD(real)}` : <NoFigure reason={basis === "capacity" ? "noData" : "notAtThisLevel"} />}</>}</span>
        {(h.after >= 0.005 || (strip && c > 0)) && <span data-home-cov-after>{basis === "token"
          ? t("costs.home.covAfter", { usd: fmtUSD(h.after) })
          : <>{t("costs.basisViews.home.covAfter")} <NoFigure reason={basis === "capacity" ? "pending" : "notAtThisLevel"} /></>}</span>}
      </div>
    </div>
  );
}

function Door({ to, label, value, sub, testid, lock }: {
  to: string; label: string; value: ReactNode; sub?: string; testid: string;
  /** The door's figure keeps the token price under the page's basis: the chip stands under the door, outside the link. */
  lock?: { support: BasisSupport; basis: Basis };
}) {
  const { t } = useTranslation();
  const link = (
    <Link to={to} className="cost-home-door" data-home-door={testid}>
      <span className="cost-home-label">{label}</span>
      <span className="cost-home-door-value">{value}</span>
      {sub && <span className="cost-home-sub">{sub}</span>}
      <span className="cost-home-open">{t("costs.home.open")}</span>
    </Link>
  );
  if (!lock || lock.support.state !== "locked") return link;
  return <div style={{ display: "grid", gap: 6, alignContent: "start" }} data-home-door-lock={testid}>{link}<BasisLockChip support={lock.support} basis={lock.basis} /></div>;
}

/** B1 (cost home PR 9): under billed, what Anthropic billed the mapped API keys, on its own line under the hero and
 *  never in the equation: pay-per-token keys, whole keys (not only covey's use of them), whole billed buckets. */
function BilledLine({ figure, loading }: { figure: { usd: number; until: string } | null; loading: boolean }) {
  const { t } = useTranslation();
  return (
    <p className="text-sm mt-2 mb-0" data-home-billed={loading ? "loading" : figure ? figure.usd.toFixed(2) : "none"}>
      <BasisTag basis="billed" />{" "}
      <span>{t("costs.basisViews.home.billedLabel")}: </span>
      {loading ? <span className="muted">…</span> : figure ? <b data-home-billed-usd>{fmtUSD(figure.usd)}</b> : <NoFigure reason="noBilledKeys" />}
      {!loading && figure && <span className="muted text-xs"> · {t("costs.basisViews.home.billedUntil", { until: fmtBucket(figure.until, "hour") })}</span>}
    </p>
  );
}

export default function CostHome() {
  const { t } = useTranslation();
  const r = useCostRange();
  const { win, wq, bucket, badRange } = r;
  const toDetails = DETAIL_KEYS.some((k) => r.params.has(k));
  const cells = useCostCells(win, bucket, !badRange);
  const apiCode: RangeCode | "unknown" | null = cells.error instanceof ApiError && cells.error.status === 400
    ? (isRangeCode(cells.error.code) ? cells.error.code : "unknown") : null;
  const rangeError = r.range.error ?? apiCode;
  const refused = badRange || apiCode != null;
  const health = useCostHealth();
  const q = useCostQuery(["source"]);
  const { basis } = useBasis();
  const billed = useBilled(win, bucket, basis === "billed" && !badRange);
  const billedFigure = useMemo(() => billedKeys(billed.data?.window), [billed.data]);
  const heroSupport = basisSupport("home", "hero", basis);
  const detailsSupport = basisSupport("home", "doorDetails", basis);
  const runSupport = basisSupport("home", "doorRun", basis);

  const head = useMemo(() => headline(cells.data), [cells.data]);
  const dent = useMemo(() => subscriptionDent(cells.data?.external), [cells.data]);
  const a = useMemo(() => (head && dent && cells.data?.external ? answerTable(head, dent, cells.data.external, cells.data.meter ?? [], NO_SOURCES) : null),
    [head, dent, cells.data]);
  // The checks run at now: on a custom window in the past their marks would be today's (#200 merge review).
  const windowEnd = cells.data?.until ? Date.parse(cells.data.until) : r.custom ? r.range.to! : Date.now();
  const windowIsNow = !r.custom || Date.now() - windowEnd < 3_600_000;
  const marks = incompleteParts(windowIsNow ? health.data : undefined, { agents: true, outside: true });

  const models = useQuery({ queryKey: ["cost", "models", wq], queryFn: () => api<ModelUsageReport>(`/cost/models?${wq}`), enabled: !refused });
  const runs = useQuery({ queryKey: ["cost", "runs", "org", wq, 1], queryFn: () => api<RunCost[] | null>(`/cost/runs?${wq}&limit=1`), enabled: !refused });
  const topModel = useMemo(() => {
    const by = new Map<string, number>();
    for (const row of models.data?.rows ?? []) by.set(row.model, (by.get(row.model) ?? 0) + row.engine_usd);
    const all = [...by.values()].reduce((x, y) => x + y, 0);
    const best = [...by.entries()].sort((x, y) => y[1] - x[1])[0];
    return best && all > 0 ? { model: best[0], share: best[1] / all } : null;
  }, [models.data]);
  const topRun = (runs.data ?? [])[0];
  // The run's token price on the basis the server ranks by and Work shows (measured, else Claude Code's, else booked).
  const topRunUSD = topRun ? runUSD(topRun) : null;
  const outsideShare = a && a.list > 0 ? a.rows.filter((x) => x.kind !== "covey").reduce((s, x) => s + (x.list ?? 0), 0) / a.list : null;
  const checks = health.data?.checks.filter((c) => c.status !== "info") ?? [];

  // The meter's span in the window: the seats' weekly coverage (one merged meter per seat since #214).
  const cov = (cells.data?.meter ?? []).filter((m) => m.window_kind === "seven_day");
  const first = cov.length ? Math.min(...cov.map((m) => Date.parse(m.baseline_at))) : null;
  const last = cov.length ? Math.max(...cov.map((m) => Date.parse(m.last_at))) : null;
  const since = cells.data?.since ? Date.parse(cells.data.since) : null;

  if (toDetails) return <Navigate to={`/costs/details?${r.params.toString()}`} replace />;
  return (
    <div className="cost-home">
      <div className="flex items-baseline gap-3 mb-1 flex-wrap">
        <h1 className="text-[22px]">{t("costs.home.title")}</h1>
        <CostTabs />
      </div>
      {/* The answer is account-wide: the bar says Who is not applied here instead of offering a filter that does nothing. */}
      <CostFilterBar r={r} rangeError={rangeError} bucketSwitch={false} leading={<WhoSummary names={[]} locked />} trailing={<ShareButton />} />
      {refused && <p className="muted text-sm" data-range-refused>{t("costs.range.refused")}</p>}
      {!refused && cells.isLoading && <p className="muted text-sm">{t("costs.home.loading")}</p>}
      {!refused && cells.data && !a && <p className="muted text-sm" data-home-empty>{t("costs.home.noMeter")}</p>}
      {!refused && a && head && dent && (
        <>
          <section className="card cost-home-hero" aria-label={t("costs.answer.title")}>
            {heroSupport.state === "locked" && <div className="mb-2" data-home-hero-lock><BasisLockChip support={heroSupport} basis={basis} /></div>}
            <Hero a={a} />
            <Trust rep={health.data} seats={cov.length} />
            {since != null && <Coverage h={head} since={since} until={windowEnd} first={first} last={last} seats={cov.length} basis={basis} real={a.real} />}
          </section>
          {basis === "billed" && <BilledLine figure={billedFigure} loading={billed.isLoading} />}
          <div className="cost-home-split">
            <CostAnswer a={a} h={head} filtered={false} hero={false} basis={basis}
              marks={marks.multiple.length + marks.outside.length + marks.tokenPrice.length > 0 && <>
                <IncompleteMark checks={marks.multiple} part="multiple" />
                <IncompleteMark checks={marks.outside} part="outside" />
                <IncompleteMark checks={marks.tokenPrice} part="tokenPrice" />
              </>}
              drawer={<SubscriptionDentRow d={dent} filtered={false} h={head} />} />
            <nav className="cost-home-doors" aria-label={t("costs.home.doorsLabel")}>
              <Door testid="details" to={`/costs/details${q}`} label={t("costs.home.doorDetails")} sub={t("costs.home.doorDetailsSub")}
                value={detailsSupport.state === "none" ? <NoFigure reason={detailsSupport.reason} />
                  : basis === "capacity" ? (a.real != null ? `≈ ${fmtUSD(a.real)}` : <NoFigure reason="noData" />)
                  : fmtUSD(a.list)} />
              <Door testid="outside" to={`/costs/outside${q}`} label={t("costs.home.tabOutside")}
                value={outsideShare != null ? `${fmtNumber(outsideShare * 100, 0)} %` : "–"} sub={t("costs.home.doorOutsideSub")} />
              <Door testid="model" to={`/costs/models${q}`} label={t("costs.home.doorModel")}
                value={topModel?.model ?? "–"} sub={topModel ? t("costs.home.doorModelSub", { pct: fmtNumber(topModel.share * 100, 0) }) : undefined} />
              <Door testid="run" to={`/costs/work${q}`} label={t("costs.home.doorRun")} lock={{ support: runSupport, basis }}
                value={topRunUSD != null ? fmtUSD(topRunUSD) : "–"} sub={topRun?.title ?? t("costs.home.noRuns")} />
              <Door testid="health" to={`/costs/health${q}`} label={t("costs.home.tabHealth")}
                value={checks.length && windowIsNow ? `${checks.filter((c) => c.status === "pass").length} / ${checks.length}` : "–"}
                sub={windowIsNow ? t("costs.home.doorHealthSub") : t("costs.home.healthNow")} />
            </nav>
          </div>
        </>
      )}
    </div>
  );
}
