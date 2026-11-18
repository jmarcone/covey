import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import type { CostCells } from "../api";
import { fmtCount, fmtNumber, fmtUSD, fmtUSDAxis } from "../format";
import { BarChart, Chip, SOURCE_COLOR, Swatch, type Bar } from "./StackedCostChart";
import { agentsPerPeriod, outsideDefaultOn, outsidePerPeriod, outsideTotals, type OutsideTotals } from "./outsideFigures";

/* The Claude Code sessions outside covey on the overview (#120, part C): a
 * consumer next to the agents, never a fifth money source and never folded
 * into the agents' figures unannounced. On by default in the org-wide view,
 * off once the page is narrowed to agents; either way the user can switch
 * it. Shown apart, it is its own chart; combined, one bar carries both and
 * the total says it includes the outside sessions — the agents' own tiles
 * above stay theirs alone. The detailed view (OutsideCosts) splits it per
 * model, origin and token type. */

export const OUTSIDE_COLOR = "var(--cost-outside)";

const pct = (v: number) => `${fmtNumber(v * 100, 1)} %`;

/** A figure with its lines, in the style of the credential tiles. */
export function FigureTile({ label, value, lines, color, hatched, test }: {
  label: string; value: string; lines?: (string | false | undefined)[]; color?: string; hatched?: boolean; test?: string;
}) {
  return (
    <div role="group" aria-label={label} data-figure={test}
      style={{ padding: "10px 14px", borderRadius: 8, border: "1px solid var(--border)", minWidth: 170, flex: "1 1 170px" }}>
      <div className="muted text-xs" style={{ display: "flex", alignItems: "center", gap: 6 }}>
        {color && <Swatch color={color} hatched={hatched} />}
        {label}
      </div>
      <div data-value style={{ fontSize: 20, fontWeight: 600, marginTop: 2, fontVariantNumeric: "tabular-nums" }}>{value}</div>
      {(lines ?? []).filter(Boolean).map((l, i) => <div key={i} className="muted text-xs" style={{ marginTop: 2 }}>{l}</div>)}
    </div>
  );
}

/** The lines under the outside figures: their real share of the meter (F3),
 *  readings they did not report, work after the last reading, unpriced tokens. */
export function outsideLines(t: ReturnType<typeof useTranslation>["t"], o: OutsideTotals): string[] {
  const lines: string[] = [];
  if (!o.linked) lines.push(t("costs.outside.noMeterLinked"));
  else if (o.share != null) lines.push(t("costs.outside.realShare", { usd: fmtUSD(o.real), pct: pct(o.share) }));
  else lines.push(t("costs.outside.noShare"));
  if (o.notReported > 0) lines.push(t("costs.outside.notReported", { count: o.notReported, usd: fmtUSD(o.notReportedUSD) }));
  if (o.unmeteredList > 0) lines.push(t("costs.outside.unmetered", { usd: fmtUSD(o.unmeteredList) }));
  if (o.unpricedTokens > 0) lines.push(t("costs.outside.unpriced", { n: fmtCount(o.unpricedTokens) }));
  return lines;
}

export function OutsideOverview({ data, selected, days, bucket, fmtPeriod }: {
  data: CostCells | undefined; selected: Set<string>; days: number; bucket: string; fmtPeriod: (p: string) => string;
}) {
  const { t } = useTranslation();
  const filtered = selected.size > 0;
  // null: the default for the view; a click is the user's choice until the
  // view changes between org-wide and narrowed.
  const [choice, setChoice] = useState<boolean | null>(null);
  useEffect(() => setChoice(null), [filtered]);
  const on = choice ?? outsideDefaultOn(filtered);
  const [mode, setMode] = useState<"apart" | "combined">("apart");
  const [asTable, setAsTable] = useState(false);

  const totals = useMemo(() => outsideTotals(data?.external), [data]);
  const outside = useMemo(() => outsidePerPeriod(data?.external), [data]);
  const agents = useMemo(() => agentsPerPeriod(data, selected), [data, selected]);
  if (!totals) return null;

  const agentsList = [...agents.values()].reduce((a, e) => a + e.usd, 0);
  const outsideList = totals.list ?? 0;
  const combined = mode === "combined";
  const keys = [...new Set([...outside.keys(), ...(combined ? agents.keys() : [])])].sort((a, b) => a - b);
  const periods = keys.map((k) => outside.get(k)?.period ?? agents.get(k)!.period);
  const seriesLabel = t("costs.outside.series");
  const groups: Bar[][] = keys.map((k) => {
    const o = outside.get(k)?.usd ?? 0;
    if (!combined) return [{ key: "outside", label: seriesLabel, color: OUTSIDE_COLOR, segs: [{ key: "outside", label: seriesLabel, color: OUTSIDE_COLOR, value: o }] }];
    return [{ key: "combined", label: t("costs.outside.combinedTotal"), color: OUTSIDE_COLOR, segs: [
      { key: "agents", label: t("costs.outside.agentsPart"), color: SOURCE_COLOR.recomputed, value: agents.get(k)?.usd ?? 0 },
      { key: "outside", label: seriesLabel, color: OUTSIDE_COLOR, value: o },
    ] }];
  });
  const detail = `/costs/outside?days=${days}&bucket=${bucket}`;

  return (
    <section className="card mb-4" aria-label={seriesLabel} data-outside>
      <div className="flex items-center justify-between flex-wrap gap-2 mb-1">
        <div style={{ fontWeight: 600, display: "inline-flex", alignItems: "center", gap: 6 }}>
          <Swatch color={OUTSIDE_COLOR} /> {seriesLabel}
        </div>
        <Link to={detail} className="text-sm" data-outside-detail>{t("costs.outside.details")}</Link>
      </div>
      <p className="muted text-xs mb-3" style={{ maxWidth: 760 }}>{t("costs.outside.hint")}</p>
      <div className="flex flex-wrap items-center gap-2 mb-3">
        <Chip on={on} color={OUTSIDE_COLOR} onClick={() => setChoice(!on)}>{t("costs.outside.show")}</Chip>
        {on && <>
          <span className="muted text-xs" style={{ marginLeft: 8 }}>{t("costs.outside.mode")}</span>
          <Chip on={!combined} onClick={() => setMode("apart")}>{t("costs.outside.modeApart")}</Chip>
          <Chip on={combined} onClick={() => setMode("combined")}>{t("costs.outside.modeCombined")}</Chip>
          <span style={{ flex: 1 }} />
          <Chip on={asTable} onClick={() => setAsTable((v) => !v)}>{t("costs.fig.showAsTable")}</Chip>
        </>}
      </div>
      {!on && <p className="muted text-xs mb-0" data-outside-off>{filtered ? t("costs.outside.offFiltered") : t("costs.outside.off")}</p>}
      {on && <>
        <div className="flex flex-wrap gap-3 mb-3">
          <FigureTile test="outside" label={t("costs.outside.tile")} color={OUTSIDE_COLOR}
            value={totals.list != null ? fmtUSD(totals.list) : "–"}
            lines={[totals.list != null ? t("costs.outside.tileSub", { calls: fmtCount(totals.calls) }) : t("costs.outside.noPrice"), ...outsideLines(t, totals)]} />
          {combined && (
            <FigureTile test="combined" label={t("costs.outside.combinedTotal")}
              value={fmtUSD(agentsList + outsideList)}
              lines={[t("costs.outside.combinedSub", { outside: fmtUSD(outsideList), agents: fmtUSD(agentsList) })]} />
          )}
        </div>
        {periods.length === 0 ? (
          <div className="muted text-sm">{t("costs.empty")}</div>
        ) : (
          <BarChart title={combined ? t("costs.outside.chartCombined") : t("costs.outside.chartApart")} periods={periods} groups={groups}
            fmtPeriod={fmtPeriod} fmtValue={fmtUSD} fmtAxis={fmtUSDAxis} asTable={asTable} height={200} />
        )}
        {combined && <p className="muted text-xs mt-1 mb-0" data-combined-note>{t("costs.outside.combinedNote")}</p>}
      </>}
    </section>
  );
}
