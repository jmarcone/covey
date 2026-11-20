import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import { fmtNumber, fmtUSD } from "../format";
import { SPLIT_COLOR, SPLIT_HATCH } from "./costSources";
import type { DentPart, FloorMultiple, Headline, PayPerTokenHeadline, SubscriptionDent } from "./costFigures";
import { OrgWide, Swatch, outsideMoney, priceTableLine } from "./StackedCostChart";
import { isOperatorSource } from "./outsideFigures";
import { usageList } from "./CostBasis";

/* The headline of the Costs page once Claude Code sessions outside covey
 * report on a subscription meter (#120): the whole dent in the meters over
 * the range, split between covey's agents, the sessions that drive them and
 * the movement neither explains, and list $ per real $ for covey, the
 * outside sessions and both together.
 *
 * Every figure is the server's split (costFigures.subscriptionDent over
 * external.meter): per chunk of readings, by the chunk's list-price shares
 * (F3c) — a single reading's whole percents say little about its own
 * movement. A chunk holds at least 5 points but for the last one and one
 * closed early (a reading not split, a new week, a change of plan price). The meter is account-wide: a machine that
 * does not report (another one on the same subscription) moves it too. Per
 * plan price the server takes the lowest rate of points per list $ seen over
 * a stretch of adjacent chunks of 10 points (k); movement beyond it is
 * unattributed and a lower
 * bound, and a chunk that moved with no call at all (the meter's lag, or
 * another machine) is unattributed whole, an estimate — both booked to
 * nobody, never to covey or a source. covey's and the outside sessions'
 * parts are estimates (≈); the multiples are lower bounds. Movement in a
 * reading the outside sessions had not reported, or whose outside calls all
 * had no price, is split to nobody and stands as its own part. Points
 * without a plan price are not in the dent and are counted beside it. The
 * dent ignores the agent filter and says so. */

const times = (v: number) => `${fmtNumber(v, 1)}×`;

/** A seat's rate (F3c's k) in words: 1 point ≈ $X of list price, at the
 *  earlier plan price it was measured at when that is not the latest one,
 *  or that there is no rate yet. seat: its name, null when it is the only
 *  one. */
export function rateLine(t: TFunction, seat: string | null, k: number | null, olderPlan: number | null): string {
  if (k == null || !(k > 0)) return t(seat != null ? "costs.dent.rateUnknownSeat" : "costs.dent.rateUnknown", { seat });
  const usd = fmtUSD(1 / k);
  if (olderPlan != null) {
    return t(seat != null ? "costs.dent.rateSeatOlderPlan" : "costs.dent.rateOlderPlan", { seat, usd, plan: fmtUSD(olderPlan) });
  }
  return t(seat != null ? "costs.dent.rateSeat" : "costs.dent.rate", { seat, usd });
}
const pctOf = (v: number | null) => (v != null ? `${fmtNumber(v * 100, 0)} %` : "–");
const pts = (v: number) => fmtNumber(v, v < 10 ? 1 : 0);
const count = (v: number) => fmtNumber(v, 0);
/** A lower bound: "≥ 1.1×". */
const atLeast = (m: FloorMultiple | null) => (m ? `≥ ${times(m.low)}` : "–");

function Part({ label, value, sub, color, dashed, hatch, test, title }: {
  label: string; value: string; sub: string; color?: string; dashed?: string; hatch?: number; test: string; title?: string;
}) {
  return (
    // The hover is also the card's accessible description, which the keyboard and touch screens reach where a title is not.
    <div role="group" aria-label={label} data-dent={test} title={title} aria-description={title?.split("\n").join("; ")}
      style={{ padding: "10px 14px", borderRadius: 8, border: "1px solid var(--border)", minWidth: 170, flex: "1 1 170px" }}>
      <div className="muted text-xs" style={{ display: "flex", alignItems: "center", gap: 6 }}>
        {color && <Swatch color={color} hatched dashed={dashed} hatch={hatch} />}
        {label}
      </div>
      <div style={{ fontSize: 20, fontWeight: 600, marginTop: 2, fontVariantNumeric: "tabular-nums" }}>{value}</div>
      <div className="muted text-xs" style={{ marginTop: 2 }}>{sub}</div>
    </div>
  );
}

/** The headline's sentence (#120, the human's core): all the work at token
 *  prices against the one real cost of the subscription, and one multiple —
 *  the estimate and its floor. */
function HeadlineSentence({ h }: { h: Headline }) {
  const { t } = useTranslation();
  // The outside part at its lead price (#120), in the covey operator and the
  // other projects (#163): reported where a call carried it, else the list
  // price, marked once after the parts; or "no list price" with the tokens
  // that have none — never $0 where there is no figure (M3).
  const group = (of: (s: Headline["sources"][number]) => boolean) => {
    const ss = h.sources.filter(of);
    const priced = ss.filter((s) => s.list != null);
    return { calls: ss.reduce((a, s) => a + s.calls, 0), list: null, reported: null, reportedCalls: 0,
      unpricedTokens: ss.reduce((a, s) => a + s.unpricedTokens, 0),
      lead: priced.length > 0 ? priced.reduce((a, s) => a + s.list!, 0) : null,
      leadRecomputed: ss.reduce((a, s) => a + s.recomputed, 0), leadUnpricedTokens: ss.reduce((a, s) => a + s.unpricedTokens, 0) };
  };
  const op = group((s) => isOperatorSource(s.source)), pr = group((s) => !isOperatorSource(s.source));
  const lines = [t("costs.incl.lineCovey", { usd: fmtUSD(h.coveyList) })];
  if (op.calls > 0) lines.push(t("costs.incl.lineOperator", { usd: outsideMoney(t, op, "lead", false) }));
  if (pr.calls > 0) lines.push(t("costs.incl.lineProjects", { usd: outsideMoney(t, pr, "lead", false) }));
  const marked = op.leadRecomputed + pr.leadRecomputed > 0;
  const parts = { list: fmtUSD(h.list), split: lines.join(" + ") + (marked ? ` (${t("costs.lead.recomputedMark")})` : "") };
  const sentence = h.real == null ? t(h.why === "usage" ? "costs.dent.head.noRealUsage" : "costs.dent.head.noReal", parts)
    : h.multiple == null ? t("costs.dent.head.sentenceNoMultiple", { ...parts, real: fmtUSD(h.real) })
    : t(h.points != null ? "costs.dent.head.sentence" : "costs.dent.head.sentenceNoPoints", {
      ...parts, real: fmtUSD(h.real), pts: pts(h.points ?? 0), metered: fmtUSD(h.metered), x: times(h.multiple) });
  // covey's price table over the same range: a line only where it differs (PRICE_CHECK).
  const check = priceTableLine(t, h.list, h.table);
  // What the multiple does not divide, by where it lies: before the meters'
  // first reading in the range (no meter data, never a real cost), after
  // their last one (pending), in readings with no plan price.
  const outsideParts = [
    h.before >= 0.005 ? t("costs.dent.head.partBefore", { usd: fmtUSD(h.before) }) : null,
    h.after >= 0.005 ? t("costs.dent.head.partAfter", { usd: fmtUSD(h.after) }) : null,
    h.unpriced >= 0.005 ? t("costs.dent.head.partUnpriced", { usd: fmtUSD(h.unpriced) }) : null,
  ].filter((x): x is string => x != null);
  return (
    <div className="mb-3" data-head>
      <p className="text-sm mb-1" style={{ fontWeight: 600 }} data-head-sentence>{sentence}</p>
      {h.floor != null && (
        <p className="text-sm mb-1" data-head-floor>
          {t("costs.dent.head.floor", { floor: times(h.floor), metered: fmtUSD(h.floorList), high: fmtUSD(h.real! + h.slack) })}
        </p>
      )}
      {h.why === "movement" && <p className="muted text-xs mb-1" data-head-no-multiple>{t("costs.dent.head.noMultipleMovement", { pts: pts(h.points ?? 0) })}</p>}
      {h.why === "unpriced" && h.real != null && <p className="muted text-xs mb-1" data-head-no-multiple>{t("costs.dent.head.noMultipleUnpriced", { n: pts(h.unpricedPoints) })}</p>}
      {/* A seat's time on the usage basis (0151): out of the real cost and of
          the list the multiple divides, said with the seats; their money, the
          usage charges, beside it per currency in its own — never added to
          the real cost, across currencies, or converted. */}
      {h.usageSeats.length > 0 && (
        <p className="text-sm mb-1" data-head-usage>
          {t("costs.dent.head.usageOut", { seats: h.usageSeats.join(", "), n: pts(h.usagePoints), list: fmtUSD(h.usage) })}
        </p>
      )}
      {h.usageCharges ? (
        <p className="text-sm mb-1" data-head-usage-charges>{t("costs.dent.head.usageCharges", { list: usageList(t, h.usageCharges) })}</p>
      ) : h.usageSeats.length > 0 && (
        <p className="muted text-xs mb-1" data-head-usage-charges>{t("costs.dent.head.usageNoCharge")}</p>
      )}
      {/* list = metered + before + after + unpriced (+ usage, said above): the multiple divides only the first (M2). */}
      {outsideParts.length > 0 && (
        <p className="muted text-xs mb-1" data-head-unmetered>
          {t("costs.dent.head.unmetered", { list: fmtUSD(h.list), metered: fmtUSD(h.metered), parts: outsideParts.join("; ") })}
        </p>
      )}
      {/* Per-use sessions are on no meter: in the tiles, not in this sum (M6). */}
      {h.perUse && h.perUse.calls > 0 && (
        <p className="muted text-xs mb-1" data-head-per-use>
          {t("costs.dent.head.perUse", { list: outsideMoney(t, h.perUse, "lead") })}
        </p>
      )}
      {check && <p className="muted text-xs mb-1" data-price-check>{check}</p>}
      {h.sources.length > 0 && (
        <ul className="muted text-xs mb-1" aria-label={t("costs.dent.head.sourcesTitle")} data-head-sources style={{ paddingLeft: 16 }}>
          {h.sources.map((s) => (
            <li key={s.source} data-head-source={s.source}>
              {t("costs.dent.head.source", { source: s.source,
                list: s.before ? t("costs.incl.beforeRange") : s.list == null ? t("costs.incl.noList")
                  : s.recomputed > 0 ? t("costs.lead.part", { usd: fmtUSD(s.list) }) : fmtUSD(s.list) })}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

export function SubscriptionDentRow({ d, filtered, h = null, sourceParts = [] }: {
  d: SubscriptionDent; filtered: boolean;
  /** The headline: all the work at token prices against the one real cost. */
  h?: Headline | null;
  /** The outside part per source (the server's source_parts), for the estimate below; before: its calls lie before the range. */
  sourceParts?: { source: string; list_usd: number; real_usd: number; before?: boolean }[];
}) {
  const { t } = useTranslation();
  // Points are one unit only at one point price: then each part's points
  // stand beside its dollars, else the dollars carry the row alone.
  const part = (p: DentPart) => d.points != null
    ? t("costs.dent.partSubPoints", { pct: pctOf(p.share), pts: pts(p.points), list: fmtUSD(p.list), x: atLeast(p.multiple) })
    : t("costs.dent.partSub", { pct: pctOf(p.share), list: fmtUSD(p.list), x: atLeast(p.multiple) });
  const tg = d.together;
  const un = d.unattributed;
  // A part no split step backs has no figure (its real cost is 0 because
  // nothing was split, not because it cost nothing): "–", the reason, and the
  // part's work at token prices where the headline has it (#164).
  const notSplit = (tokenPrice: number | null | undefined) => [
    tokenPrice != null && tokenPrice >= 0.005 ? t("costs.dent.partTokenPrice", { list: fmtUSD(tokenPrice) }) : null,
    t("costs.dent.partNotSplit"),
  ].filter(Boolean).join(" · ");
  // The unattributed part's two halves: beyond the rate (at least) — only
  // where some seat has a rate to measure it by, else that there is none
  // yet — and the chunks that moved with no call (≈).
  const unParts = [d.seats.some((s) => s.pointsPerList != null)
    ? t("costs.dent.unattributedBeyond", { usd: fmtUSD(un.beyond) })
    : t("costs.dent.unattributedNoRate")];
  if (un.noCall >= 0.005) unParts.push(t("costs.dent.unattributedNoCall", { usd: fmtUSD(un.noCall) }));
  const sources = d.sources.join(", ");
  // The outside part per source (the server's source_parts), in the covey
  // operator and the other projects: each with its real share and its share of
  // the dent. No list price on the cards: the server's per source is the list
  // price, the cards beside them state the lead price (#163 review, M2); the
  // per-source list is in the lines below.
  const shown = sourceParts.filter((p) => p.list_usd > 0 || p.real_usd > 0);
  const outsideParts: { key: "operator" | "projects" | "rest"; real: number; title?: string }[] = (["operator", "projects"] as const).flatMap((key) => {
    const ps = shown.filter((p) => isOperatorSource(p.source) === (key === "operator"));
    if (ps.length === 0) return [];
    return [{ key, real: ps.reduce((a, p) => a + p.real_usd, 0),
      title: key === "projects" ? ps.map((p) => `${p.source}: ≈ ${fmtUSD(p.real_usd)}`).join("\n") : undefined }];
  });
  // The cards must add up to the outside part: what the per-source shares do
  // not carry (a seat with outside real cost and no source parts, a source
  // with no calls in the range) is its own card, never lost (#166 review,
  // F2); where they carry more than the outside part, the one card stands.
  const sourced = outsideParts.reduce((a, o) => a + o.real, 0), gap = d.outside.real - sourced;
  if (outsideParts.length > 0 && gap >= 0.005) outsideParts.push({ key: "rest", real: gap });
  const splitCards = d.outside.split && outsideParts.length > 0 && gap > -0.005;
  const seat = (s: SubscriptionDent["seats"][number]) => `${s.key} #${s.slot}`;
  // The unsplit readings by why: the outside sessions had not reported them,
  // or their outside calls all had no price. n is the number as shown.
  const unpricedSteps = d.unsplit.unpricedSteps;
  const notReported = d.unsplit.steps - unpricedSteps;
  const unsplitSub = unpricedSteps === 0
    ? t("costs.dent.unsplitSub", { pct: pctOf(d.unsplit.share), n: count(notReported) })
    : notReported === 0
      ? t("costs.dent.unsplitSubUnpriced", { pct: pctOf(d.unsplit.share), n: count(unpricedSteps) })
      : t("costs.dent.unsplitSubBoth", { pct: pctOf(d.unsplit.share), n: count(notReported), m: count(unpricedSteps) });
  // The rate per priced seat with movement: 1 point ≈ $X of list price
  // (1 / k), or "no rate yet". The seat is named only when there are two.
  const rated = d.seats.filter((s) => s.real != null && s.steps > 0 && (s.split || s.pointsPerList != null));
  const named = rated.length > 1;
  const rates = rated.map((s) => rateLine(t, named ? seat(s) : null, s.pointsPerList, s.olderPlan));
  return (
    <section className="card mb-4" aria-label={t("costs.dent.title")} data-dent-row>
      <div style={{ fontWeight: 600, marginBottom: 4 }}>
        {t("costs.dent.title")}
        {filtered && <OrgWide />}
      </div>
      {h && <HeadlineSentence h={h} />}
      {/* The per-party real shares are an estimate: a detail, collapsed. */}
      <details className="mb-1" data-dent-detail>
      <summary className="text-sm" style={{ cursor: "pointer" }}>{t("costs.dent.head.splitSummary")}</summary>
      <p className="muted text-xs mb-3 mt-2" style={{ maxWidth: 760 }}>{t("costs.dent.hint")}</p>
      <div className="flex flex-wrap gap-3 mb-3">
        {d.real == null ? (
          // No reading has a plan price: no figure, never ≈ $0.00. Every one
          // under the usage basis (0151): the charges are the money.
          <Part test="total" label={t("costs.dent.total")} value="–"
            sub={t(d.usagePoints > 0 && d.unpricedPoints === 0 || d.noMultiple === "usage" ? "costs.dent.noPricedReadingUsage" : "costs.dent.noPricedReading")} />
        ) : (
          <Part test="total" label={t("costs.dent.total")} value={`≈ ${fmtUSD(d.real)}`}
            sub={d.points != null ? t("costs.dent.totalSubPoints", { pts: pts(d.points), sources }) : t("costs.dent.totalSub", { sources })} />
        )}
        {/* Estimates, per chunk (F3c): the bounds are on the multiples and
            on the unattributed part beyond the rate. Without a priced
            reading there is nothing to split: "–", never ≈ $0.00. */}
        {d.real == null ? <>
          <Part test="covey" label={t("costs.dent.covey")} value="–" sub={t("costs.dent.noPart")} color={SPLIT_COLOR.covey} />
          <Part test="outside" label={t("costs.dent.outside")} value="–" sub={t("costs.dent.noPart")} color={SPLIT_COLOR.outside} hatch={SPLIT_HATCH.outside} />
          <Part test="unattributed" label={t("costs.dent.unattributed")} value="–" sub={t("costs.dent.noPart")} color={SPLIT_COLOR.unattributed} hatch={SPLIT_HATCH.unattributed} />
        </> : <>
          {d.covey.split
            ? <Part test="covey" label={t("costs.dent.covey")} value={`≈ ${fmtUSD(d.covey.real)}`} sub={part(d.covey)} color={SPLIT_COLOR.covey} />
            : <Part test="covey" label={t("costs.dent.covey")} value="–" sub={notSplit(h?.coveyList)} color={SPLIT_COLOR.covey} />}
          {/* The outside part in two (#163), from the server's real share per source: the covey operator and the other
              projects, the latter with each project as its hover. Without per-source parts, the one outside part. */}
          {splitCards ? outsideParts.map((o) => (
            <Part key={o.key} test={o.key} label={t(o.key === "operator" ? "costs.dent.operator" : o.key === "projects" ? "costs.dent.projects" : "costs.dent.outsideRest")} value={`≈ ${fmtUSD(o.real)}`}
              sub={t("costs.dent.partSubSource", { pct: pctOf(d.real! > 0 ? o.real / d.real! : null) })}
              title={o.title} color={SPLIT_COLOR.outside} hatch={SPLIT_HATCH.outside} />
          )) : d.outside.split ? (
            <Part test="outside" label={t("costs.dent.outside")} value={`≈ ${fmtUSD(d.outside.real)}`} sub={part(d.outside)} color={SPLIT_COLOR.outside} hatch={SPLIT_HATCH.outside} />
          ) : (
            <Part test="outside" label={t("costs.dent.outside")} value="–" sub={notSplit(h?.outsideList)} color={SPLIT_COLOR.outside} hatch={SPLIT_HATCH.outside} />
          )}
          <Part test="unattributed" label={t("costs.dent.unattributed")} value={`≈ ${fmtUSD(un.real)}`}
            sub={d.points != null
              ? t("costs.dent.unattributedSubPoints", { pct: pctOf(un.share), pts: pts(un.points), parts: unParts.join(" · ") })
              : t("costs.dent.unattributedSub", { pct: pctOf(un.share), parts: unParts.join(" · ") })}
            color={SPLIT_COLOR.unattributed} hatch={SPLIT_HATCH.unattributed} />
        </>}
        {d.unsplit.steps > 0 && (
          <Part test="unsplit" label={t("costs.dent.unsplit")} value={`≈ ${fmtUSD(d.unsplit.real)}`}
            sub={unsplitSub}
            color={SPLIT_COLOR.unsplit} dashed={SPLIT_COLOR.unsplitEdge} />
        )}
      </div>
      {/* The plain sentence: what the subscription paid — the whole dent —
          and, of it, what the attributed movement paid for how much
          list-price work, by whom. */}
      {d.real != null && tg.real > 0 && d.outside.split && (
        <p className="text-sm mb-1" data-dent-sentence>
          {un.real >= 0.005
            ? t("costs.dent.sentenceAttributed", { dent: fmtUSD(d.real), real: fmtUSD(tg.real), list: fmtUSD(tg.list), covey: fmtUSD(d.covey.list), outside: fmtUSD(d.outside.list) })
            : d.unsplit.real >= 0.005
              ? t("costs.dent.sentenceUnsplit", { dent: fmtUSD(d.real), real: fmtUSD(tg.real), list: fmtUSD(tg.list), covey: fmtUSD(d.covey.list), outside: fmtUSD(d.outside.list) })
              : t("costs.dent.sentence", { real: fmtUSD(d.real), list: fmtUSD(tg.list), covey: fmtUSD(d.covey.list), outside: fmtUSD(d.outside.list) })}
        </p>
      )}
      {un.real >= 0.005 && (
        <p className="text-sm mb-1" data-dent-unattributed>{t("costs.dent.unattributedSentence", { usd: fmtUSD(un.real) })}</p>
      )}
      {rates.length > 0 && (
        <p className="muted text-xs mb-1" data-dent-rate>
          {rates.join(" ")}
          {rated.some((s) => s.pointsPerList != null) && <> {t("costs.dent.lowerBound")}</>}
        </p>
      )}
      {tg.multiple ? (
        <p className="text-sm mb-1" data-dent-multiples>
          {t("costs.dent.multiples", { covey: atLeast(d.covey.multiple), outside: atLeast(d.outside.multiple), together: atLeast(tg.multiple) })}
        </p>
      ) : (
        <p className="muted text-xs mb-1" data-dent-no-multiple>
          {d.noMultiple === "usage" ? t("costs.dent.noMultipleUsage")
            : d.noMultiple === "split"
            ? t(d.unsplit.steps === 0 && un.steps > 0 ? "costs.dent.noMultipleSplitNoCall"
              : unpricedSteps > 0 && notReported === 0 ? "costs.dent.noMultipleSplitUnpriced" : "costs.dent.noMultipleSplit")
            : !d.outside.split ? t("costs.dent.noMultipleOutside")
            : t("costs.dent.noMultipleMovement", { pts: pts(tg.points) })}
        </p>
      )}
      {notReported > 0 && (
        <p className="muted text-xs mb-1" data-dent-unsplit>
          {t("costs.dent.unsplitNote", { usd: fmtUSD(d.unsplit.real - d.unsplit.unpricedReal), n: count(notReported) })}
        </p>
      )}
      {unpricedSteps > 0 && (
        <p className="muted text-xs mb-1" data-dent-unsplit-unpriced>
          {t("costs.dent.unsplitUnpricedNote", { usd: fmtUSD(d.unsplit.unpricedReal), n: count(unpricedSteps) })}
        </p>
      )}
      {d.incompleteChunks > 0 && (
        <p className="muted text-xs mb-1" data-dent-incomplete>{t("costs.dent.incompleteNote", { n: count(d.incompleteChunks) })}</p>
      )}
      {d.unpricedPoints > 0 && (
        <p className="muted text-xs mb-1" data-dent-unpriced>{t("costs.dent.unpricedPoints", { n: pts(d.unpricedPoints) })}</p>
      )}
      {d.usagePoints > 0 && (
        <p className="muted text-xs mb-1" data-dent-usage>{t("costs.dent.usagePoints", { n: pts(d.usagePoints) })}</p>
      )}
      {d.unpricedCalls > 0 && (
        <p className="muted text-xs mb-1" data-dent-unpriced-calls>{t("costs.dent.unpricedCalls", { n: count(d.unpricedCalls) })}</p>
      )}
      {d.unmeteredBefore >= 0.005 && (
        <p className="muted text-xs mb-1" data-dent-unmetered="before">{t("costs.dent.unmeteredBefore", { usd: fmtUSD(d.unmeteredBefore) })}</p>
      )}
      {d.unmeteredAfter >= 0.005 && (
        <p className="muted text-xs mb-1" data-dent-unmetered="after">{t("costs.dent.unmeteredAfter", { usd: fmtUSD(d.unmeteredAfter) })}</p>
      )}
      {/* Per seat: points of seats on different plans are not one unit, and
          a seat whose points have no plan price has no figure. */}
      {(d.seats.length > 1 || d.seats.some((s) => s.real == null)) && (
        <ul className="muted text-xs mb-1" aria-label={t("costs.dent.seats")} data-dent-seats style={{ paddingLeft: 16 }}>
          {d.seats.map((s) => (
            <li key={seat(s)} data-dent-seat={seat(s)}>
              {s.real == null
                ? (s.steps === 0 ? t("costs.dent.seatNoReading", { seat: seat(s) })
                  : s.usageSteps > 0 && s.unpricedPoints === 0 ? t("costs.dent.seatUsage", { seat: seat(s), n: pts(s.usagePoints) })
                  : s.usageSteps > 0 ? t("costs.dent.seatNoPriceAndUsage", { seat: seat(s), n: pts(s.unpricedPoints), m: pts(s.usagePoints) })
                  : t("costs.dent.seatNoPrice", { seat: seat(s), n: pts(s.unpricedPoints) }))
                : t(s.outsideParts?.operator != null && s.outsideParts.projects != null ? "costs.dent.seatLineSplit" : "costs.dent.seatLine", {
                  seat: seat(s), real: fmtUSD(s.real), pts: pts(s.points), covey: fmtUSD(s.covey), outside: fmtUSD(s.outside),
                  operator: fmtUSD(s.outsideParts?.operator ?? 0), projects: fmtUSD(s.outsideParts?.projects ?? 0),
                  unattributed: fmtUSD(s.unattributed), unsplit: fmtUSD(s.unsplit) })}
              {s.sources.length === 0 && t("costs.dent.seatNoSource")}
            </li>
          ))}
        </ul>
      )}
      {sourceParts.some((p) => p.list_usd > 0 || p.real_usd > 0) && (
        <ul className="muted text-xs mb-1" aria-label={t("costs.dent.head.sourcesRealTitle")} data-dent-source-parts style={{ paddingLeft: 16 }}>
          {sourceParts.filter((p) => p.list_usd > 0 || p.real_usd > 0).map((p) => (
            <li key={p.source} data-dent-source-part={p.source}>
              {t("costs.dent.head.sourceReal", { source: p.source, usd: fmtUSD(p.real_usd), list: fmtUSD(p.list_usd) })}
              {p.before && <> · {t("costs.incl.beforeRange")}</>}
            </li>
          ))}
        </ul>
      )}
      </details>
      <p className="muted text-xs mb-0" style={{ fontStyle: "italic" }}>{t("costs.dent.caveat")}</p>
    </section>
  );
}

/** The pay-per-token headline (0152): the lead figure over the spans an API
 *  key's bill and the counted usage charges (by hand and the monthly meter's)
 *  cover, against that money — one multiple where all of it is in USD, each
 *  currency on its own and why otherwise. Its own section: never added to a
 *  subscription meter's real cost. */
export function PayPerTokenRow({ h, filtered }: { h: PayPerTokenHeadline; filtered: boolean }) {
  const { t } = useTranslation();
  const parts = [
    h.billed != null ? t("costs.ppt.bill", { usd: fmtUSD(h.billed) }) : null,
    h.charges.length > 0 ? t("costs.ppt.charges", { list: usageList(t, h.charges) }) : null,
  ].filter((x): x is string => x != null);
  const plainUsd = parts.length === 1 && h.charges.every((c) => c.currency === "USD" && !c.meter);
  const real = h.realUsd == null ? parts.join(" + ") : plainUsd ? fmtUSD(h.realUsd) : `${fmtUSD(h.realUsd)} (${parts.join(" + ")})`;
  const split = h.outsideList >= 0.005 ? ` (${t("costs.ppt.parts", { covey: fmtUSD(h.coveyList), outside: fmtUSD(h.outsideList) })})` : "";
  const args = { list: fmtUSD(h.list), parts: split, real };
  return (
    <section className="card mb-4" aria-label={t("costs.ppt.title")} data-ppt>
      <div style={{ fontWeight: 600, marginBottom: 4 }}>
        {t("costs.ppt.title")}
        {filtered && <OrgWide />}
      </div>
      <p className="text-sm mb-1" style={{ fontWeight: 600 }} data-ppt-sentence>
        {h.multiple != null ? t("costs.ppt.sentence", { ...args, x: `≈ ${fmtNumber(h.multiple, 2)}×` }) : t("costs.ppt.sentenceNoMultiple", args)}
      </p>
      {/* A non-USD charge is in USD only at the rate a person entered: named beside the multiple. */}
      {h.multiple != null && h.rates.length > 0 && (
        <p className="muted text-xs mb-1" data-ppt-rates>
          {t("costs.ppt.rates", { rates: h.rates.map((r) => t("costs.basis.storedRate", { rate: fmtNumber(r.rate, 4), currency: r.currency, date: r.date })).join("; ") })}
        </p>
      )}
      {h.why && (
        <p className="muted text-xs mb-1" data-ppt-no-multiple={h.why}>
          {h.why === "rate" ? t("costs.ppt.noMultipleRate", { currencies: h.noRate.join(", ") })
            : h.why === "list" ? t("costs.ppt.noMultipleList") : t("costs.ppt.noMultipleMoney")}
        </p>
      )}
      {h.billedPending && <p className="muted text-xs mb-1" data-ppt-pending>{t("costs.ppt.billPending")}</p>}
      <p className="muted text-xs mb-0" data-ppt-credentials>
        {t("costs.ppt.credentials", { list: h.credentials.map((c) => `${c.name} (${t(c.money === "bill" ? "costs.ppt.byBill" : "costs.ppt.byCharges")})`).join(", ") })}
        {" "}{t("costs.ppt.hint")}
      </p>
    </section>
  );
}
