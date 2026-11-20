import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { fmtBucket, fmtNumber, type DisplayTz } from "../format";
import type { UsageReading } from "../api";
import {
  MAX_LAST_MINUTES,
  betweenReadings,
  fromInputValue,
  lastMinutesRange,
  lastResetAt,
  readingChoices,
  todayRange,
  toInputValue,
  validRange,
  type RangeCode,
} from "./costRange";
import { Seg } from "./Seg";

/* The custom range of the Costs page: a start and an end to the minute, the
 * zone they are read in, and four ways to fill them in (today, since the last
 * reset, the last N minutes, between two meter readings). A native
 * datetime-local input carries the keyboard and screen-reader behaviour.
 *
 * It only ever hands up a range that passed validRange; a refusal stays here
 * as an inline error in the reader's language (the code, never the server's
 * text). The range itself lives in the address (?from=&to=&tz=), which
 * Costs owns. */

// The height of a field in the first row: the zone switch and Apply sit in a
// box this tall, so the three share their middle and the row ends level.
const INPUT_H = 34;

const iso = (ms: number) => new Date(ms).toISOString();

export function RangePanel({ from, to, tz, onTz, onApply, error: outer, readings, now = Date.now }: {
  /** The range the fields start from, as instants: the one in force, or the one a refused address names; null when there is none. */
  from: number | null;
  to: number | null;
  tz: DisplayTz;
  onTz: (tz: DisplayTz) => void;
  /** A range that is valid. */
  onApply: (from: number, to: number) => void;
  /** Why the range in the address, or the server's answer to it, is refused. */
  error?: RangeCode | "unknown" | null;
  /** The weekly meter's readings of the last days, for the helpers. */
  readings: readonly UsageReading[];
  now?: () => number;
}) {
  const { t } = useTranslation();
  const uid = useId().replace(/[^a-zA-Z0-9_-]/g, "");
  const start = now();
  const [fromText, setFromText] = useState(() => toInputValue(from ?? lastMinutesRange(start, 60)[0], tz));
  const [toText, setToText] = useState(() => toInputValue(to ?? lastMinutesRange(start, 60)[1], tz));
  const [inner, setInner] = useState<RangeCode | null>(null);
  const [minutes, setMinutes] = useState("45");
  const choices = readingChoices(readings);
  const [pickA, setPickA] = useState("");
  const [pickB, setPickB] = useState("");
  const reset = lastResetAt(readings, start);

  // A refusal from here is shown first; then the one the address or the
  // server gave, until the reader applies something.
  const error = inner ?? outer ?? null;
  const errorText = error == null ? null : error === "unknown" ? t("costs.range.errUnknown") : t(`costs.range.err.${error}`);

  const take = (f: number, e: number) => {
    setFromText(toInputValue(f, tz));
    setToText(toInputValue(e, tz));
    const code = validRange(f, e, now());
    setInner(code);
    if (!code) onApply(f, e);
  };
  const submit = (ev: React.FormEvent) => {
    ev.preventDefault();
    take(fromInputValue(fromText, tz), fromInputValue(toText, tz));
  };
  // The zone only changes how the two fields read: what they hold stays the
  // same instants.
  const switchTz = (next: DisplayTz) => {
    if (next === tz) return;
    const f = fromInputValue(fromText, tz), e = fromInputValue(toText, tz);
    if (Number.isFinite(f)) setFromText(toInputValue(f, next));
    if (Number.isFinite(e)) setToText(toInputValue(e, next));
    onTz(next);
  };
  const n = Number(minutes);
  const minutesOk = /^\d+$/.test(minutes.trim()) && n >= 1 && n <= MAX_LAST_MINUTES;
  const a = choices.find((c) => c.id === (pickA || choices[1]?.id));
  const b = choices.find((c) => c.id === (pickB || choices[0]?.id));
  const label = (c: (typeof choices)[number]) => t("costs.range.reading", { at: fmtBucket(iso(c.at), "10min"), pct: fmtNumber(c.percent, 0) });
  const errId = `${uid}-err`;

  return (
    <form className="card mb-4" onSubmit={submit} aria-label={t("costs.range.title")} data-range-panel noValidate>
      <div style={{ fontWeight: 600, marginBottom: 8 }}>{t("costs.range.title")}</div>
      <div className="flex flex-wrap items-end gap-3">
        <label className="text-xs" htmlFor={`${uid}-from`} style={{ flex: "1 1 190px", display: "flex", flexDirection: "column", margin: 0 }}>
          <span className="block mb-1">{t("costs.range.from")}</span>
          <input id={`${uid}-from`} type="datetime-local" step={60} required value={fromText}
            aria-invalid={error != null} aria-describedby={error ? errId : undefined} onChange={(e) => setFromText(e.target.value)} />
        </label>
        <label className="text-xs" htmlFor={`${uid}-to`} style={{ flex: "1 1 190px", display: "flex", flexDirection: "column", margin: 0 }}>
          <span className="block mb-1">{t("costs.range.to")}</span>
          <input id={`${uid}-to`} type="datetime-local" step={60} required value={toText}
            aria-invalid={error != null} aria-describedby={error ? errId : undefined} onChange={(e) => setToText(e.target.value)} />
        </label>
        <div role="group" aria-labelledby={`${uid}-tz`} className="text-xs" style={{ display: "flex", flexDirection: "column", margin: 0 }}>
          <span className="block mb-1" id={`${uid}-tz`}>{t("costs.range.tz")}</span>
          <div style={{ display: "flex", alignItems: "center", minHeight: INPUT_H }}>
            <Seg value={tz} onChange={switchTz} options={[{ v: "utc", label: t("costs.range.tzUtc") }, { v: "local", label: t("costs.range.tzLocal") }]} />
          </div>
        </div>
        <div style={{ display: "flex", alignItems: "center", minHeight: INPUT_H }}>
          <button type="submit" className="btn sm primary">{t("costs.range.apply")}</button>
        </div>
      </div>
      {errorText && <p className="danger-text text-xs mt-2 mb-0" role="alert" id={errId} data-range-error={error}>{errorText}</p>}

      <div className="text-xs muted mt-3 mb-1">{t("costs.range.picks")}</div>
      <div className="flex flex-wrap items-center gap-2">
        <button type="button" className="btn sm" onClick={() => { const [f, e] = todayRange(now(), tz); take(f, e); }}>{t("costs.range.today")}</button>
        <button type="button" className="btn sm" disabled={reset == null}
          aria-describedby={reset == null ? `${uid}-noreset` : undefined}
          onClick={() => { if (reset != null) take(reset, Math.ceil(now() / 60_000) * 60_000); }}>{t("costs.range.sinceReset")}</button>
        {reset == null && <span className="muted text-xs" id={`${uid}-noreset`}>{t("costs.range.sinceResetNone")}</span>}
      </div>
      <div className="flex flex-wrap items-center gap-2 mt-2">
        <label className="text-xs" htmlFor={`${uid}-min`} style={{ display: "inline-flex", alignItems: "center", gap: 8 }}>
          {t("costs.range.last")}
          <input id={`${uid}-min`} type="number" inputMode="numeric" min={1} max={MAX_LAST_MINUTES} step={1} value={minutes}
            style={{ width: 80 }} onChange={(e) => setMinutes(e.target.value)} />
          {t("costs.range.minutes")}
        </label>
        <button type="button" className="btn sm" disabled={!minutesOk}
          onClick={() => { const [f, e] = lastMinutesRange(now(), n); take(f, e); }}>{t("costs.range.show")}</button>
      </div>
      <fieldset className="mt-2" style={{ border: 0, padding: 0, margin: "8px 0 0" }}>
        <legend className="text-xs muted" style={{ padding: 0 }}>{t("costs.range.between")}</legend>
        {choices.length < 2 ? <p className="muted text-xs mt-1 mb-0">{t("costs.range.noReadings")}</p> : (
          <div className="flex flex-wrap items-end gap-2 mt-1">
            {/* The two selects share their row (or each take one) at equal width; the button wraps on its own. */}
            <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(190px, 1fr))", gap: 8, flex: "1 1 400px" }}>
              <label className="text-xs" htmlFor={`${uid}-ra`}>
                <span className="block mb-1">{t("costs.range.readingFrom")}</span>
                <select id={`${uid}-ra`} value={a?.id ?? ""} onChange={(e) => setPickA(e.target.value)}>
                  {choices.map((c) => <option key={c.id} value={c.id}>{label(c)}</option>)}
                </select>
              </label>
              <label className="text-xs" htmlFor={`${uid}-rb`}>
                <span className="block mb-1">{t("costs.range.readingTo")}</span>
                <select id={`${uid}-rb`} value={b?.id ?? ""} onChange={(e) => setPickB(e.target.value)}>
                  {choices.map((c) => <option key={c.id} value={c.id}>{label(c)}</option>)}
                </select>
              </label>
            </div>
            <button type="button" className="btn sm" disabled={!a || !b}
              onClick={() => { if (a && b) { const [f, e] = betweenReadings(a.at, b.at); take(f, e); } }}>{t("costs.range.useReadings")}</button>
          </div>
        )}
      </fieldset>
      {from != null && to != null && error == null && (
        <p className="muted text-xs mt-3 mb-0" data-range-window>
          {t("costs.range.window", { from: fmtBucket(iso(from), "10min"), to: fmtBucket(iso(to), "10min") })}
        </p>
      )}
      {tz === "local" && <p className="muted text-xs mt-1 mb-0">{t("costs.range.dayUtc")}</p>}
    </form>
  );
}
