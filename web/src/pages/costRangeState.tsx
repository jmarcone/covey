import { lazy, useEffect, useId, useMemo, useRef, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { useSearchParams } from "react-router";
import { setDisplayTz } from "../format";
import { bucketFits, useCostWindow } from "./costView";
import { bucketForSpan, customWindow, isCustom, readRange, windowQuery, writeRange, type CostWindow, type RangeCode } from "./costRange";
import { bucketOptions, rangeOptions } from "./outsideFigures";
import { Seg } from "./Seg";
import { BasisControl } from "./BasisControl";
import { useUsageReadings } from "./StackedCostChart";
import { CostBarWait, CostBundle, CostLoading, loadState } from "./CostLoading";

// The custom range's panel carries the calendar (react-day-picker and date-fns): a chunk of its own, loaded when the
// panel opens, so a cost page on a quick range does not pay for it.
const RangePanel = lazy(() => import("./RangePanel").then((m) => ({ default: m.RangePanel })));

/* The cost pages' window, shared by the home (/costs) and the details page
 * (/costs/details) so both read one range the same way (cost home PR 5).
 * A quick range and its bucket are in the address (PR 2, useCostWindow); a
 * custom range is ?from=&to= (RFC 3339, [from, to)) with ?tz= (#193), so a
 * segment can be shared, reloaded and walked back to. */

export function useCostRange() {
  const [params, setParams] = useSearchParams();
  const { days, bucket: quickBucket, setBucket: setQuickBucket } = useCostWindow();
  const range = useMemo(() => readRange(params, Date.now()), [params]);
  const tz = range.tz;
  // One clock for every label of the page (the tiles, the axis, the tables).
  // Set in the render, for this pass's labels, and again in the effect: under
  // StrictMode the simulated unmount runs the cleanup after the first commit,
  // and a child that re-renders on its own query would read UTC from then on.
  setDisplayTz(tz);
  useEffect(() => {
    setDisplayTz(tz);
    return () => setDisplayTz("utc");
  }, [tz]);
  const customOk = range.from != null && range.to != null && range.error == null;
  // from/to given but not usable: no query is asked, the panel says why.
  const badRange = range.error != null;
  // The fields show what the address says whenever both values parse, a refused
  // range too, so the error points at them; only an unparseable one falls back
  // to the panel's default.
  const parsed = Number.isFinite(range.from) && Number.isFinite(range.to);
  const shownFrom = parsed ? range.from : null;
  const shownTo = parsed ? range.to : null;
  const win: CostWindow = customOk ? customWindow(range.from!, range.to!) : { days };
  const custom = isCustom(win);
  const [panelOpen, setPanelOpen] = useState(false);
  // The panel follows the address: once it holds no custom range (Back from an applied one), the control shows the
  // quick range the page really reads, not a stale "Custom" (#200 merge review).
  useEffect(() => {
    if (range.from == null && range.to == null) setPanelOpen(false);
  }, [range.from, range.to]);
  const wq = windowQuery(win);
  // A custom range's bucket: the one the address names where it fits the span, else the span's own.
  const spanDays = custom ? Math.max(1, Math.ceil((range.to! - range.from!) / 86_400_000)) : days;
  const want = params.get("bucket");
  const bucket = custom ? (want != null && bucketFits(spanDays, want) ? want : bucketForSpan(range.to! - range.from!)) : quickBucket;
  const setBucket = (b: string) => (custom
    ? setParams((prev) => { const p = new URLSearchParams(prev); p.set("bucket", b); return p; }, { replace: true })
    : setQuickBucket(b));
  // A change of range is a step in the history: Back returns to the one before. A quick range keeps a chosen bucket
  // where it fits (PR 2); a custom range starts at its span's own.
  const setRange = (v: number | "custom") => {
    if (v === "custom") {
      setPanelOpen(true);
      return;
    }
    setPanelOpen(false);
    setParams((prev) => {
      const p = writeRange(prev, { days: v }, tz);
      const b = p.get("bucket");
      if (b != null && !bucketFits(v, b)) p.delete("bucket");
      return p;
    });
  };
  // A custom range starts at its span's own bucket; "freeze at now" (keepBucket) keeps the one the reader reads where
  // it still fits the frozen span — the same period, now fixed, drawn the same way.
  const applyCustom = (from: number, to: number, keepBucket = false) => {
    setPanelOpen(false); // the custom range keeps the panel shown while it is in force
    setParams((prev) => {
      const w = customWindow(from, to);
      const p = writeRange(prev, w, tz);
      if (keepBucket && bucketFits(w.days, bucket)) p.set("bucket", bucket);
      else p.delete("bucket");
      return p;
    });
  };
  const freezeCustom = (from: number, to: number) => applyCustom(from, to, true);
  const setTz = (next: typeof tz) => setParams((prev) => {
    const n = new URLSearchParams(prev);
    n.set("tz", next);
    return n;
  }, { replace: true });
  return { params, setParams, days, range, tz, badRange, shownFrom, shownTo, win, custom, panelOpen, wq, bucket,
    setBucket, setRange, applyCustom, freezeCustom, setTz };
}

export type CostRange = ReturnType<typeof useCostRange>;

/** The range and bucket switches, and the custom range's panel when it is open or in force. rangeError: the page's
 *  refusal (the address's own, or the server's code), said in the panel. */
export function CostRangeControls({ r, rangeError, bucketSwitch = true, basisSwitch = false, leading, trailing, waiting = false, part = "both" }: {
  r: CostRange; rangeError: RangeCode | "unknown" | null;
  /** The bucket matters only where a chart draws periods: the home leaves it out. */
  bucketSwitch?: boolean;
  /** The Basis switch (cost home PR 9): on in the filter bar of every cost view. */
  basisSwitch?: boolean;
  /** Controls at the start of the row (the filter bar's Who), and at its end (Share). */
  leading?: ReactNode;
  trailing?: ReactNode;
  /** Whether the view waits for figures: said in the row's free room, where it stays in sight. */
  waiting?: boolean;
  /** The switches' row, the custom range's panel, or both: the filter bar keeps only the row sticky. */
  part?: "row" | "panel" | "both";
}) {
  const { t } = useTranslation();
  // The meter's readings of the last days, for the picker's helpers (since the last reset, between two readings).
  const helperReadings = useUsageReadings({ days: 8 }, part !== "row" && (r.panelOpen || r.custom || r.badRange));
  return (
    <>
      {part !== "panel" && <div className="flex flex-wrap items-center gap-3 mb-4">
        {leading}
        {basisSwitch && <BasisControl />}
        <CostBarWait waiting={waiting} />
        <Seg<number | "custom"> value={r.custom || r.panelOpen || r.badRange ? "custom" : r.days} onChange={r.setRange}
          options={[...rangeOptions(t), { v: "custom", label: t("costs.range.custom") }]} />
        {bucketSwitch && <Seg value={r.bucket} onChange={r.setBucket} options={bucketOptions(t, r.win.days)} />}
        {trailing}
      </div>}
      {part !== "row" && (r.custom || r.panelOpen || r.badRange) && (
        // Re-made when the range changes from outside (Back, a pasted link), so the fields hold what the address says.
        <CostBundle fallback={<div className="card mb-4 cost-range-loading"><CostLoading /></div>}>
          <RangePanel key={`${r.range.from}-${r.range.to}-${r.tz}`} from={r.shownFrom} to={r.shownTo}
            tz={r.tz} onTz={r.setTz} onApply={(f, t) => r.applyCustom(f, t)} onFreeze={r.freezeCustom} error={rangeError} readings={helperReadings.data ?? []}
            readingsLoad={loadState(helperReadings)}
            quickDays={r.custom || r.badRange ? null : r.days} />
        </CostBundle>
      )}
    </>
  );
}

/** Copies the page's address: it holds the whole view (PR 2), so the link opens what the sender sees. The clipboard
 *  can refuse (an older app view, a denied permission): then it says where to copy from instead. */
export function ShareButton() {
  const { t } = useTranslation();
  const [said, setSaid] = useState<"" | "copied" | "failed">("");
  const timer = useRef<number | undefined>(undefined);
  useEffect(() => () => window.clearTimeout(timer.current), []);
  const share = async () => {
    window.clearTimeout(timer.current);
    try {
      await navigator.clipboard.writeText(window.location.href);
      setSaid("copied");
    } catch {
      setSaid("failed");
    }
    timer.current = window.setTimeout(() => setSaid(""), 4000);
  };
  return (
    <span className="cost-share">
      <button type="button" className="cost-share-btn" onClick={share} data-share>{t("costs.bar.share")}</button>
      <span className="muted text-xs" aria-live="polite" data-share-said>{said === "copied" ? t("costs.bar.copied") : said === "failed" ? t("costs.bar.copyFailed") : ""}</span>
    </span>
  );
}

/** Who the page's figures are for: everyone, or a selection with a one-click clear. locked: the page answers for the
 *  whole account and does not apply the selection (the home's answer), said in so many words, never silently. */
export function WhoSummary({ names, count = names.length, onClear, locked = false }: {
  names: string[];
  /** How many are selected: more than names while the agents' names are not loaded (a new range, a refused one). */
  count?: number;
  onClear?: () => void; locked?: boolean;
}) {
  const { t } = useTranslation();
  const why = `${useId().replace(/[^a-zA-Z0-9_-]/g, "")}-why`;
  if (locked) {
    return (
      <span className="cost-who locked" data-who-locked tabIndex={0} aria-describedby={why} title={t("costs.bar.whoLockedTitle")}>
        {t("costs.bar.whoLocked")}<span id={why} className="sr-only">{t("costs.bar.whoLockedTitle")}</span>
      </span>
    );
  }
  return (
    <span className="cost-who" data-who>
      <span className="muted">{t("costs.bar.who")}</span>{" "}
      <b>{count === 0 ? t("costs.bar.whoAll") : names.length === count && count <= 2 ? names.join(", ") : t("costs.bar.whoSome", { count })}</b>
      {count > 0 && onClear && <button type="button" className="cost-who-clear" onClick={onClear} aria-label={t("costs.bar.clear")} data-who-clear>×</button>}
    </span>
  );
}

/** The cost pages' filter bar (cost home PR 6): sticky at the top while the page scrolls, every control that changes
 *  what the figures cover in one place. */
export function CostFilterBar(props: Parameters<typeof CostRangeControls>[0]) {
  const { t } = useTranslation();
  // Only the row sticks: the custom range's panel stays open while a custom range is in force, and pinned it would
  // cover a phone's whole screen (#218 review). It follows the bar in the page instead.
  return (
    <>
      <div className="cost-filterbar" role="region" aria-label={t("costs.bar.label")} data-filterbar>
        <CostRangeControls basisSwitch {...props} part="row" />
      </div>
      <CostRangeControls {...props} part="panel" />
    </>
  );
}
