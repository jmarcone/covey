import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { api, ApiError, type IndicatorReport, type RunCost } from "../api";
import { exact, fmtBucket, fmtCount, fmtUSD } from "../format";
import { PriceList } from "../components/PriceList";
import { CostInsights } from "./CostInsights";
import {
  AgentFilter,
  CostExplorer,
  SOURCE_COLOR,
  Swatch,
  TokenExplorer,
  TYPE_COLOR,
  cellTotals,
  useBilled,
  useCostCells,
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

// --- KPI tiles ---------------------------------------------------------------

function Kpi({ label, value, sub, color }: { label: string; value: string; sub?: string; color?: string }) {
  return (
    <div className="card" style={{ flex: 1, minWidth: 150 }}>
      <div className="muted text-xs" style={{ display: "flex", alignItems: "center", gap: 6 }}>
        {color && <Swatch color={color} />}
        {label}
      </div>
      <div style={{ fontSize: 24, fontWeight: 600, marginTop: 2 }}>{value}</div>
      {sub && <div className="muted text-xs" style={{ marginTop: 2 }}>{sub}</div>}
    </div>
  );
}

// --- Segmented switches ------------------------------------------------------

function Seg<T extends string | number>({
  value,
  options,
  onChange,
}: {
  value: T;
  options: { v: T; label: string; disabled?: boolean; title?: string }[];
  onChange: (v: T) => void;
}) {
  return (
    /* One option per agent grows past any width with a large fleet: the
       control scrolls inside itself rather than widening the page (#463). */
    <div style={{ display: "inline-flex", gap: 2, maxWidth: "100%", overflowX: "auto", scrollbarWidth: "thin", background: "var(--surface-1)", border: "0.5px solid var(--border)", borderRadius: 8, padding: 2 }}>
      {options.map((o) => {
        const active = o.v === value;
        return (
          <button
            key={String(o.v)}
            onClick={() => onChange(o.v)}
            aria-pressed={active}
            disabled={o.disabled}
            title={o.title}
            style={{
              border: "none",
              flexShrink: 0,
              whiteSpace: "nowrap",
              borderRadius: 6,
              padding: "5px 12px",
              fontSize: 13,
              cursor: o.disabled ? "not-allowed" : "pointer",
              opacity: o.disabled ? 0.45 : 1,
              background: active ? "var(--surface-2)" : "transparent",
              color: active ? "var(--text-primary)" : "var(--text-secondary)",
              fontWeight: active ? 600 : 400,
              boxShadow: active ? "0 1px 3px var(--shadow-xs)" : "none",
            }}
          >
            {o.label}
          </button>
        );
      })}
    </div>
  );
}

// --- Page --------------------------------------------------------------------

/** Hourly buckets only up to this many days: 90 days by the hour are 2,160
 *  periods, more than a chart (or a table) can show. */
const HOUR_MAX_DAYS = 7;

const RANGES: { v: number; bucket: string }[] = [
  { v: 1, bucket: "hour" },
  { v: 7, bucket: "day" },
  { v: 30, bucket: "day" },
  { v: 90, bucket: "week" },
];

/** The token types side by side. They stand here because the dollar figure
 *  alone hides the key observation: the weight lies almost entirely on the
 *  cached input side, not on what the agents write. Measured on covey.work:
 *  112M cache read against 3.2M output in 24 hours — the prompt is read anew
 *  in every turn. Whoever misses that optimizes the output length and
 *  wonders why nothing happens. The tokens are the lines' own, over every
 *  model, so they are the calls the dollars above are made of. */
function TokenMix({ tokens }: { tokens: Record<string, number> }) {
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

export default function Costs() {
  const { t } = useTranslation();

  // 24 hours, hourly: the window in which the cost per hour can actually be read.
  const [days, setDays] = useState(1);
  const [bucket, setBucket] = useState("hour");
  const [metric, setMetric] = useState<Metric>("cost");
  // The agent filter replaces the scope tabs: none selected = the whole
  // organisation, any combination for the charts and the KPIs; exactly one
  // also narrows the cards below to that agent.
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const scope = selected.size === 1 ? [...selected][0] : "org";
  const cells = useCostCells(days, bucket);
  const billed = useBilled(days, bucket);

  // The most expensive runs of the period — org-wide or for the chosen agent.
  const runs = useQuery({
    queryKey: ["cost", "runs", scope, days],
    queryFn: () =>
      api<RunCost[] | null>(
        scope === "org" ? `/cost/runs?days=${days}&limit=25` : `/agents/${scope}/cost/runs?days=${days}&limit=25`,
      ),
    refetchInterval: 30000,
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
  });
  // A role that may not see something should not get it as an empty box:
  // empty means "there are no metrics", and that is a different statement
  // than "you do not see them".
  const indicatorsForbidden = indicators.error instanceof ApiError && indicators.error.status === 403;

  const setRange = (v: number) => {
    setDays(v);
    const r = RANGES.find((x) => x.v === v);
    if (r) setBucket(r.bucket);
  };

  // KPIs over the same lines as the charts, narrowed by the same filter.
  const totals = useMemo(() => cellTotals(cells.data, selected), [cells.data, selected]);
  const tok = totals.tokens;
  const input = (tok.input ?? 0) + (tok.cache_read ?? 0) + (tok.cache_write_5m ?? 0) + (tok.cache_write_1h ?? 0);
  // The hit rate: what was read from the cache, of all input. A cache write
  // is a miss that pays extra to be a hit next time, not a hit.
  const cached = tok.cache_read ?? 0;
  const output = tok.output ?? 0;

  const agentList = cells.data?.agents ?? [];
  const scopeName =
    selected.size === 0
      ? t("costs.wholeOrg")
      : selected.size === 1
        ? agentList.find((a) => a.id === scope)?.label ?? ""
        : t("costs.filter.nSelected", { count: selected.size });
  const fmtPeriod = (p: string) => fmtBucket(p, bucket);

  return (
    <div>
      <div className="flex items-baseline gap-3 mb-1">
        <h1 className="text-[22px]">{t("costs.title")}</h1>
        <span className="muted">{scopeName}</span>
      </div>
      <p className="muted text-sm mb-4" style={{ maxWidth: 620 }}>{t("costs.subtitle")}</p>

      {/* Controls */}
      <div className="flex flex-wrap items-center gap-3 mb-4">
        <div style={{ flex: 1 }} />
        <Seg
          value={days}
          onChange={setRange}
          options={RANGES.map((r) => ({ v: r.v, label: r.v === 1 ? t("costs.range24h") : t("costs.rangeDays", { count: r.v }) }))}
        />
        <Seg
          value={bucket}
          onChange={setBucket}
          options={[
            // 90 days by the hour are 2,160 periods: hours only up to a week.
            { v: "hour", label: t("costs.bHour"), disabled: days > HOUR_MAX_DAYS, title: days > HOUR_MAX_DAYS ? t("costs.hourTooLong", { days: HOUR_MAX_DAYS }) : undefined },
            { v: "day", label: t("costs.bDay") },
            { v: "week", label: t("costs.bWeek") },
            { v: "month", label: t("costs.bMonth") },
          ]}
        />
      </div>

      <div className="mb-4">
        <AgentFilter agents={agentList} selected={selected} onChange={setSelected} />
      </div>

      {/* KPIs: over every credential, dollars and tokens of the same lines. */}
      <div className="flex flex-wrap gap-3 mb-4">
        <Kpi label={t("costs.fig.reported")} value={fmtUSD(totals.reported)} sub={t("costs.kpiAllCredentials")} color={SOURCE_COLOR.reported} />
        <Kpi label={t("costs.fig.recomputed")} value={fmtUSD(totals.recomputed)} sub={t("costs.kpiAllCredentials")} color={SOURCE_COLOR.recomputed} />
        <Kpi
          label={t("costs.inputTokens")}
          value={fmtCount(input)}
          sub={t("costs.cachedShare", { pct: input > 0 ? Math.round((cached / input) * 100) : 0 })}
        />
        <Kpi label={t("costs.outputTokens")} value={fmtCount(output)} sub={exact(output)} />
        <Kpi label={t("costs.totalTokens")} value={fmtCount(input + output)} />
      </div>

      {/* Chart */}
      <div className="card mb-4">
        <div className="flex items-center justify-between mb-3 flex-wrap gap-2">
          <div style={{ fontWeight: 600 }}>{metric === "cost" ? t("costs.chartCost") : t("costs.chartTokens")}</div>
          <Seg
            value={metric}
            onChange={setMetric}
            options={[{ v: "cost", label: t("costs.mCost") }, { v: "tokens", label: t("costs.mTokens") }]}
          />
        </div>
        {metric === "cost" ? (
          <CostExplorer data={cells.data} billed={billed.data} selected={selected} bucket={bucket} fmtPeriod={fmtPeriod} />
        ) : (
          <TokenExplorer data={cells.data} selected={selected} fmtPeriod={fmtPeriod} />
        )}
      </div>

      {/* Price list + token types + priciest runs: they hold for every scope */}
      <div className="grid gap-4 mb-4" style={{ gridTemplateColumns: "repeat(auto-fit, minmax(min(320px, 100%), 1fr))" }}>
        <div className="card">
          <div style={{ fontWeight: 600, marginBottom: 4 }}>{t("costs.indicators.title")}</div>
          <p className="muted text-xs mb-3">{t("costs.indicators.hint")}</p>
          {indicatorsForbidden ? (
            <p className="muted text-xs">{t("costs.indicators.restricted")}</p>
          ) : (
            <PriceList rep={indicators.data} />
          )}
        </div>
        <div className="card">
          <div style={{ fontWeight: 600, marginBottom: 4 }}>{t("costs.mix.title")}</div>
          <p className="muted text-xs mb-3">{t("costs.mix.hint")}</p>
          <TokenMix tokens={tok} />
        </div>
        <div className="card" style={{ gridColumn: "span 1" }}>
          <div style={{ fontWeight: 600, marginBottom: 4 }}>{t("costs.runs.title")}</div>
          <p className="muted text-xs mb-3">{t("costs.runs.hint")}</p>
          <ExpensiveRuns runs={runs.data ?? []} />
        </div>
      </div>

      {/* Cost insights: per model as Claude Code reported it, and the
          subscription meter per credential against the list price. Org-wide only. */}
      {selected.size === 0 && <CostInsights days={days} />}
    </div>
  );
}
