import { useTranslation } from "react-i18next";
import { fmtNumber, fmtUSD } from "../format";
import { SPLIT_COLOR } from "./costSources";
import type { DentPart, RangedMultiple, SubscriptionDent } from "./costFigures";
import { OrgWide, Swatch } from "./StackedCostChart";

/* The headline of the Costs page once Claude Code sessions outside covey
 * report on a subscription meter (#120): the whole dent in the meters over
 * the range, split between covey's agents and the sessions that drive them,
 * and list $ per real $ for each and for both together.
 *
 * Every figure is the server's split (costFigures.subscriptionDent over
 * external.meter): per meter reading, over the span since the reading
 * before it, by list-price share. Movement in a reading the outside
 * sessions had not reported, or whose outside calls all had no price, is
 * split to nobody and stands as its own part — it is never booked to covey
 * whole. Points without a plan price are not in
 * the dent and are counted beside it. The meters are account-wide, so the
 * whole page remains an estimate; the dent ignores the agent filter and
 * says so. */

const times = (v: number) => `${fmtNumber(v, 1)}×`;
const pctOf = (v: number | null) => (v != null ? `${fmtNumber(v * 100, 0)} %` : "–");
const pts = (v: number) => fmtNumber(v, v < 10 ? 1 : 0);
const count = (v: number) => fmtNumber(v, 0);

/** "≈ 1.1× (range 0.9× – 1.2×)"; with no upper end (the part's real cost
 *  could be 0 within the meter's whole percents), the lower bound only. */
function useMultiple() {
  const { t } = useTranslation();
  return (m: RangedMultiple | null) => {
    if (m?.kind !== "estimate") return "–";
    if (!Number.isFinite(m.high)) return `≥ ${times(m.low)}`;
    return `≈ ${times(m.value)} (${t("costs.fig.multipleRange", { low: times(m.low), high: times(m.high) })})`;
  };
}

function Part({ label, value, sub, color, dashed, test }: {
  label: string; value: string; sub: string; color?: string; dashed?: string; test: string;
}) {
  return (
    <div role="group" aria-label={label} data-dent={test}
      style={{ padding: "10px 14px", borderRadius: 8, border: "1px solid var(--border)", minWidth: 170, flex: "1 1 170px" }}>
      <div className="muted text-xs" style={{ display: "flex", alignItems: "center", gap: 6 }}>
        {color && <Swatch color={color} hatched dashed={dashed} />}
        {label}
      </div>
      <div style={{ fontSize: 20, fontWeight: 600, marginTop: 2, fontVariantNumeric: "tabular-nums" }}>{value}</div>
      <div className="muted text-xs" style={{ marginTop: 2 }}>{sub}</div>
    </div>
  );
}

export function SubscriptionDentRow({ d, filtered }: { d: SubscriptionDent; filtered: boolean }) {
  const { t } = useTranslation();
  const mult = useMultiple();
  // Points are one unit only at one point price: then each part's points
  // stand beside its dollars, else the dollars carry the row alone.
  const part = (p: DentPart) => d.points != null
    ? t("costs.dent.partSubPoints", { pct: pctOf(p.share), pts: pts(p.points), list: fmtUSD(p.list), x: mult(p.multiple) })
    : t("costs.dent.partSub", { pct: pctOf(p.share), list: fmtUSD(p.list), x: mult(p.multiple) });
  const tg = d.together;
  const sources = d.sources.join(", ");
  const seat = (s: SubscriptionDent["seats"][number]) => `${s.key} #${s.slot}`;
  const approxM = (m: RangedMultiple | null) => (m?.kind === "estimate" ? (Number.isFinite(m.high) ? `≈ ${times(m.value)}` : `≥ ${times(m.low)}`) : "–");
  // The unsplit readings by why: the outside sessions had not reported them,
  // or their outside calls all had no price. n is the number as shown.
  const unpricedSteps = d.unsplit.unpricedSteps;
  const notReported = d.unsplit.steps - unpricedSteps;
  const unsplitSub = unpricedSteps === 0
    ? t("costs.dent.unsplitSub", { pct: pctOf(d.unsplit.share), n: count(notReported) })
    : notReported === 0
      ? t("costs.dent.unsplitSubUnpriced", { pct: pctOf(d.unsplit.share), n: count(unpricedSteps) })
      : t("costs.dent.unsplitSubBoth", { pct: pctOf(d.unsplit.share), n: count(notReported), m: count(unpricedSteps) });
  return (
    <section className="card mb-4" aria-label={t("costs.dent.title")} data-dent-row>
      <div style={{ fontWeight: 600, marginBottom: 4 }}>
        {t("costs.dent.title")}
        {filtered && <OrgWide />}
      </div>
      <p className="muted text-xs mb-3" style={{ maxWidth: 760 }}>{t("costs.dent.hint")}</p>
      <div className="flex flex-wrap gap-3 mb-3">
        {d.real == null ? (
          // No reading has a plan price: no figure, never ≈ $0.00.
          <Part test="total" label={t("costs.dent.total")} value="–" sub={t("costs.dent.noPricedReading")} />
        ) : (
          <Part test="total" label={t("costs.dent.total")} value={`≈ ${fmtUSD(d.real)}`}
            sub={d.points != null ? t("costs.dent.totalSubPoints", { pts: pts(d.points), sources }) : t("costs.dent.totalSub", { sources })} />
        )}
        <Part test="covey" label={t("costs.dent.covey")} value={`≈ ${fmtUSD(d.covey.real)}`} sub={part(d.covey)} color={SPLIT_COLOR.covey} />
        <Part test="outside" label={t("costs.dent.outside")} value={`≈ ${fmtUSD(d.outside.real)}`} sub={part(d.outside)} color={SPLIT_COLOR.outside} />
        {d.unsplit.steps > 0 && (
          <Part test="unsplit" label={t("costs.dent.unsplit")} value={`≈ ${fmtUSD(d.unsplit.real)}`}
            sub={unsplitSub}
            color={SPLIT_COLOR.unsplit} dashed={SPLIT_COLOR.unsplitEdge} />
        )}
      </div>
      {/* The plain sentence: what the subscription paid — the whole dent —
          and, of it, what the split readings paid for how much list-price
          work, by whom. */}
      {d.real != null && tg.real > 0 && (
        <p className="text-sm mb-1" data-dent-sentence>
          {d.unsplit.real >= 0.005
            ? t("costs.dent.sentenceUnsplit", { dent: fmtUSD(d.real), real: fmtUSD(tg.real), list: fmtUSD(tg.list), covey: fmtUSD(d.covey.list), outside: fmtUSD(d.outside.list) })
            : t("costs.dent.sentence", { real: fmtUSD(d.real), list: fmtUSD(tg.list), covey: fmtUSD(d.covey.list), outside: fmtUSD(d.outside.list) })}
        </p>
      )}
      {tg.multiple ? (
        <p className="text-sm mb-1" data-dent-multiples>
          {t("costs.dent.multiples", {
            covey: approxM(d.covey.multiple), outside: approxM(d.outside.multiple), together: approxM(tg.multiple),
            low: times(tg.multiple.low), high: Number.isFinite(tg.multiple.high) ? times(tg.multiple.high) : "–",
          })}
        </p>
      ) : (
        <p className="muted text-xs mb-1" data-dent-no-multiple>
          {d.noMultiple === "split"
            ? t(unpricedSteps > 0 && notReported === 0 ? "costs.dent.noMultipleSplitUnpriced" : "costs.dent.noMultipleSplit")
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
      {d.unpricedPoints > 0 && (
        <p className="muted text-xs mb-1" data-dent-unpriced>{t("costs.dent.unpricedPoints", { n: pts(d.unpricedPoints) })}</p>
      )}
      {d.unpricedCalls > 0 && (
        <p className="muted text-xs mb-1" data-dent-unpriced-calls>{t("costs.dent.unpricedCalls", { n: count(d.unpricedCalls) })}</p>
      )}
      {d.unmeteredList >= 0.005 && (
        <p className="muted text-xs mb-1" data-dent-unmetered>{t("costs.dent.unmetered", { usd: fmtUSD(d.unmeteredList) })}</p>
      )}
      {/* Per seat: points of seats on different plans are not one unit, and
          a seat whose points have no plan price has no figure. */}
      {(d.seats.length > 1 || d.seats.some((s) => s.real == null)) && (
        <ul className="muted text-xs mb-1" aria-label={t("costs.dent.seats")} data-dent-seats style={{ paddingLeft: 16 }}>
          {d.seats.map((s) => (
            <li key={seat(s)} data-dent-seat={seat(s)}>
              {s.real == null
                ? (s.steps === 0 ? t("costs.dent.seatNoReading", { seat: seat(s) }) : t("costs.dent.seatNoPrice", { seat: seat(s), n: pts(s.unpricedPoints) }))
                : t("costs.dent.seatLine", { seat: seat(s), real: fmtUSD(s.real), pts: pts(s.points),
                  covey: fmtUSD(s.covey), outside: fmtUSD(s.outside), unsplit: fmtUSD(s.unsplit) })}
              {s.sources.length === 0 && t("costs.dent.seatNoSource")}
            </li>
          ))}
        </ul>
      )}
      <p className="muted text-xs mb-0" style={{ fontStyle: "italic" }}>{t("costs.dent.caveat")}</p>
    </section>
  );
}
