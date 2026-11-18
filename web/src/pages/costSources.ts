import type { TFunction } from "i18next";

/* The four money sources of the Costs page, defined once: the chart's chips,
 * its colour key and the per-model table's columns all read them from here,
 * so an id, its name, its hint and its colour cannot drift apart. */

export type Source = "reported" | "recomputed" | "real" | "billed";

/** The sources in the order the page shows them. */
export const SOURCES: readonly Source[] = ["reported", "recomputed", "real", "billed"];

/** One colour per source, everywhere on the page (styles.css). */
export const SOURCE_COLOR: Record<Source, string> = {
  reported: "var(--cost-reported)",
  recomputed: "var(--cost-recomputed)",
  real: "var(--cost-real)",
  billed: "var(--cost-billed)",
};

/** The source's full name, as the chips carry it. */
export function sourceLabel(t: TFunction, s: Source): string {
  return t(s === "real" ? "costs.fig.realEstimate" : `costs.fig.${s}`);
}

/** What the source is and what it is not. */
export function sourceHint(t: TFunction, s: Source): string {
  return t(s === "reported" ? "costs.filter.reportedHint" : s === "recomputed" ? "costs.filter.measuredHint"
    : s === "real" ? "costs.filter.subscriptionHint" : "costs.filter.billedHint");
}
