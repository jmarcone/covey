import { useCallback, useSyncExternalStore } from "react";
import { useSearchParams } from "react-router";
import type { Source } from "./costSources";

/* The Basis switch (cost home PR 9): which money the cost views lead with. One choice in the filter bar, in the address
 * as ?basis= (token, the default, is left out), read by every view through basisSupport(): a block either honours the
 * basis, keeps the token price and says why (locked), or has no figure at all and names the reason (none). No block
 * decides on its own, and none shows an allocation the data does not carry.
 *
 * - token: the work at token prices (Claude Code's own where reported, else covey's list).
 * - capacity: plan capacity used, an estimate: meter points moved × the plan's price per point, account-wide.
 * - billed: what Anthropic billed the pay-per-token API keys (whole keys, not seats).
 * - cash (the plan's price pro-rated over the metered span) awaits the human's sign-off: CASH_BASIS_ENABLED. */

export type Basis = "token" | "capacity" | "billed";
export const BASES: readonly Basis[] = ["token", "capacity", "billed"];
/** Cash is shown as an option, disabled, until the human signs off the figure (cost home proposal, open question 1). */
export const CASH_BASIS_ENABLED = false;

/** Why a block keeps the token price under another basis. */
export type LockReason =
  | "notPerRun" // capacity is a meter's, not a run's: no run carries one
  | "noCredentialOnRun" // a run carries no credential, so no bill
  | "notPerModel" // capacity is not split by model
  | "billedPerKey" // bills are per API key, not per agent or model
  | "answerIsTokenOverCapacity" // the answer is always token price ÷ capacity
  | "perPeriodAgent" // per period, capacity and bills are per credential, not per agent
  | "meterPerSeat" // the meter card shows plan capacity per seat; bills are per API key
  | "outsideTokenOnly" // capacity: the outside page's tiles and chart are at token price; capacity per source is in its chips
  | "outsideSeeSources" // capacity: the details' outside section is at token price; capacity per source is on the Outside page
  | "outsideNoBill"; // billed: outside sources are not linked to an API key, so their money stays at token price

/** Why a figure is absent (never $0.00). */
export type NoFigureReason =
  | "pending" | "beforeMeter" | "afterMeter" | "notSplit" | "unpricedExcluded" | "notAtThisLevel" | "usageBasis"
  | "accountWide" | "noReported" | "noData" | "bucketTooFine" | "noBilledKeys"
  | "loadError" // the bill could not be fetched
  | "queryFailed" // the figure's own request failed (not the bill's)
  | "notLanded" // keys are mapped, but no bill has landed yet (or the sync failed or is not configured)
  | "noPlanPrice"; // the seat's meter readings have no plan price

export type BasisSupport =
  | { state: "honoured" }
  | { state: "locked"; reason: LockReason }
  | { state: "none"; reason: NoFigureReason };

export type CostViewId = "home" | "models" | "work" | "details" | "outside";
export type BlockId =
  | "hero" | "parts" | "coverage" | "doorDetails" | "doorRun" // home
  | "table" | "tiles" // models, work
  | "priceList" | "runs" // work, details
  | "kpis" | "chart" | "insightsModels" | "insightsMeter" | "outsideOverview" // details
  | "sources" | "perModel" | "overview"; // outside

const HONOURED: BasisSupport = { state: "honoured" };
const lock = (reason: LockReason): BasisSupport => ({ state: "locked", reason });
const none = (reason: NoFigureReason): BasisSupport => ({ state: "none", reason });

/** The contract: what a block does with the basis. Token is honoured everywhere. bucket: the page's period width
 *  (capacity and bills come per hour at the finest); whoSelected: agents are selected (or the per-agent layout). */
export function basisSupport(view: CostViewId, block: BlockId, basis: Basis, ctx: { bucket?: string; whoSelected?: boolean } = {}): BasisSupport {
  if (basis === "token") return HONOURED;
  const capacity = basis === "capacity";
  switch (view) {
    case "home":
      // The hero is token ÷ capacity: capacity is half of it, so it is honoured; under billed it stays the equation.
      if (block === "hero") return capacity ? HONOURED : lock("answerIsTokenOverCapacity");
      if (block === "parts" || block === "coverage") return capacity ? HONOURED : none("notAtThisLevel");
      // The details door keeps the work at token price under billed (bills are per key) and says so.
      if (block === "doorDetails") return capacity ? HONOURED : lock("billedPerKey");
      if (block === "doorRun") return lock(capacity ? "notPerRun" : "noCredentialOnRun");
      break;
    case "models":
      return lock(capacity ? "notPerModel" : "billedPerKey");
    case "work":
      return lock(capacity ? "notPerRun" : "noCredentialOnRun");
    case "details":
      if (block === "runs" || block === "priceList") return lock(capacity ? "notPerRun" : "noCredentialOnRun");
      if (block === "insightsModels") return lock(capacity ? "notPerModel" : "billedPerKey");
      if (block === "insightsMeter") return capacity ? HONOURED : lock("meterPerSeat");
      // The outside sessions' section: its money is at token price (capacity per source is on the Outside page).
      if (block === "outsideOverview") return lock(capacity ? "outsideSeeSources" : "outsideNoBill");
      if (block === "kpis" || block === "chart") {
        if (ctx.whoSelected) return lock("perPeriodAgent");
        if (ctx.bucket === "10min") return none("bucketTooFine");
        return HONOURED;
      }
      break;
    case "outside":
      if (block === "sources") return capacity ? HONOURED : none("notAtThisLevel");
      if (block === "perModel") return capacity ? lock("notPerModel") : none("usageBasis");
      if (block === "overview") return lock(capacity ? "outsideTokenOnly" : "outsideNoBill");
      break;
  }
  return HONOURED;
}

/** The chart's source that carries a basis: the lead series under it. */
export const BASIS_SOURCE: Record<Basis, Source> = { token: "reported", capacity: "real", billed: "billed" };

export const BASIS_INFO: Record<Basis | "cash", { colorVar: string; estimate: boolean }> = {
  token: { colorVar: "var(--cost-reported)", estimate: false },
  capacity: { colorVar: "var(--cost-real)", estimate: true },
  billed: { colorVar: "var(--cost-billed)", estimate: false },
  cash: { colorVar: "var(--text-muted)", estimate: true },
};

export type BasisPruned = null | "cash" | "unknown";

/** The page's basis from the address: token when absent; cash (until signed off) and unknown values read as token and
 *  say so (pruned). The setter writes with replace and leaves token out of the address. */
export function useBasis(): { basis: Basis; pruned: BasisPruned; setBasis: (b: Basis) => void } {
  const [params, setParams] = useSearchParams();
  const raw = params.get("basis");
  const known = raw != null && (BASES as readonly string[]).includes(raw);
  const basis: Basis = known ? (raw as Basis) : "token";
  const pruned: BasisPruned = raw == null || known ? null : raw === "cash" ? "cash" : "unknown";
  const setBasis = useCallback((b: Basis) => setParams((prev) => {
    const p = new URLSearchParams(prev);
    if (b === "token") p.delete("basis"); else p.set("basis", b);
    return p;
  }, { replace: true }), [setParams]);
  return { basis, pruned, setBasis };
}

/** True from 1600px wide: the basis switch is a segmented row there, a select below. */
export function useWide(query = "(min-width: 1600px)"): boolean {
  return useSyncExternalStore(
    (cb) => {
      const m = window.matchMedia?.(query);
      m?.addEventListener?.("change", cb);
      return () => m?.removeEventListener?.("change", cb);
    },
    () => window.matchMedia?.(query).matches ?? false,
    () => false,
  );
}
