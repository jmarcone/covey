import { useMemo } from "react";
import { useQueries, useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { api, ApiError, type IndicatorReport, type RunCost } from "../api";
import { fmtBucket, fmtCount, fmtUSD } from "../format";
import { PriceList } from "../components/PriceList";
import { isRangeCode, type RangeCode } from "./costRange";
import { CostFilterBar, ShareButton, WhoSummary, useCostRange } from "./costRangeState";
import { CostTabs } from "./CostTabs";
import { asSet, runUSD, useParamState } from "./costView";

/* Work (cost home views): which runs burned the money, and what came of it. The runs are ranked by their token price
 * (Claude Code's own price where it reported one, else covey's list price); a run that ended without a result (failed,
 * cancelled) is waste, said in its own tile; a run with no action changed nothing outside itself. The runs follow the
 * page's Who: with agents selected, each agent's own runs are fetched (a cut of the org's top runs would lose them).
 * The server ranks at most 500 runs per fetch: where one returns that many, the page says the figures are over those. */

const NONE: ReadonlySet<string> = new Set();
const LIMIT = 500;
const WASTE = new Set(["failed", "cancelled", "killed"]);
export { runUSD };

export default function CostWork() {
  const { t } = useTranslation();
  const r = useCostRange();
  const [agents, setAgents] = useParamState<ReadonlySet<string>>("agents", NONE, asSet.parse, asSet.show);
  const scopes = agents.size > 0 ? [...agents].sort() : ["org"];
  const fetches = useQueries({ queries: scopes.map((s) => ({
    queryKey: ["cost", "runs", s, r.wq, LIMIT],
    queryFn: () => api<RunCost[] | null>(s === "org" ? `/cost/runs?${r.wq}&limit=${LIMIT}` : `/agents/${s}/cost/runs?${r.wq}&limit=${LIMIT}`),
    enabled: !r.badRange,
  })) });
  const runs = { error: fetches.find((f) => f.error)?.error ?? null, isLoading: fetches.some((f) => f.isLoading),
    data: fetches.every((f) => f.data !== undefined) ? fetches.flatMap((f) => f.data ?? []) : undefined };
  // The price list is over whole quick ranges (its trend compares the span with the one before it).
  const indicators = useQuery({ queryKey: ["cost", "indicators", "org", r.days], queryFn: () => api<IndicatorReport>(`/cost/indicators?days=${r.days}`),
    enabled: !r.custom && !r.badRange, retry: false });
  const apiCode: RangeCode | "unknown" | null = runs.error instanceof ApiError && runs.error.status === 400
    ? (isRangeCode(runs.error.code) ? runs.error.code : "unknown") : null;
  const refused = r.badRange || apiCode != null;
  const failed = !refused && runs.error != null;
  const all = runs.data ?? [];
  const shown = useMemo(() => all.slice().sort((a, b) => (runUSD(b) ?? -1) - (runUSD(a) ?? -1)), [all]);
  const total = shown.reduce((a, x) => a + (runUSD(x) ?? 0), 0);
  const wasted = shown.filter((x) => WASTE.has(x.state));
  const waste = wasted.reduce((a, x) => a + (runUSD(x) ?? 0), 0);
  const idle = shown.filter((x) => x.actions === 0).length;
  const capped = fetches.some((f) => (f.data?.length ?? 0) >= LIMIT);
  const names = agents.size > 0 ? [...new Set(all.map((x) => x.slug))] : [];

  return (
    <div className="cost-view">
      <div className="flex items-baseline gap-3 mb-1 flex-wrap">
        <h1 className="text-[22px]">{t("costs.work.title")}</h1>
        <CostTabs />
      </div>
      <CostFilterBar r={r} rangeError={r.range.error ?? apiCode} bucketSwitch={false} trailing={<ShareButton />}
        leading={<WhoSummary names={names} count={agents.size} onClear={() => setAgents(new Set())} />} />
      {refused && <p className="muted text-sm" data-range-refused>{t("costs.range.refused")}</p>}
      {failed && <p className="muted text-sm" data-work-error role="alert">{t("costs.home.loadError")}</p>}
      {!refused && runs.isLoading && <p className="muted text-sm">{t("costs.home.loading")}</p>}
      {!refused && !failed && runs.data && shown.length === 0 && <p className="muted text-sm" data-work-empty>{t("costs.work.empty")}</p>}
      {!refused && !failed && shown.length > 0 && (
        <>
          <div className="cost-view-tiles" data-work-tiles>
            <div className="cost-view-tile" data-work-top><span className="cost-home-label">{t("costs.work.top")}</span>
              <span className="cost-view-tile-value">{runUSD(shown[0]) != null ? fmtUSD(runUSD(shown[0])!) : "–"}</span>
              <Link className="cost-home-sub cost-view-ellipsis" to={`/agents/${shown[0].agent_id}`} title={shown[0].title}>{shown[0].title}</Link></div>
            <div className="cost-view-tile" data-work-waste><span className="cost-home-label">{t("costs.work.waste")}</span>
              <span className="cost-view-tile-value">{fmtUSD(waste)}</span>
              <span className="cost-home-sub">{t("costs.work.wasteSub", { count: wasted.length, pct: total > 0 ? Math.round((waste / total) * 100) : 0 })}</span></div>
            <div className="cost-view-tile" data-work-idle><span className="cost-home-label">{t("costs.work.idle")}</span>
              <span className="cost-view-tile-value">{fmtCount(idle)}</span>
              <span className="cost-home-sub">{t("costs.work.idleSub", { count: shown.length })}</span></div>
            <div className="cost-view-tile"><span className="cost-home-label">{t("costs.work.total")}</span>
              <span className="cost-view-tile-value">{fmtUSD(total)}</span>
              <span className="cost-home-sub">{t(capped ? "costs.work.totalCapped" : "costs.work.totalSub", { count: shown.length })}</span></div>
          </div>
          <div className="cost-view-split">
            <div className="card cost-view-table">
              <table data-work-table>
                <thead>
                  <tr>
                    <th scope="col">{t("costs.runs.task")}</th>
                    <th scope="col">{t("costs.runs.agent")}</th>
                    <th scope="col">{t("costs.work.colOutcome")}</th>
                    <th scope="col" className="r">{t("costs.work.colPrice")}</th>
                    <th scope="col" className="r">{t("costs.runs.cached")}</th>
                    <th scope="col" className="r">{t("costs.runs.actions")}</th>
                  </tr>
                </thead>
                <tbody>
                  {shown.slice(0, 100).map((x) => (
                    <tr key={x.task_id} data-work-row={x.task_id}>
                      <th scope="row">
                        <div className="cost-view-ellipsis" title={x.title}>{x.title}</div>
                        <span className="muted text-xs">{x.origin} · {fmtBucket(x.started_at, "hour")}</span>
                      </th>
                      <td><Link to={`/agents/${x.agent_id}`}>{x.slug}</Link></td>
                      <td><span className={`cost-view-outcome ${WASTE.has(x.state) ? "waste" : x.state}`} data-work-outcome={x.state}>{t(`costs.work.state.${x.state}`, { defaultValue: x.state })}</span></td>
                      <td className="r">{runUSD(x) != null ? fmtUSD(runUSD(x)!) : <span className="muted">{t("costs.runs.noLines")}</span>}</td>
                      <td className="r muted">{fmtCount(x.cache_read_tokens + x.cache_creation_tokens)}</td>
                      <td className="r">{x.actions === 0 ? <span className="cost-view-idle" title={t("costs.runs.idleHint")}>{t("costs.runs.idle")}</span> : x.actions}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
              {shown.length > 100 && <p className="muted text-xs mt-2">{t("costs.work.more", { count: shown.length - 100 })}</p>}
            </div>
            <div className="card">
              <div style={{ fontWeight: 600, marginBottom: 4 }}>{t("costs.indicators.title")}</div>
              <p className="muted text-xs mb-3">{t("costs.indicators.hint")}</p>
              {r.custom ? <p className="muted text-xs">{t("costs.range.indicatorsCustom")}</p>
                : indicators.error instanceof ApiError && indicators.error.status === 403 ? <p className="muted text-xs">{t("costs.indicators.restricted")}</p>
                : <PriceList rep={indicators.data} />}
            </div>
          </div>
        </>
      )}
    </div>
  );
}
