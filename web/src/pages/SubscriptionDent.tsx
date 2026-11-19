import { useTranslation } from "react-i18next";
import { fmtNumber, fmtUSD } from "../format";
import { SPLIT_COLOR } from "./costSources";
import type { DentPart, FloorMultiple, SubscriptionDent } from "./costFigures";
import { OrgWide, Swatch } from "./StackedCostChart";

/* The headline of the Costs page once Claude Code sessions outside covey
 * report on a subscription meter (#120): the whole dent in the meters over
 * the range, split between covey's agents, the sessions that drive them and
 * the movement neither explains, and list $ per real $ for covey, the
 * outside sessions and both together.
 *
 * Every figure is the server's split (costFigures.subscriptionDent over
 * external.meter): per meter reading, over the span since the reading
 * before it, by list-price share. The meter is account-wide: a machine that
 * does not report (another one on the same subscription) moves it too. Per
 * seat the server takes the lowest rate of points per list $ seen over a
 * run of split readings (k, F3b); movement beyond it, and readings that
 * moved with no call, are unattributed — booked to nobody, never to covey or
 * a source — and a lower bound, so the other parts are at most what they
 * show and the multiples at least. Movement in a reading the outside
 * sessions had not reported, or whose outside calls all had no price, is
 * split to nobody and stands as its own part. Points without a plan price
 * are not in the dent and are counted beside it. The dent ignores the agent
 * filter and says so. */

const times = (v: number) => `${fmtNumber(v, 1)}×`;
const pctOf = (v: number | null) => (v != null ? `${fmtNumber(v * 100, 0)} %` : "–");
const pts = (v: number) => fmtNumber(v, v < 10 ? 1 : 0);
const count = (v: number) => fmtNumber(v, 0);
/** A lower bound: "≥ 1.1×". */
const atLeast = (m: FloorMultiple | null) => (m ? `≥ ${times(m.low)}` : "–");

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
  // Points are one unit only at one point price: then each part's points
  // stand beside its dollars, else the dollars carry the row alone.
  const part = (p: DentPart) => d.points != null
    ? t("costs.dent.partSubPoints", { pct: pctOf(p.share), pts: pts(p.points), list: fmtUSD(p.list), x: atLeast(p.multiple) })
    : t("costs.dent.partSub", { pct: pctOf(p.share), list: fmtUSD(p.list), x: atLeast(p.multiple) });
  const tg = d.together;
  const un = d.unattributed;
  const sources = d.sources.join(", ");
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
  const rates = rated.map((s) => s.pointsPerList != null && s.pointsPerList > 0
    ? t(named ? "costs.dent.rateSeat" : "costs.dent.rate", { seat: seat(s), usd: fmtUSD(1 / s.pointsPerList) })
    : t(named ? "costs.dent.rateUnknownSeat" : "costs.dent.rateUnknown", { seat: seat(s) }));
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
        <Part test="unattributed" label={t("costs.dent.unattributed")} value={`≈ ${fmtUSD(un.real)}`}
          sub={d.points != null
            ? t("costs.dent.unattributedSubPoints", { pct: pctOf(un.share), pts: pts(un.points) })
            : t("costs.dent.unattributedSub", { pct: pctOf(un.share) })}
          color={SPLIT_COLOR.unattributed} />
        {d.unsplit.steps > 0 && (
          <Part test="unsplit" label={t("costs.dent.unsplit")} value={`≈ ${fmtUSD(d.unsplit.real)}`}
            sub={unsplitSub}
            color={SPLIT_COLOR.unsplit} dashed={SPLIT_COLOR.unsplitEdge} />
        )}
      </div>
      {/* The plain sentence: what the subscription paid — the whole dent —
          and, of it, what the attributed movement paid for how much
          list-price work, by whom. */}
      {d.real != null && tg.real > 0 && (
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
          {d.noMultiple === "split"
            ? t(d.unsplit.steps === 0 && un.steps > 0 ? "costs.dent.noMultipleSplitNoCall"
              : unpricedSteps > 0 && notReported === 0 ? "costs.dent.noMultipleSplitUnpriced" : "costs.dent.noMultipleSplit")
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
                  covey: fmtUSD(s.covey), outside: fmtUSD(s.outside), unattributed: fmtUSD(s.unattributed), unsplit: fmtUSD(s.unsplit) })}
              {s.sources.length === 0 && t("costs.dent.seatNoSource")}
            </li>
          ))}
        </ul>
      )}
      <p className="muted text-xs mb-0" style={{ fontStyle: "italic" }}>{t("costs.dent.caveat")}</p>
    </section>
  );
}
