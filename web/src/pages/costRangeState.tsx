import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useSearchParams } from "react-router";
import { setDisplayTz } from "../format";
import { bucketFits, useCostWindow } from "./costView";
import { bucketForSpan, customWindow, isCustom, readRange, windowQuery, writeRange, type CostWindow, type RangeCode } from "./costRange";
import { bucketOptions, rangeOptions } from "./outsideFigures";
import { RangePanel } from "./RangePanel";
import { Seg } from "./Seg";
import { useUsageReadings } from "./StackedCostChart";

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
  const applyCustom = (from: number, to: number) => {
    setPanelOpen(false); // the custom range keeps the panel shown while it is in force
    setParams((prev) => {
      const p = writeRange(prev, customWindow(from, to), tz);
      p.delete("bucket");
      return p;
    });
  };
  const setTz = (next: typeof tz) => setParams((prev) => {
    const n = new URLSearchParams(prev);
    n.set("tz", next);
    return n;
  }, { replace: true });
  return { params, setParams, days, range, tz, badRange, shownFrom, shownTo, win, custom, panelOpen, wq, bucket,
    setBucket, setRange, applyCustom, setTz };
}

export type CostRange = ReturnType<typeof useCostRange>;

/** The range and bucket switches, and the custom range's panel when it is open or in force. rangeError: the page's
 *  refusal (the address's own, or the server's code), said in the panel. */
export function CostRangeControls({ r, rangeError, bucketSwitch = true }: {
  r: CostRange; rangeError: RangeCode | "unknown" | null;
  /** The bucket matters only where a chart draws periods: the home leaves it out. */
  bucketSwitch?: boolean;
}) {
  const { t } = useTranslation();
  // The meter's readings of the last days, for the picker's helpers (since the last reset, between two readings).
  const helperReadings = useUsageReadings({ days: 8 }, r.panelOpen || r.custom || r.badRange);
  return (
    <>
      <div className="flex flex-wrap items-center gap-3 mb-4">
        <div style={{ flex: 1 }} />
        <Seg<number | "custom"> value={r.custom || r.panelOpen || r.badRange ? "custom" : r.days} onChange={r.setRange}
          options={[...rangeOptions(t), { v: "custom", label: t("costs.range.custom") }]} />
        {bucketSwitch && <Seg value={r.bucket} onChange={r.setBucket} options={bucketOptions(t, r.win.days)} />}
      </div>
      {(r.custom || r.panelOpen || r.badRange) && (
        // Re-made when the range changes from outside (Back, a pasted link), so the fields hold what the address says.
        <RangePanel key={`${r.range.from}-${r.range.to}-${r.tz}`} from={r.shownFrom} to={r.shownTo}
          tz={r.tz} onTz={r.setTz} onApply={r.applyCustom} error={rangeError} readings={helperReadings.data ?? []} />
      )}
    </>
  );
}
