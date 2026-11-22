import { useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { DayPicker } from "react-day-picker";
import type { DayPickerLocale } from "react-day-picker/locale";
import "react-day-picker/style.css";
import { fmtBucket, fmtNumber, type DisplayTz } from "../format";
import { uiLocale } from "../i18n";
import type { Lang } from "../langs";
import type { UsageReading } from "../api";
import {
  MAX_LAST_MINUTES,
  RANGE_PRESETS,
  betweenReadings,
  canFreeze,
  cutToNow,
  dayKeyOf,
  dayStart,
  dayStartValue,
  freezeRange,
  fromInputValue,
  joinEnd,
  lastMinutesRange,
  lastResetAt,
  presetRange,
  readingChoices,
  splitEnd,
  splitStart,
  toInputValue,
  validRange,
  type DayKey,
  type RangeCode,
} from "./costRange";
import { Seg } from "./Seg";

/* The custom range of the Costs page: a start and an end to the minute, the
 * zone they are read in, and the ways to fill them in — presets that apply at
 * once, a calendar of days with a start and an end time, the exact
 * datetime-local fields (the keyboard's and the screen reader's way in, kept
 * in step with the calendar both ways), the last N minutes, and two meter
 * readings.
 *
 * Days are the zone's: in UTC the calendar, the presets and the times are
 * UTC's whatever the browser's clock says. A range of days is [start of the
 * first, start of the day after the last); an end time on the last day ends
 * it at that minute. Calendar clicks only fill the fields; Apply hands the
 * range up.
 *
 * It only ever hands up a range that passed validRange; a refusal stays here
 * as an inline error in the reader's language (the code, never the server's
 * text). The range itself lives in the address (?from=&to=&tz=), which
 * Costs owns. */

const iso = (ms: number) => new Date(ms).toISOString();

/** The calendar's month and weekday names follow the app's language. The week starts on Monday in every language:
 *  the cost pages' week buckets and the week presets start on Monday (fmtBucket), and a calendar that drew Sunday
 *  first would draw another week than the chart beside it. English is the British cut, as the app's flag says.
 *  Each is a chunk of its own, loaded for the language in use only: the ten together weigh more than the calendar. */
const LOCALES: Record<Lang, () => Promise<DayPickerLocale>> = {
  de: () => import("react-day-picker/locale/de").then((m) => m.de),
  en: () => import("react-day-picker/locale/en-GB").then((m) => m.enGB),
  es: () => import("react-day-picker/locale/es").then((m) => m.es),
  fr: () => import("react-day-picker/locale/fr").then((m) => m.fr),
  it: () => import("react-day-picker/locale/it").then((m) => m.it),
  ja: () => import("react-day-picker/locale/ja").then((m) => m.ja),
  nl: () => import("react-day-picker/locale/nl").then((m) => m.nl),
  pl: () => import("react-day-picker/locale/pl").then((m) => m.pl),
  pt: () => import("react-day-picker/locale/pt").then((m) => m.pt),
  zh: () => import("react-day-picker/locale/zh-CN").then((m) => m.zhCN),
};
const loaded = new Map<Lang, DayPickerLocale>();

/** The calendar locale of the app's language; null while its chunk loads (the calendar waits, rather than flashing
 *  another language's names). */
function useCalendarLocale(language: string | undefined): DayPickerLocale | null {
  const raw = (language ?? "en").split("-")[0];
  const lang: Lang = raw in LOCALES ? (raw as Lang) : "en";
  const [, force] = useState(0);
  useEffect(() => {
    if (loaded.has(lang)) return;
    let live = true;
    LOCALES[lang]().then((l) => {
      loaded.set(lang, l);
      if (live) force((x) => x + 1);
    }).catch(() => {
      // A chunk that does not load (offline, a new deploy): the library's own English, rather than no calendar.
      loaded.set(lang, {} as DayPickerLocale);
      if (live) force((x) => x + 1);
    });
    return () => { live = false; };
  }, [lang]);
  return loaded.get(lang) ?? null;
}

/** The panel width from which the calendar shows two months side by side. */
const TWO_MONTHS_AT = 900;

function usePanelWidth() {
  const ref = useRef<HTMLFormElement>(null);
  const [w, setW] = useState(0);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    setW(el.getBoundingClientRect().width);
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver((entries) => setW(entries[0]?.contentRect.width ?? 0));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  return [ref, w] as const;
}

/** A day as an instant the calendar reads as that day in the zone (its noon, clear of any clock change). */
function dayDate(key: DayKey, tz: DisplayTz): Date | undefined {
  const s = dayStart(key, tz);
  return Number.isFinite(s) ? new Date(s + 12 * 3_600_000) : undefined;
}

/** The day the calendar hands back. In UTC it is a TZDate, whose getters read UTC; else a local Date. */
const keyOfPicked = (d: Date): DayKey =>
  `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;

/** A month (YYYY-MM) moved by some months. */
function shiftMonth(ym: string, by: number): string {
  const [y, m] = ym.split("-").map(Number);
  const d = new Date(Date.UTC(y, m - 1 + by, 1));
  return `${d.getUTCFullYear()}-${String(d.getUTCMonth() + 1).padStart(2, "0")}`;
}

export function RangePanel({ from, to, tz, onTz, onApply, onFreeze, error: outer, readings, quickDays = null, now = Date.now }: {
  /** The range the fields start from, as instants: the one in force, or the one a refused address names; null when there is none. */
  from: number | null;
  to: number | null;
  tz: DisplayTz;
  onTz: (tz: DisplayTz) => void;
  /** A range that is valid. */
  onApply: (from: number, to: number) => void;
  /** A valid range from "freeze at now": the page keeps the reader's bucket where it fits. onApply when not given. */
  onFreeze?: (from: number, to: number) => void;
  /** Why the range in the address, or the server's answer to it, is refused. */
  error?: RangeCode | "unknown" | null;
  /** The weekly meter's readings of the last days, for the helpers. */
  readings: readonly UsageReading[];
  /** The quick range the page reads while no custom range is in force (the last n days, moving with the clock):
   *  what "freeze at now" turns into a fixed range. Null when a custom range is in force. */
  quickDays?: number | null;
  now?: () => number;
}) {
  const { t, i18n } = useTranslation();
  const uid = useId().replace(/[^a-zA-Z0-9_-]/g, "");
  const start = now();
  const [fromText, setFromText] = useState(() => toInputValue(from ?? lastMinutesRange(start, 60)[0], tz));
  const [toText, setToText] = useState(() => toInputValue(to ?? lastMinutesRange(start, 60)[1], tz));
  // An end typed in the end-time field, with the field value it made: while the end field still holds that value, the
  // calendar and the time field show what was typed. 00:00 typed on a day is that day's first minute, not "the end of
  // the day before" (which is how an end at a day's start reads otherwise), so typing it neither clears the field
  // nor moves the calendar.
  const [typedEnd, setTypedEnd] = useState<{ text: string; day: DayKey; time: string } | null>(null);
  // Every other way to the end field forgets what was typed.
  const setEnd = (v: string) => { setToText(v); setTypedEnd(null); };
  const [inner, setInner] = useState<RangeCode | null>(null);
  // The first day of a range being picked in the calendar: the next click picks the last.
  const [pending, setPending] = useState<DayKey | null>(null);
  const [minutes, setMinutes] = useState("45");
  const choices = readingChoices(readings);
  const [pickA, setPickA] = useState("");
  const [pickB, setPickB] = useState("");
  const reset = lastResetAt(readings, start);
  const [formRef, width] = usePanelWidth();
  const months = width >= TWO_MONTHS_AT ? 2 : 1;

  // What the calendar and the time fields show is read from the two exact fields, so the two ways never disagree.
  const s = splitStart(fromText);
  const e = typedEnd != null && typedEnd.text === toText ? typedEnd : splitEnd(toText, s.day, tz);
  const today = dayKeyOf(start, tz);
  // The month shown last: the end's (or today's), until the reader pages.
  const [lastMonth, setLastMonth] = useState(() => (e.day && e.day <= today ? e.day : today).slice(0, 7));
  const firstMonth = shiftMonth(lastMonth, 1 - months);
  const showMonth = (day: DayKey) => {
    if (!day) return;
    const m = (day <= today ? day : today).slice(0, 7);
    if (m < firstMonth || m > lastMonth) setLastMonth(m);
  };

  // A refusal from here is shown first; then the one the address or the
  // server gave, until the reader applies something.
  const error = inner ?? outer ?? null;
  const errorText = error == null ? null : error === "unknown" ? t("costs.range.errUnknown") : t(`costs.range.err.${error}`);
  const errId = `${uid}-err`;
  const describedBy = error ? errId : undefined;

  const take = (f: number, end: number, apply: (from: number, to: number) => void = onApply) => {
    const n = now();
    const cut = cutToNow(f, end, n);
    setFromText(toInputValue(f, tz));
    setEnd(toInputValue(cut, tz));
    setPending(null);
    const code = validRange(f, cut, n);
    setInner(code);
    if (!code) apply(f, cut);
  };
  const submit = (ev: React.FormEvent) => {
    ev.preventDefault();
    take(fromInputValue(fromText, tz), fromInputValue(toText, tz));
  };
  // The zone only changes how the fields read: what they hold stays the
  // same instants.
  const switchTz = (next: DisplayTz) => {
    if (next === tz) return;
    const f = fromInputValue(fromText, tz), en = fromInputValue(toText, tz);
    if (Number.isFinite(f)) setFromText(toInputValue(f, next));
    if (Number.isFinite(en)) setEnd(toInputValue(en, next));
    onTz(next);
  };

  // A calendar click fills the fields with whole days: the first click the day, the second the last day (either
  // order). The times can then be set beside it. A day starts at its first minute that exists in the zone (00:00,
  // but 01:00 where a clock change skips midnight), read from the instant, never the literal "T00:00".
  const pickDay = (picked: Date | undefined) => {
    if (!picked) return;
    const d = keyOfPicked(picked);
    const [a, b] = pending == null ? [d, d] : pending <= d ? [pending, d] : [d, pending];
    setPending(pending == null ? d : null);
    setFromText(dayStartValue(a, tz) || `${a}T00:00`);
    setEnd(joinEnd(b, "", tz));
    setInner(null);
  };

  const fromMs = fromInputValue(fromText, tz), toMs = fromInputValue(toText, tz);
  const summary = Number.isFinite(fromMs) && Number.isFinite(toMs) && toMs > fromMs ? summarize(fromMs, toMs) : "";
  function summarize(f: number, en: number): string {
    const fmt = new Intl.DateTimeFormat(uiLocale(), {
      weekday: "short", day: "numeric", month: "short", hour: "2-digit", minute: "2-digit", hourCycle: "h23",
      ...(new Date(f).getUTCFullYear() !== new Date(start).getUTCFullYear() ? { year: "numeric" } : {}),
      ...(tz === "utc" ? { timeZone: "UTC" } : {}),
    });
    const at = (ms: number) => (tz === "utc" ? `${fmt.format(ms)} UTC` : fmt.format(ms));
    const mins = Math.round((en - f) / 60_000);
    const hm = `${Math.floor(mins / 60)}:${String(mins % 60).padStart(2, "0")}`;
    const days = Math.round((mins / 1440) * 10) / 10;
    const span = mins < 1440 ? t("costs.range.spanHours", { hm })
      : t("costs.range.spanDays", { count: days, n: fmtNumber(days, Number.isInteger(days) ? 0 : 1), hm });
    return t("costs.range.summary", { from: at(f), to: at(en), span });
  }

  const n = Number(minutes);
  const minutesOk = /^\d+$/.test(minutes.trim()) && n >= 1 && n <= MAX_LAST_MINUTES;
  const a = choices.find((c) => c.id === (pickA || choices[1]?.id));
  const b = choices.find((c) => c.id === (pickB || choices[0]?.id));
  const label = (c: (typeof choices)[number]) => t("costs.range.reading", { at: fmtBucket(iso(c.at), "10min"), pct: fmtNumber(c.percent, 0) });
  const quickLabel = quickDays == null ? "" : quickDays === 1 ? t("costs.range24h") : t("costs.rangeDays", { count: quickDays });
  const locale = useCalendarLocale(i18n.language);

  const selected = pending != null
    ? { from: dayDate(pending, tz), to: undefined }
    : s.day && e.day && e.day >= s.day ? { from: dayDate(s.day, tz), to: dayDate(e.day, tz) } : undefined;

  return (
    <form ref={formRef} className="card mb-4 cost-range" onSubmit={submit} aria-label={t("costs.range.title")} data-range-panel noValidate>
      <div className="cost-range-head">
        <div style={{ fontWeight: 600 }}>{t("costs.range.title")}</div>
        <div role="group" aria-labelledby={`${uid}-tz`} className="cost-range-tz text-xs">
          <span id={`${uid}-tz`}>{t("costs.range.tz")}</span>
          <Seg value={tz} onChange={switchTz} options={[{ v: "utc", label: t("costs.range.tzUtc") }, { v: "local", label: t("costs.range.tzLocal") }]} />
        </div>
      </div>

      <div className="cost-range-grid">
        <div className="cost-range-presets" role="group" aria-labelledby={`${uid}-picks`} data-range-presets>
          <div className="cost-range-label" id={`${uid}-picks`}>{t("costs.range.picks")}</div>
          <div className="cost-range-preset-list">
            {RANGE_PRESETS.map((p) => (
              <button key={p} type="button" className="cost-range-preset" data-preset={p}
                onClick={() => { const [f, en] = presetRange(p, now(), tz); take(f, en); }}>{t(`costs.range.${p}`)}</button>
            ))}
            <button type="button" className="cost-range-preset" disabled={reset == null} data-preset="sinceReset"
              aria-describedby={reset == null ? `${uid}-noreset` : undefined}
              onClick={() => { if (reset != null) take(reset, Math.ceil(now() / 60_000) * 60_000); }}>{t("costs.range.sinceReset")}</button>
          </div>
          {reset == null && <p className="muted text-xs cost-range-why" id={`${uid}-noreset`}>{t("costs.range.sinceResetNone")}</p>}
          {quickDays != null && (
            <div className="cost-range-freeze">
              <button type="button" className="cost-range-preset" data-range-freeze disabled={!canFreeze(quickDays)}
                aria-describedby={`${uid}-freeze`}
                onClick={() => { const [f, en] = freezeRange(quickDays, now()); take(f, en, onFreeze ?? onApply); }}>
                {t("costs.range.freeze", { range: quickLabel })}
              </button>
              <p className="muted text-xs cost-range-why" id={`${uid}-freeze`}>
                {canFreeze(quickDays) ? t("costs.range.freezeHint") : t("costs.range.freezeTooLong", { range: quickLabel })}
              </p>
            </div>
          )}
        </div>

        <div className="cost-range-cal" data-range-calendar>
          {locale == null ? <div className="cost-range-cal-wait" aria-busy="true" /> : <DayPicker
            mode="range"
            selected={selected}
            onSelect={(_range, day) => pickDay(day)}
            numberOfMonths={months}
            month={dayDate(`${firstMonth}-15`, tz)}
            onMonthChange={(m) => setLastMonth(shiftMonth(keyOfPicked(m).slice(0, 7), months - 1))}
            endMonth={new Date(start)}
            today={new Date(start)}
            disabled={{ after: new Date(start) }}
            timeZone={tz === "utc" ? "UTC" : undefined}
            locale={locale}
            weekStartsOn={1}
            aria-label={t("costs.range.calendar")}
          />}
          <p className="muted text-xs cost-range-why cost-range-live" aria-live="polite">{pending != null ? t("costs.range.pickEnd") : ""}</p>
        </div>

        <div className="cost-range-side">
          <div className="cost-range-times">
            <label className="text-xs" htmlFor={`${uid}-st`}>
              <span className="block mb-1">{t("costs.range.startTime")}</span>
              <input id={`${uid}-st`} type="time" step={60} value={s.time} disabled={!s.day}
                aria-invalid={error != null} aria-describedby={describedBy}
                onChange={(ev) => { setPending(null); setFromText(ev.target.value ? `${s.day}T${ev.target.value}` : dayStartValue(s.day, tz)); }} />
            </label>
            <label className="text-xs" htmlFor={`${uid}-et`}>
              <span className="block mb-1">{t("costs.range.endTime")}</span>
              <input id={`${uid}-et`} type="time" step={60} value={e.time} disabled={!e.day}
                aria-invalid={error != null} aria-describedby={[`${uid}-eth`, describedBy].filter(Boolean).join(" ")}
                onChange={(ev) => {
                  setPending(null);
                  const time = ev.target.value;
                  const text = joinEnd(e.day, time, tz);
                  setToText(text);
                  // A cleared time is "until the end of the day": the usual reading of the end field says that.
                  setTypedEnd(time ? { text, day: e.day, time } : null);
                }} />
            </label>
          </div>
          <p className="muted text-xs cost-range-why" id={`${uid}-eth`}>{t("costs.range.endTimeHint")}</p>
          <p className="cost-range-summary" data-range-summary aria-live="polite">{summary}</p>
          {errorText && <p className="danger-text text-xs mt-0 mb-2" role="alert" id={errId} data-range-error={error}>{errorText}</p>}
          <div>
            <button type="submit" className="btn sm primary">{t("costs.range.apply")}</button>
          </div>
          <fieldset className="cost-range-exact">
            <legend className="cost-range-label">{t("costs.range.exact")}</legend>
            <div className="cost-range-exact-fields">
              <label className="text-xs" htmlFor={`${uid}-from`}>
                <span className="block mb-1">{t("costs.range.from")}</span>
                <input id={`${uid}-from`} type="datetime-local" step={60} required value={fromText}
                  aria-invalid={error != null} aria-describedby={describedBy}
                  onChange={(ev) => { setFromText(ev.target.value); setPending(null); }} />
              </label>
              <label className="text-xs" htmlFor={`${uid}-to`}>
                <span className="block mb-1">{t("costs.range.to")}</span>
                <input id={`${uid}-to`} type="datetime-local" step={60} required value={toText}
                  aria-invalid={error != null} aria-describedby={describedBy}
                  onChange={(ev) => { setEnd(ev.target.value); setPending(null); showMonth(splitEnd(ev.target.value, s.day, tz).day); }} />
              </label>
            </div>
          </fieldset>
        </div>
      </div>

      <div className="cost-range-more">
        <div className="cost-range-label">{t("costs.range.more")}</div>
        <div className="flex flex-wrap items-center gap-2">
          <label className="text-xs" htmlFor={`${uid}-min`} style={{ display: "inline-flex", alignItems: "center", gap: 8, margin: 0 }}>
            {t("costs.range.last")}
            <input id={`${uid}-min`} type="number" inputMode="numeric" min={1} max={MAX_LAST_MINUTES} step={1} value={minutes}
              style={{ width: 80 }} onChange={(ev) => setMinutes(ev.target.value)} />
            {t("costs.range.minutes")}
          </label>
          <button type="button" className="btn sm" disabled={!minutesOk}
            onClick={() => { const [f, en] = lastMinutesRange(now(), n); take(f, en); }}>{t("costs.range.show")}</button>
        </div>
        <fieldset className="mt-2" style={{ border: 0, padding: 0, margin: "8px 0 0", minWidth: 0 }}>
          <legend className="text-xs muted" style={{ padding: 0 }}>{t("costs.range.between")}</legend>
          {choices.length < 2 ? <p className="muted text-xs mt-1 mb-0">{t("costs.range.noReadings")}</p> : (
            <div className="flex flex-wrap items-end gap-2 mt-1">
              {/* The two selects share their row (or each take one) at equal width; the button wraps on its own. */}
              <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(190px, 1fr))", gap: 8, flex: "1 1 400px", minWidth: 0 }}>
                <label className="text-xs" htmlFor={`${uid}-ra`}>
                  <span className="block mb-1">{t("costs.range.readingFrom")}</span>
                  <select id={`${uid}-ra`} value={a?.id ?? ""} onChange={(ev) => setPickA(ev.target.value)}>
                    {choices.map((c) => <option key={c.id} value={c.id}>{label(c)}</option>)}
                  </select>
                </label>
                <label className="text-xs" htmlFor={`${uid}-rb`}>
                  <span className="block mb-1">{t("costs.range.readingTo")}</span>
                  <select id={`${uid}-rb`} value={b?.id ?? ""} onChange={(ev) => setPickB(ev.target.value)}>
                    {choices.map((c) => <option key={c.id} value={c.id}>{label(c)}</option>)}
                  </select>
                </label>
              </div>
              <button type="button" className="btn sm" disabled={!a || !b}
                onClick={() => { if (a && b) { const [f, en] = betweenReadings(a.at, b.at); take(f, en); } }}>{t("costs.range.useReadings")}</button>
            </div>
          )}
        </fieldset>
      </div>
      {from != null && to != null && error == null && (
        <p className="muted text-xs mt-3 mb-0" data-range-window>
          {t("costs.range.window", { from: fmtBucket(iso(from), "10min"), to: fmtBucket(iso(to), "10min") })}
        </p>
      )}
      {tz === "local" && <p className="muted text-xs mt-1 mb-0">{t("costs.range.dayUtc")}</p>}
    </form>
  );
}
