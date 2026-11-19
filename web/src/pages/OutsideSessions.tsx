import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import type { CostCells } from "../api";
import { fmtCount, fmtNumber, fmtUSD, fmtUSDAxis } from "../format";
import { BarChart, Chip, SOURCE_COLOR, Swatch, type Bar } from "./StackedCostChart";
import {
  UNSPLIT_REASONS,
  agentsPerPeriod,
  outsideDefaultOn,
  outsidePerPeriod,
  outsideTotals,
  type OutsideTotals,
  type Unattributed,
  type Unsplit,
} from "./outsideFigures";

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

type T = ReturnType<typeof useTranslation>["t"];

/** A count for a t() call: count picks the plural, n is the number as shown. */
export const counted = (v: number) => ({ count: v, n: fmtNumber(v, 0) });

/** The readings not split, one line per reason, each with its real cost
 *  split to nobody. */
export function unsplitLines(t: T, u: Unsplit): string[] {
  return UNSPLIT_REASONS.filter((r) => u[r].steps > 0)
    .map((r) => t(`costs.outside.unsplit_${r}`, { ...counted(u[r].steps), usd: fmtUSD(u[r].usd) }));
}

/** The readings the server books to nobody whole (F3c: a chunk with no list,
 *  so they moved with no reported call on either side) — counted as the
 *  server marks them, never from their reason. */
export function noCallLines(t: T, n: Unattributed["noCall"]): string[] {
  return n.steps > 0 ? [t("costs.outside.noCall", { ...counted(n.steps), usd: fmtUSD(n.usd) })] : [];
}

/** Where a share is shown, one line on the unattributed part: its amount and
 *  what it is (#126's sentence), or that none was measured and what it would
 *  be. */
export const unattributedLine = (t: T, usd: number) =>
  usd > 0 ? t("costs.dent.unattributedSentence", { usd: fmtUSD(usd) }) : t("costs.outside.unattributedNone");

/** The unattributed part's two halves in #126's words: beyond the rate (at
 *  least) and moved with no call (≈). withBeyond: name the part beyond the
 *  rate even at $0 (a split seat or period, where it is a figure). */
export function unattributedHalves(t: T, h: { beyond: number; noCall: number }, withBeyond: boolean): string[] {
  return [
    ...(withBeyond || h.beyond > 0 ? [t("costs.dent.unattributedBeyond", { usd: fmtUSD(h.beyond) })] : []),
    ...(h.noCall > 0 ? [t("costs.dent.unattributedNoCall", { usd: fmtUSD(h.noCall) })] : []),
  ];
}

/** The chunks with use not in their list price: their parts are approximate. */
export const incompleteLines = (t: T, n: number) => (n > 0 ? [t("costs.dent.incompleteNote", { n: fmtNumber(n, 0) })] : []);

/** The list price over a range: the figure, or why there is none — no call
 *  at all, or calls none of which had a list price (never $0). */
export const listValue = (list: number | null) => (list != null ? fmtUSD(list) : "–");
export const listLine = (t: T, list: number | null, calls: number) =>
  calls === 0 ? t("costs.outside.noCalls") : list == null ? t("costs.outside.noPrice") : t("costs.outside.tileSub", counted(calls));

/** The lines under the outside figures: their real share of the meter (F3c:
 *  ≈, of the dent) and the unattributed part beside it with its halves, the
 *  incomplete chunks, readings booked to nobody whole, readings not split,
 *  work after the last reading, reported costs, unpriced tokens. */
export function outsideLines(t: T, o: OutsideTotals): string[] {
  const lines: string[] = [];
  const m = o.meter;
  if (!o.linked) lines.push(t("costs.outside.noMeterLinked"));
  else {
    if (m.share != null) lines.push(t("costs.outside.realShare", { usd: fmtUSD(m.real), pct: pct(m.share) }));
    else lines.push(t("costs.outside.noShare"));
    if (m.share != null || m.unattributed > 0) lines.push(unattributedLine(t, m.unattributed));
    if (m.unattributed > 0) lines.push(unattributedHalves(t, m, false).join(" · "));
    lines.push(...incompleteLines(t, m.incomplete));
  }
  lines.push(...noCallLines(t, o.noCall), ...unsplitLines(t, o.unsplit));
  if (o.unpricedSteps > 0) lines.push(t("costs.outside.unpricedReadings", counted(o.unpricedSteps)));
  if (o.unmeteredList > 0) lines.push(t("costs.outside.unmetered", { usd: fmtUSD(o.unmeteredList) }));
  if (o.reported != null && o.reportedCalls > 0) lines.push(t("costs.outside.reportedLine", { ...counted(o.reportedCalls), usd: fmtUSD(o.reported) }));
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
  const combined = mode === "combined";
  const keys = [...new Set([...outside.keys(), ...(combined ? agents.keys() : [])])].sort((a, b) => a - b);
  const periods = keys.map((k) => outside.get(k)?.period ?? agents.get(k)!.period);
  const seriesLabel = t("costs.outside.series");
  // A period whose outside calls all had no list price has no outside bar
  // (never one of $0) and says so.
  const notes = new Map<number, string>();
  const groups: Bar[][] = keys.map((k, i) => {
    const e = outside.get(k);
    const o = e?.usd ?? null;
    if (e && o == null) notes.set(i, combined ? t("costs.outside.chartNoPriceCombined") : t("costs.outside.noPrice"));
    if (!combined) return o == null ? [] : [{ key: "outside", label: seriesLabel, color: OUTSIDE_COLOR, segs: [{ key: "outside", label: seriesLabel, color: OUTSIDE_COLOR, value: o }] }];
    return [{ key: "combined", label: t("costs.outside.combinedTotal"), color: OUTSIDE_COLOR, segs: [
      { key: "agents", label: t(filtered ? "costs.outside.agentsPartSelected" : "costs.outside.agentsPart"), color: SOURCE_COLOR.recomputed, value: agents.get(k)?.usd ?? 0 },
      ...(o == null ? [] : [{ key: "outside", label: seriesLabel, color: OUTSIDE_COLOR, value: o }]),
    ] }];
  });
  // Narrowed to agents, the agents' part is the selected agents', and says so.
  const sel = filtered ? "Selected" : "";
  const combinedSub = totals.list != null
    ? t(`costs.outside.combinedSub${sel}`, { outside: fmtUSD(totals.list), agents: fmtUSD(agentsList) })
    : t(`costs.outside.${totals.calls === 0 ? "combinedSubNoCalls" : "combinedSubNoPrice"}${sel}`, { agents: fmtUSD(agentsList) });
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
            value={listValue(totals.list)}
            lines={[listLine(t, totals.list, totals.calls), ...outsideLines(t, totals)]} />
          {combined && (
            <FigureTile test="combined" label={t("costs.outside.combinedTotal")}
              value={fmtUSD(agentsList + (totals.list ?? 0))}
              lines={[combinedSub]} />
          )}
        </div>
        {periods.length === 0 ? (
          <div className="muted text-sm">{t("costs.empty")}</div>
        ) : (
          <BarChart title={combined ? t("costs.outside.chartCombined") : t("costs.outside.chartApart")} periods={periods} groups={groups}
            fmtPeriod={fmtPeriod} fmtValue={fmtUSD} fmtAxis={fmtUSDAxis} asTable={asTable} height={200} notes={notes}
            notesLabel={t("costs.outside.colNote")} />
        )}
        {totals.unpricedTokens > 0 && <p className="muted text-xs mt-1 mb-0" data-outside-unpriced-note>{t("costs.outside.chartUnpriced")}</p>}
        {combined && <p className="muted text-xs mt-1 mb-0" data-combined-note>{t("costs.outside.combinedNote")}</p>}
      </>}
    </section>
  );
}
