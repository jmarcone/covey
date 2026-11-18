import { useTranslation } from "react-i18next";
import { fmtNumber, fmtUSD } from "../format";
import { SPLIT_COLOR } from "./costSources";
import type { DentPart, Multiple, SubscriptionDent } from "./costFigures";
import { OrgWide, Swatch } from "./StackedCostChart";

/* The headline of the Costs page once Claude Code sessions outside covey
 * report on a subscription meter (#120): the whole dent in the meters over
 * the range, split between covey's agents and the sessions that drive them,
 * and list $ per real $ for each and for both together.
 *
 * Every figure is the server's split (costFigures.subscriptionDent over
 * external.meter): per UTC hour by list-price share, whatever the chart's
 * bucket. Movement in an hour the outside sessions did not report is split
 * to nobody and stands as its own part — it is never booked to covey whole.
 * The meters are account-wide, so the whole page remains an estimate; the
 * dent ignores the agent filter and says so. */

const times = (v: number) => (Number.isFinite(v) ? `${fmtNumber(v, 1)}×` : "∞");
const pctOf = (v: number | null) => (v != null ? `${fmtNumber(v * 100, 0)} %` : "–");
const approx = (m: Multiple | null) => (m?.kind === "estimate" ? `≈ ${times(m.value)}` : "–");

function Part({ label, value, sub, color, test }: { label: string; value: string; sub: string; color?: string; test: string }) {
  return (
    <div role="group" aria-label={label} data-dent={test}
      style={{ padding: "10px 14px", borderRadius: 8, border: "1px solid var(--border)", minWidth: 170, flex: "1 1 170px" }}>
      <div className="muted text-xs" style={{ display: "flex", alignItems: "center", gap: 6 }}>
        {color && <Swatch color={color} hatched />}
        {label}
      </div>
      <div style={{ fontSize: 20, fontWeight: 600, marginTop: 2, fontVariantNumeric: "tabular-nums" }}>{value}</div>
      <div className="muted text-xs" style={{ marginTop: 2 }}>{sub}</div>
    </div>
  );
}

export function SubscriptionDentRow({ d, filtered }: { d: SubscriptionDent; filtered: boolean }) {
  const { t } = useTranslation();
  const part = (p: DentPart) => t("costs.dent.partSub", { pct: pctOf(p.share), list: fmtUSD(p.list), x: approx(p.multiple) });
  const tg = d.together;
  return (
    <section className="card mb-4" aria-label={t("costs.dent.title")} data-dent-row>
      <div style={{ fontWeight: 600, marginBottom: 4 }}>
        {t("costs.dent.title")}
        {filtered && <OrgWide />}
      </div>
      <p className="muted text-xs mb-3" style={{ maxWidth: 760 }}>{t("costs.dent.hint")}</p>
      <div className="flex flex-wrap gap-3 mb-3">
        <Part test="total" label={t("costs.dent.total")} value={`≈ ${fmtUSD(d.real)}`}
          sub={t("costs.dent.totalSub", { sources: d.sources.join(", ") })} />
        <Part test="covey" label={t("costs.dent.covey")} value={`≈ ${fmtUSD(d.covey.real)}`} sub={part(d.covey)} color={SPLIT_COLOR.covey} />
        <Part test="outside" label={t("costs.dent.outside")} value={`≈ ${fmtUSD(d.outside.real)}`} sub={part(d.outside)} color={SPLIT_COLOR.outside} />
        {d.unsplit.hours > 0 && (
          <Part test="unsplit" label={t("costs.dent.unsplit")} value={`≈ ${fmtUSD(d.unsplit.real)}`}
            sub={t("costs.dent.unsplitSub", { pct: pctOf(d.unsplit.share), count: d.unsplit.hours })} color={SPLIT_COLOR.unsplit} />
        )}
      </div>
      {/* The plain sentence: what the subscription paid, and for how much
          list-price work, by whom — over the hours that were split. */}
      {tg.real > 0 && (
        <p className="text-sm mb-1" data-dent-sentence>
          {t("costs.dent.sentence", { real: fmtUSD(tg.real), list: fmtUSD(tg.list), covey: fmtUSD(d.covey.list), outside: fmtUSD(d.outside.list) })}
        </p>
      )}
      {tg.multiple ? (
        <p className="text-sm mb-1" data-dent-multiples>
          {t("costs.dent.multiples", {
            covey: approx(d.covey.multiple), outside: approx(d.outside.multiple), together: approx(tg.multiple),
            low: times(tg.multiple.low), high: times(tg.multiple.high),
          })}
        </p>
      ) : (
        <p className="muted text-xs mb-1" data-dent-no-multiple>
          {d.noMultiple === "split" ? t("costs.dent.noMultipleSplit") : t("costs.dent.noMultipleMovement", { pts: fmtNumber(d.splitPoints, 0) })}
        </p>
      )}
      {d.unsplit.hours > 0 && (
        <p className="muted text-xs mb-1" data-dent-unsplit>{t("costs.dent.unsplitNote", { usd: fmtUSD(d.unsplit.real), count: d.unsplit.hours })}</p>
      )}
      {d.unmeteredList >= 0.005 && (
        <p className="muted text-xs mb-1" data-dent-unmetered>{t("costs.dent.unmetered", { usd: fmtUSD(d.unmeteredList) })}</p>
      )}
      <p className="muted text-xs mb-0" style={{ fontStyle: "italic" }}>{t("costs.dent.caveat")}</p>
    </section>
  );
}
