import { useId, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import { fmtCount, fmtNumber, fmtUSD, fmtUSDFixed } from "../format";
import type { Headline } from "./costFigures";
import type { AnswerRow, AnswerTable } from "./costAnswer";
import { OrgWide, outsideMoney, priceTableLine } from "./StackedCostChart";

/* The answer table (#195): the first thing on the Costs page. One row per
 * consumer of the subscription, what it comes to at token prices and its
 * share of the real cost, a total, the multiple, and one sentence. Every
 * figure is the headline's and the dent's (costAnswer.ts); the explanations
 * that used to sit beside them are in the drawer, which is not rendered while
 * it is closed — so no figure stands on the page twice. */

const times = (v: number) => `${fmtNumber(v, 1)}×`;
const pts = (v: number) => fmtNumber(v, v < 10 ? 1 : 0);

function listCell(t: TFunction, r: AnswerRow): string {
  if (r.before) return t("costs.incl.beforeRange");
  // Never $0 where no call had a price: say so, with the tokens it leaves out.
  if (r.list == null) return r.unpricedTokens > 0 ? t("costs.incl.noListTokens", { n: fmtCount(r.unpricedTokens) }) : t("costs.incl.noList");
  const usd = r.unpricedTokens > 0 ? t("costs.incl.listUnpriced", { usd: fmtUSD(r.list), n: fmtCount(r.unpricedTokens) }) : fmtUSD(r.list);
  return r.recomputed ? t("costs.lead.part", { usd }) : usd;
}

function label(t: TFunction, r: AnswerRow): { name: string; from: string } {
  switch (r.kind) {
    case "covey": return { name: t("costs.answer.rowCovey"), from: t("costs.answer.fromCovey") };
    case "operator": return { name: t("costs.answer.rowOperator"), from: t("costs.answer.fromOperator") };
    case "project": return { name: t("costs.answer.rowProject", { source: r.source }), from: t("costs.answer.fromProject") };
    default: return { name: t("costs.answer.rowRest"), from: t("costs.answer.fromRest") };
  }
}

const cell = { padding: "8px 12px 8px 0", verticalAlign: "top" } as const;
const num = { ...cell, textAlign: "right", fontVariantNumeric: "tabular-nums", whiteSpace: "nowrap" } as const;

export function CostAnswer({ a, h, filtered, drawer }: {
  a: AnswerTable; h: Headline; filtered: boolean;
  /** The explanations, rendered only while the drawer is open. */
  drawer?: ReactNode;
}) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const drawerId = useId().replace(/[^a-zA-Z0-9_-]/g, "");
  const hidden = a.hidden.length > 0;
  const rateText = a.rate != null ? fmtUSDFixed(a.rate, 4) : null;
  // The total's own line: where its points and their price are known, the
  // arithmetic; across a reset, the points of each week.
  const totalSub = a.real == null ? null
    : hidden ? t("costs.answer.totalSubShown", { pts: a.points != null ? pts(a.points) : "–" })
    : a.points == null || rateText == null ? null
    : a.weeks ? t("costs.answer.totalSubWeeks", { weeks: a.weeks.map(pts).join(" + "), rate: rateText })
    : t("costs.answer.totalSub", { pts: pts(a.points), rate: rateText });
  const noReal = t(h.why === "usage" ? "costs.answer.noRealUsage" : "costs.answer.noReal");
  const sentenceArgs = { list: fmtUSD(a.list), real: a.real != null ? fmtUSD(a.real) : "", pts: a.points != null ? pts(a.points) : "" };
  const sentence = a.real == null ? t("costs.answer.sentenceNoReal", { list: fmtUSD(a.list), why: noReal })
    : a.multiple != null ? t(a.points != null ? "costs.answer.sentence" : "costs.answer.sentenceNoPoints", { ...sentenceArgs, x: times(a.multiple) })
    : t(a.points != null && !hidden ? "costs.answer.sentenceNoMultiple" : "costs.answer.sentenceNoMultipleNoPoints", sentenceArgs);
  const noMultiple = hidden ? t("costs.answer.noMultipleHidden")
    : a.why === "movement" ? t("costs.dent.head.noMultipleMovement", { pts: pts(a.points ?? 0) })
    : a.why === "unpriced" ? t("costs.dent.head.noMultipleUnpriced", { n: pts(h.unpricedPoints) })
    : a.why === "list" ? t("costs.answer.noMultipleList")
    : "–";
  // Work the table's multiple does not divide, and the per-use sessions on no meter: one small line each.
  const after = !hidden && h.after >= 0.005 ? t("costs.answer.after", { usd: fmtUSD(h.after) }) : null;
  const before = !hidden && h.before >= 0.005 ? t("costs.answer.before", { usd: fmtUSD(h.before) }) : null;
  const perUse = h.perUse && h.perUse.calls > 0 ? t("costs.answer.perUse", { list: outsideMoney(t, h.perUse, "lead") }) : null;
  const check = hidden ? undefined : priceTableLine(t, h.list, h.table);
  return (
    <section className="card mb-4" aria-label={t("costs.answer.title")} data-answer>
      <div style={{ fontWeight: 600, marginBottom: 8 }}>
        {t("costs.answer.title")}
        {filtered && <OrgWide />}
      </div>
      <div style={{ overflowX: "auto" }}>
        <table style={{ width: "100%", borderCollapse: "collapse", fontSize: 13, minWidth: 520 }} data-answer-table>
          <thead>
            <tr className="muted" style={{ textAlign: "left" }}>
              <th scope="col" style={{ ...cell, fontWeight: 500 }}>{t("costs.answer.colPart")}</th>
              <th scope="col" style={{ ...cell, fontWeight: 500 }}>{t("costs.answer.colFrom")}</th>
              <th scope="col" style={{ ...num, fontWeight: 500 }}>{t("costs.answer.colList")}</th>
              <th scope="col" style={{ ...num, fontWeight: 500, paddingRight: 0 }}>{t("costs.answer.colReal")}</th>
            </tr>
          </thead>
          <tbody>
            {a.rows.map((r) => {
              const l = label(t, r);
              return (
                <tr key={r.key} style={{ borderTop: "1px solid var(--border)" }} data-answer-row={r.key}>
                  <th scope="row" style={{ ...cell, textAlign: "left", fontWeight: 600 }}>{l.name}</th>
                  <td className="muted" style={cell}>{l.from}</td>
                  <td style={num} data-answer-list>{r.kind === "rest" ? "" : listCell(t, r)}</td>
                  <td style={{ ...num, paddingRight: 0 }} data-answer-real>{r.real != null ? `≈ ${fmtUSD(r.real)}` : "–"}</td>
                </tr>
              );
            })}
            <tr style={{ borderTop: "2px solid var(--border)" }} data-answer-total>
              <th scope="row" style={{ ...cell, textAlign: "left", fontWeight: 600 }}>{t("costs.answer.rowTotal")}</th>
              <td className="muted" style={cell}>{totalSub}</td>
              <td style={{ ...num, fontWeight: 600 }} data-answer-list>{fmtUSD(a.list)}</td>
              <td style={{ ...num, fontWeight: 600, paddingRight: 0 }} data-answer-real>{a.real != null ? `≈ ${fmtUSD(a.real)}` : "–"}</td>
            </tr>
            <tr style={{ borderTop: "1px solid var(--border)" }} data-answer-difference>
              <th scope="row" style={{ ...cell, textAlign: "left", fontWeight: 600 }}>{t("costs.answer.rowDifference")}</th>
              <td className="muted" style={cell}>{t("costs.answer.fromDifference")}</td>
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
      {before && <p className="muted text-xs mb-1" data-answer-before>{before}</p>}
      {after && <p className="muted text-xs mb-1" data-answer-after>{after}</p>}
      {perUse && <p className="muted text-xs mb-1" data-answer-per-use>{perUse}</p>}
      {check && <p className="muted text-xs mb-1" data-price-check>{check}</p>}
      {drawer != null && (
        <div className="mt-2">
          <button type="button" className="text-sm" aria-expanded={open} aria-controls={drawerId} data-answer-how
            onClick={() => setOpen((v) => !v)}
            style={{ background: "none", border: "none", padding: 0, color: "var(--text-accent)", cursor: "pointer", textDecoration: "underline" }}>
            {t("costs.answer.how")}
          </button>
          {open && <div id={drawerId} role="region" aria-label={t("costs.answer.how")} className="mt-3" data-answer-drawer>{drawer}</div>}
        </div>
      )}
    </section>
  );
}
