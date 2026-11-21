import { useId, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import { fmtCount, fmtNumber, fmtUSD, fmtUSDFixed } from "../format";
import type { Headline } from "./costFigures";
import type { AnswerRow, AnswerTable } from "./costAnswerModel";
import { OrgWide, outsideMoney, priceTableLine } from "./StackedCostChart";
import { usageList } from "./CostBasis";

/* The answer table (#195): the first thing on the Costs page. One row per
 * consumer of the subscription, what it comes to at token prices and its
 * share of the real cost, a total, the multiple, and one sentence. Every
 * figure is the headline's and the dent's (costAnswerModel.ts). Under the
 * table stand only the sentence and the line about work after the last
 * reading (and, while a chip is off, which sources are hidden); every other
 * explanation is in the drawer, which is not rendered while it is closed, so
 * no figure stands on the page twice (the human on #195/#204: "noise"). */

const times = (v: number) => `${fmtNumber(v, 1)}×`;
/** Meter points: whole numbers without a decimal (27, not 27.0); a fraction keeps one place. */
const pts = (v: number) => fmtNumber(v, Number.isInteger(v) ? 0 : 1);

/** The footnote mark for a figure recomputed from tokens; its legend is in the drawer. */
const MARK = "*";

function listCell(t: TFunction, r: AnswerRow): ReactNode {
  if (r.before) return t("costs.incl.beforeRange");
  // Never $0 where no call had a price: say so, with the tokens it leaves out.
  if (r.list == null) return r.unpricedTokens > 0 ? t("costs.incl.noListTokens", { n: fmtCount(r.unpricedTokens) }) : t("costs.incl.noList");
  const usd = r.unpricedTokens > 0 ? t("costs.incl.listUnpriced", { usd: fmtUSD(r.list), n: fmtCount(r.unpricedTokens) }) : fmtUSD(r.list);
  return r.recomputed
    ? <>{usd}<sup className="muted" title={t("costs.answer.recomputedMark")} aria-hidden="true" data-answer-recomputed>{MARK}</sup>
      <span className="sr-only"> ({t("costs.answer.recomputedMark")})</span></>
    : usd;
}

/** The rows for the rest of the real cost: they hold no work of their own, so no token price. */
const remainder = (r: AnswerRow) => r.kind === "unsplit" || r.kind === "unattributed" || r.kind === "outsideRest";

function label(t: TFunction, r: AnswerRow): { name: string; from: string } {
  switch (r.kind) {
    case "covey": return { name: t("costs.answer.rowCovey"), from: t("costs.answer.fromCovey") };
    case "operator": return { name: t("costs.answer.rowOperator"), from: t("costs.answer.fromOperator") };
    case "project": return { name: t("costs.answer.rowProject", { source: r.source }), from: t("costs.answer.fromProject") };
    case "small": return { name: t("costs.answer.rowSmall", { count: r.folded.length }), from: r.folded.join(", ") };
    case "unsplit": return { name: t("costs.answer.rowUnsplit"), from: t("costs.answer.fromUnsplit") };
    case "unattributed": return { name: t("costs.answer.rowUnattributed"), from: t("costs.answer.fromUnattributed") };
    default: return r.real != null && r.real < 0 ? { name: t("costs.answer.rowOutsideOver"), from: t("costs.answer.fromOutsideOver") }
      : { name: t("costs.answer.rowOutsideRest"), from: t("costs.answer.fromOutsideRest") };
  }
}

function realCell(t: TFunction, r: AnswerRow): ReactNode {
  if (r.pending) return <><span className="muted" data-answer-pending>{t("costs.answer.pending")}</span>
    <div className="muted text-xs" style={{ fontWeight: 400, whiteSpace: "normal" }}>{t("costs.answer.pendingWhy")}</div></>;
  return <>
    {r.real != null ? `≈ ${fmtUSD(r.real)}` : "–"}
    {r.notSplit && <div className="muted text-xs" style={{ fontWeight: 400, whiteSpace: "normal" }}>{t("costs.answer.notSplit")}</div>}
    {r.partlySplit && <div className="muted text-xs" style={{ fontWeight: 400, whiteSpace: "normal" }} data-answer-partly>{t("costs.answer.partlyNotSplit")}</div>}
  </>;
}

const cell = { padding: "8px 12px 8px 0", verticalAlign: "top" } as const;
const num = { ...cell, textAlign: "right", fontVariantNumeric: "tabular-nums", whiteSpace: "nowrap" } as const;

export function CostAnswer({ a, h, filtered, drawer, elsewhere: elsewhereUsd }: {
  a: AnswerTable; h: Headline; filtered: boolean;
  /** The explanations, rendered only while the drawer is open. */
  drawer?: ReactNode;
  /** covey's own work on credentials no row of the page covers, at token prices (the page leaves out those the
   *  pay-per-token row shows); defaults to the headline's coveyElsewhere. */
  elsewhere?: number;
}) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const drawerId = useId().replace(/[^a-zA-Z0-9_-]/g, "");
  const hidden = a.hidden.length > 0;
  const rateText = a.rate != null ? fmtUSDFixed(a.rate, 4) : null;
  // The total's own line: the points and their price; across a reset, the points of each week.
  const totalSub = a.real == null ? null
    : hidden ? t("costs.answer.totalSubShown", { pts: a.points != null ? pts(a.points) : "–" })
    : a.points == null || rateText == null ? null
    : a.weeks ? t("costs.answer.totalSubWeeks", { weeks: a.weeks.map(pts).join(" + "), pts: pts(a.points), rate: rateText })
    : t("costs.answer.totalSub", { pts: pts(a.points), rate: rateText });
  const noReal = t(h.why === "usage" ? "costs.answer.noRealUsage" : "costs.answer.noReal");
  const sentenceArgs = { list: fmtUSD(a.list), metered: fmtUSD(a.metered), real: a.real != null ? fmtUSD(a.real) : "", pts: a.points != null ? pts(a.points) : "" };
  const sentence = a.real == null ? t("costs.answer.sentenceNoReal", { list: fmtUSD(a.list), why: noReal })
    : a.multiple != null ? t(a.points != null ? "costs.answer.sentence" : "costs.answer.sentenceNoPoints", { ...sentenceArgs, x: times(a.multiple) })
    : t(a.points != null && !hidden ? "costs.answer.sentenceNoMultiple" : "costs.answer.sentenceNoMultipleNoPoints", sentenceArgs);
  const noMultiple = hidden ? t("costs.answer.noMultipleHidden")
    : a.why === "movement" ? t("costs.dent.head.noMultipleMovement", { pts: pts(a.points ?? 0) })
    : a.why === "unpriced" ? t("costs.dent.head.noMultipleUnpriced", { n: pts(h.unpricedPoints) })
    : a.why === "list" ? t("costs.answer.noMultipleList")
    : "–";
  // The one line under the sentence: work after the last reading (its real share follows at the next one).
  const after = !hidden && h.after >= 0.005 ? t("costs.answer.after", { usd: fmtUSD(h.after) }) : null;
  // Everything else that qualifies the figures is in the drawer, as short lines above the method.
  const unpriced = !hidden && h.unpriced >= 0.005 ? t("costs.answer.unpriced", { usd: fmtUSD(h.unpriced) }) : null;
  // Time on the usage basis (0151): in the list, not in the multiple or the real cost; its money is the usage charges.
  const usage = !hidden && h.usageSeats.length > 0 ? t("costs.dent.head.usageOut", { seats: h.usageSeats.join(", "), n: pts(h.usagePoints), list: fmtUSD(h.usage) }) : null;
  const usageCharges = !hidden && h.usageSeats.length > 0
    ? (h.usageCharges ? t("costs.dent.head.usageCharges", { list: usageList(t, h.usageCharges) }) : t("costs.dent.head.usageNoCharge")) : null;
  const before = !hidden && h.before >= 0.005 ? t("costs.answer.before", { usd: fmtUSD(h.before) }) : null;
  const perUse = a.perUse && a.perUse.calls > 0 ? t("costs.answer.perUse", { list: outsideMoney(t, a.perUse, "lead") }) : null;
  // covey's own work on credentials no row covers (an API key the pay-per-token row does not show, a seat with no weekly reading).
  const elsewhereV = elsewhereUsd ?? h.coveyElsewhere;
  const elsewhere = elsewhereV >= 0.005 ? t("costs.answer.elsewhere", { usd: fmtUSD(elsewhereV) }) : null;
  // spec/30 R4: the parts of a chunk with unpriced outside calls or unbooked covey work are approximate.
  const incomplete = a.incompleteChunks > 0 && a.rows.some((r) => !remainder(r) && r.real != null)
    ? t("costs.dent.incompleteNote", { n: fmtNumber(a.incompleteChunks, 0) }) : null;
  const check = hidden ? undefined : priceTableLine(t, h.list, h.table);
  const recomputed = a.rows.some((r) => r.recomputed && r.list != null && !r.before);
  // The multiple divides only the work within the meter's readings: where that is less than the total, the label says so.
  const diffFrom = a.multiple != null && a.list - a.metered >= 0.005
    ? t("costs.answer.fromDifferenceOf", { metered: fmtUSD(a.metered) }) : t("costs.answer.fromDifference");
  const notes: [string, ReactNode][] = ([
    ["incomplete", incomplete], ["before", before], ["unpriced", unpriced], ["usage", usage], ["usage-charges", usageCharges],
    ["per-use", perUse], ["elsewhere", elsewhere], ["price-check", check],
  ] as [string, ReactNode][]).filter(([, v]) => v != null && v !== "");
  return (
    <section className="card mb-4 cost-answer" aria-label={t("costs.answer.title")} data-answer>
      <div style={{ fontWeight: 600, marginBottom: 8 }}>
        {t("costs.answer.title")}
        {filtered && <OrgWide />}
      </div>
      <div style={{ overflowX: "auto" }}>
      <table style={{ width: "100%", borderCollapse: "collapse", fontSize: 13 }} data-answer-table>
        <thead>
          <tr className="muted" style={{ textAlign: "left" }}>
            <th scope="col" style={{ ...cell, fontWeight: 500 }}>{t("costs.answer.colPart")}</th>
            <th scope="col" className="cost-answer-from" style={{ ...cell, fontWeight: 500 }}>{t("costs.answer.colFrom")}</th>
            <th scope="col" style={{ ...num, fontWeight: 500 }}>{t("costs.answer.colList")}</th>
            <th scope="col" style={{ ...num, fontWeight: 500, paddingRight: 0 }}>{t("costs.answer.colReal")}</th>
          </tr>
        </thead>
        <tbody>
          {a.rows.map((r) => {
            const l = label(t, r);
            return (
              <tr key={r.key} style={{ borderTop: "1px solid var(--border)" }} data-answer-row={r.key}>
                <th scope="row" style={{ ...cell, textAlign: "left", fontWeight: 600 }}>
                  {l.name}
                  {/* On a phone the "where it comes from" column is gone; its text stands under the part. */}
                  <div className="muted text-xs cost-answer-from-inline" style={{ fontWeight: 400 }}>{l.from}</div>
                </th>
                <td className="muted cost-answer-from" style={cell}>{l.from}</td>
                <td style={r.list == null || r.unpricedTokens > 0 || r.before ? { ...num, whiteSpace: "normal" } : num} data-answer-list>
                  {remainder(r) ? "" : listCell(t, r)}
                </td>
                <td style={{ ...num, paddingRight: 0 }} data-answer-real>{realCell(t, r)}</td>
              </tr>
            );
          })}
          <tr style={{ borderTop: "2px solid var(--border)" }} data-answer-total>
            <th scope="row" style={{ ...cell, textAlign: "left", fontWeight: 600 }}>
              {t("costs.answer.rowTotal")}
              {totalSub && <div className="muted text-xs cost-answer-from-inline" style={{ fontWeight: 400 }}>{totalSub}</div>}
            </th>
            <td className="muted cost-answer-from" style={cell}>{totalSub}</td>
            <td style={{ ...num, fontWeight: 600 }} data-answer-list>{fmtUSD(a.list)}</td>
            <td style={{ ...num, fontWeight: 600, paddingRight: 0 }} data-answer-real>{a.real != null ? `≈ ${fmtUSD(a.real)}` : "–"}</td>
          </tr>
          <tr style={{ borderTop: "1px solid var(--border)" }} data-answer-difference>
            <th scope="row" style={{ ...cell, textAlign: "left", fontWeight: 600 }}>
              {t("costs.answer.rowDifference")}
              <div className="muted text-xs cost-answer-from-inline" style={{ fontWeight: 400 }}>{diffFrom}</div>
            </th>
            <td className="muted cost-answer-from" style={cell} data-answer-multiple-from>{diffFrom}</td>
            <td style={num} />
            <td style={{ ...num, paddingRight: 0 }} data-answer-multiple>
              {a.multiple != null ? (
                <>
                  <span style={{ fontWeight: 600 }}>≈ {times(a.multiple)}</span>
                  {a.floor != null && <div className="muted text-xs" style={{ fontWeight: 400 }}>{t("costs.answer.floor", { floor: times(a.floor) })}</div>}
                </>
              ) : <span className="muted">–</span>}
            </td>
          </tr>
        </tbody>
      </table>
      </div>
      <p className="text-sm mt-3 mb-1" style={{ fontWeight: 600 }} data-answer-sentence>{sentence}</p>
      {a.multiple == null && a.real != null && <p className="muted text-xs mb-1" data-answer-no-multiple>{noMultiple}</p>}
      {hidden && <p className="muted text-xs mb-1" data-answer-hidden>{t("costs.answer.hidden", { sources: a.hidden.join(", "), count: a.hidden.length })}</p>}
      {after && <p className="muted text-xs mb-1" data-answer-after>{after}</p>}
      {recomputed && <p className="muted text-xs mb-1" data-answer-note="recomputed">{`${MARK} ${t("costs.answer.recomputedMark")}`}</p>}
      {(drawer != null || notes.length > 0) && (
        <div className="mt-2">
          <button type="button" className="text-sm" aria-expanded={open} aria-controls={open ? drawerId : undefined} data-answer-how
            onClick={() => setOpen((v) => !v)}
            style={{ background: "none", border: "none", padding: 0, color: "var(--text-accent)", cursor: "pointer", textDecoration: "underline" }}>
            {t("costs.answer.how")}
          </button>
          {open && (
            <div id={drawerId} role="region" aria-label={t("costs.answer.how")} className="mt-3" data-answer-drawer>
              {notes.map(([k, v]) => <p key={k} className={k === "usage" ? "text-sm mb-1" : "muted text-xs mb-1"} data-answer-note={k}>{v}</p>)}
              {drawer}
            </div>
          )}
        </div>
      )}
    </section>
  );
}
