import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { api, ApiError, type ExternalModelUsage, type ModelUsageReport } from "../api";
import { fmtCount, fmtNumber, fmtUSD } from "../format";
import { isRangeCode, type RangeCode } from "./costRange";
import { CostFilterBar, ShareButton, WhoSummary, useCostRange } from "./costRangeState";
import { CostTabs } from "./CostTabs";
import { asSet, useParamState } from "./costView";

/* Models (cost home views): which model the money went to, and what one run of it costs. One row per model, the
 * agents' work and the sessions outside covey apart (they are no agents), each at its lead token price: Claude Code's
 * own price where it reported one, covey's list price where it did not (marked). The agents' rows follow the page's
 * Who; the outside sessions are shown only for everyone, since a selection of agents does not include them. */

const NONE: ReadonlySet<string> = new Set();

type Agentish = { id: string; label: string; runs: number; usd: number };
type Row = {
  model: string;
  who: "agents" | "outside";
  runs: number; // agents: runs; outside: calls
  input: number; output: number; cacheRead: number;
  usd: number;
  recomputed: boolean;
  unpricedTokens: number;
  agents: Agentish[];
};

export function modelRows(models: ModelUsageReport | undefined, outside: ExternalModelUsage | undefined, agents: ReadonlySet<string>): Row[] {
  const by = new Map<string, Row>();
  const row = (model: string, who: Row["who"]) => {
    const k = `${who}:${model}`;
    let r = by.get(k);
    if (!r) by.set(k, (r = { model, who, runs: 0, input: 0, output: 0, cacheRead: 0, usd: 0, recomputed: false, unpricedTokens: 0, agents: [] }));
    return r;
  };
  for (const m of models?.rows ?? []) {
    if (agents.size > 0 && !agents.has(m.agent_id)) continue;
    const r = row(m.model, "agents");
    r.runs += m.runs;
    r.input += m.input_tokens + m.cache_write_5m_tokens + m.cache_write_1h_tokens;
    r.output += m.output_tokens;
    r.cacheRead += m.cache_read_tokens;
    r.usd += m.engine_usd;
    // One line per agent: an agent that ran the model on two credential kinds comes in two report rows.
    const a = r.agents.find((x) => x.id === m.agent_id);
    if (a) { a.runs += m.runs; a.usd += m.engine_usd; } else r.agents.push({ id: m.agent_id, label: m.agent_name || m.agent_slug, runs: m.runs, usd: m.engine_usd });
  }
  if (agents.size === 0) {
    for (const c of outside?.cells ?? []) {
      const r = row(c.model, "outside");
      r.runs += c.calls;
      r.input += (c.tokens.input ?? 0) + (c.tokens.cache_write_5m ?? 0) + (c.tokens.cache_write_1h ?? 0);
      r.output += c.tokens.output ?? 0;
      r.cacheRead += c.tokens.cache_read ?? 0;
      // The lead price: Claude Code's where it reported one for every call of the cell, else covey's list price over all
      // of them (marked), so the calls without a reported price are never dropped; no price at all is never $0.
      const whole = c.reported_usd != null && c.reported_calls >= c.calls;
      const lead = whole ? c.reported_usd : (c.list_usd ?? c.reported_usd);
      if (lead != null) r.usd += lead;
      if (!whole && c.list_usd != null) r.recomputed = true;
      r.unpricedTokens += c.unpriced_tokens;
    }
  }
  for (const r of by.values()) r.agents.sort((a, b) => b.usd - a.usd);
  return [...by.values()].sort((a, b) => b.usd - a.usd);
}

/** The model that took most of the money, over agents and outside sessions together (as the cost home's door says it). */
export function topModel(rows: Row[]): { model: string; usd: number } | undefined {
  const by = new Map<string, number>();
  for (const r of rows) by.set(r.model, (by.get(r.model) ?? 0) + r.usd);
  let top: { model: string; usd: number } | undefined;
  for (const [model, usd] of by) if (!top || usd > top.usd) top = { model, usd };
  return top;
}

export default function CostModels() {
  const { t } = useTranslation();
  const r = useCostRange();
  const [agents, setAgents] = useParamState<ReadonlySet<string>>("agents", NONE, asSet.parse, asSet.show);
  const models = useQuery({ queryKey: ["cost", "models", r.wq], queryFn: () => api<ModelUsageReport>(`/cost/models?${r.wq}`), enabled: !r.badRange });
  const outside = useQuery({ queryKey: ["cost", "external-usage", r.wq, "day"], queryFn: () => api<ExternalModelUsage>(`/cost/external-usage?${r.wq}&bucket=day`),
    enabled: !r.badRange });
  const apiCode: RangeCode | "unknown" | null = models.error instanceof ApiError && models.error.status === 400
    ? (isRangeCode(models.error.code) ? models.error.code : "unknown") : null;
  const refused = r.badRange || apiCode != null;
  const failed = !refused && apiCode == null && (models.isError || outside.isError);
  const rows = useMemo(() => modelRows(models.data, outside.data, agents), [models.data, outside.data, agents]);
  const total = rows.reduce((a, x) => a + x.usd, 0);
  const agentRows = rows.filter((x) => x.who === "agents");
  const top = topModel(rows)!;
  // The cheapest per run among models with enough runs to say so (5 or more).
  const cheapest = agentRows.filter((x) => x.runs >= 5).sort((a, b) => a.usd / a.runs - b.usd / b.runs)[0];
  const input = rows.reduce((a, x) => a + x.input + x.cacheRead, 0), cacheRead = rows.reduce((a, x) => a + x.cacheRead, 0);
  const names = (models.data?.rows ?? []).filter((m) => agents.has(m.agent_id)).map((m) => m.agent_name || m.agent_slug);

  return (
    <div className="cost-view">
      <div className="flex items-baseline gap-3 mb-1 flex-wrap">
        <h1 className="text-[22px]">{t("costs.models.title")}</h1>
        <CostTabs />
      </div>
      <CostFilterBar r={r} rangeError={r.range.error ?? apiCode} bucketSwitch={false} trailing={<ShareButton />}
        leading={<WhoSummary names={[...new Set(names)]} count={agents.size} onClear={() => setAgents(new Set())} />} />
      {refused && <p className="muted text-sm" data-range-refused>{t("costs.range.refused")}</p>}
      {failed && <p className="muted text-sm" data-models-error role="alert">{t("costs.home.loadError")}</p>}
      {!refused && models.isLoading && <p className="muted text-sm">{t("costs.home.loading")}</p>}
      {!refused && !failed && models.data && rows.length === 0 && <p className="muted text-sm" data-models-empty>{t("costs.models.empty")}</p>}
      {!refused && !failed && rows.length > 0 && (
        <>
          <div className="cost-view-tiles" data-models-tiles>
            <div className="cost-view-tile"><span className="cost-home-label">{t("costs.models.topModel")}</span>
              <span className="cost-view-tile-value">{top.model}</span>
              <span className="cost-home-sub">{total > 0 ? t("costs.models.share", { pct: fmtNumber((top.usd / total) * 100, 0), usd: fmtUSD(top.usd) }) : fmtUSD(0)}</span></div>
            {cheapest && <div className="cost-view-tile"><span className="cost-home-label">{t("costs.models.cheapest")}</span>
              <span className="cost-view-tile-value">{cheapest.model}</span>
              <span className="cost-home-sub">{t("costs.models.perRun", { usd: fmtUSD(cheapest.usd / cheapest.runs) })}</span></div>}
            <div className="cost-view-tile"><span className="cost-home-label">{t("costs.models.cache")}</span>
              <span className="cost-view-tile-value">{input > 0 ? `${fmtNumber((cacheRead / input) * 100, 0)} %` : "–"}</span>
              <span className="cost-home-sub">{t("costs.models.cacheSub")}</span></div>
            <div className="cost-view-tile"><span className="cost-home-label">{t("costs.models.total")}</span>
              <span className="cost-view-tile-value">{fmtUSD(total)}</span>
              <span className="cost-home-sub">{t("costs.models.totalSub")}</span></div>
          </div>
          <div className="card cost-view-table">
            <table data-models-table>
              <thead>
                <tr>
                  <th scope="col">{t("costs.models.colModel")}</th>
                  <th scope="col" className="r">{t("costs.models.colRuns")}</th>
                  <th scope="col" className="r">{t("costs.models.colInput")}</th>
                  <th scope="col" className="r">{t("costs.models.colOutput")}</th>
                  <th scope="col" className="r">{t("costs.models.colPrice")}</th>
                  <th scope="col" className="r">{t("costs.models.colShare")}</th>
                  <th scope="col" className="r">{t("costs.models.colPerRun")}</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((x) => (
                  <tr key={`${x.who}:${x.model}`} data-model-row={`${x.who}:${x.model}`}>
                    <th scope="row">
                      {x.agents.length > 0 ? (
                        <details>
                          <summary><b>{x.model}</b></summary>
                          <ul className="muted text-xs cost-view-agents">
                            {x.agents.map((a) => <li key={a.id}>{a.label}: {fmtUSD(a.usd)} · {t("costs.models.runs", { count: a.runs })}</li>)}
                          </ul>
                        </details>
                      ) : <b>{x.model}</b>}
                      <span className={`cost-view-who ${x.who}`}>{t(x.who === "agents" ? "costs.models.whoAgents" : "costs.models.whoOutside")}</span>
                    </th>
                    <td className="r">{fmtCount(x.runs)}</td>
                    <td className="r">{fmtCount(x.input + x.cacheRead)}</td>
                    <td className="r">{fmtCount(x.output)}</td>
                    <td className="r">{x.usd > 0 || x.unpricedTokens === 0 ? <>{fmtUSD(x.usd)}{x.recomputed && <sup className="muted" title={t("costs.answer.recomputedMark")}>*</sup>}</>
                      : <span className="muted">{t("costs.incl.noListTokens", { n: fmtCount(x.unpricedTokens) })}</span>}</td>
                    <td className="r">{total > 0 ? `${fmtNumber((x.usd / total) * 100, 1)} %` : "–"}</td>
                    <td className="r">{x.who === "agents" && x.runs > 0 ? fmtUSD(x.usd / x.runs) : <span className="muted">–</span>}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            <p className="muted text-xs mt-2">{t("costs.models.note")}</p>
          </div>
        </>
      )}
    </div>
  );
}
