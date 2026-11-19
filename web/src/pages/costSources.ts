import type { TFunction } from "i18next";
import type { ModelUsageRow } from "../api";

/* The four money sources of the Costs page, defined once: the chart's chips,
 * its colour key and the per-model table's columns all read them from here,
 * so an id, its name, its hint and its colour cannot drift apart. */

export type Source = "reported" | "recomputed" | "real" | "billed";

/** The sources in the order the page shows them. */
export const SOURCES: readonly Source[] = ["reported", "recomputed", "real", "billed"];

/** What a source can be broken down by. A per-model source has a figure for
 *  each model line; a per-credential one (the meter's real cost, the bill)
 *  only for a credential, so the per-model table shows it in a row of its
 *  own and never beside a model. */
export type Granularity = "model" | "credential";

type SourceInfo = {
  granularity: Granularity;
  /** On when nothing is remembered: the table as it was before the others. */
  defaultOn: boolean;
  /** For a per-credential source, the label of its row under the total. */
  rowLabel?: string;
  /** Shown as "≈" with a range, and hatched wherever it has a swatch: the
   *  meter's real cost is an estimate, a bill is not. */
  estimate?: boolean;
};

export const SOURCE_INFO: Record<Source, SourceInfo> = {
  reported: { granularity: "model", defaultOn: true },
  recomputed: { granularity: "model", defaultOn: true },
  real: { granularity: "credential", defaultOn: false, rowLabel: "costs.insights.seatsRow", estimate: true },
  billed: { granularity: "credential", defaultOn: false, rowLabel: "costs.insights.keysRow" },
};

export const DEFAULT_SOURCES: readonly Source[] = SOURCES.filter((s) => SOURCE_INFO[s].defaultOn);

export const isPerModel = (s: Source): boolean => SOURCE_INFO[s].granularity === "model";

/** A per-model source's figure for one model line; null where it has none
 *  (a model the list does not price has no recomputed figure). */
export function modelFigure(s: Source, r: ModelUsageRow): number | null {
  if (s === "reported") return r.engine_usd;
  if (s === "recomputed") return r.list_usd;
  return null;
}

/** One colour per source, everywhere on the page (styles.css). */
export const SOURCE_COLOR: Record<Source, string> = {
  reported: "var(--cost-reported)",
  recomputed: "var(--cost-recomputed)",
  real: "var(--cost-real)",
  billed: "var(--cost-billed)",
};

/** The parts of a meter's real cost once sessions outside covey report on it
 *  (#120): covey's agents, the outside sessions, the unattributed movement
 *  neither explains (F3c, booked to nobody), and movement in a reading the
 *  outside sessions did not report, split to nobody — drawn plain with a
 *  dashed edge (unsplitEdge) rather than hatched, so it tells from covey's
 *  part by its pattern too. */
export const SPLIT_COLOR = {
  covey: "var(--cost-real)",
  outside: "var(--cost-outside)",
  unattributed: "var(--cost-unattributed)",
  unsplit: "var(--cost-unsplit)",
  unsplitEdge: "var(--cost-unsplit-edge)",
} as const;

/** The source's full name, as the chips carry it. */
export function sourceLabel(t: TFunction, s: Source): string {
  return t(s === "real" ? "costs.fig.realEstimate" : `costs.fig.${s}`);
}

/** What the source is and what it is not. */
export function sourceHint(t: TFunction, s: Source): string {
  return t(s === "reported" ? "costs.filter.reportedHint" : s === "recomputed" ? "costs.filter.measuredHint"
    : s === "real" ? "costs.filter.subscriptionHint" : "costs.filter.billedHint");
}
