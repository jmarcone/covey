import { useId, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import { ApiError, api, del, post, put, type CostBasisKind, type RuntimeInstance, type UsageCharge, type UsageChargeKind,
  type UsageLimit, type UsageReading, type WindowCharge } from "../api";
import { fmtBucket, fmtNumber, fmtUSD } from "../format";
import { chargeDeletable, chargeKey, isExportCharge, isMeterCharge, isSeat, usageCurrencies, windowResetsAt, type CredentialFigures,
  type ExportCheck, type MonthlyMeter, type UsageCurrency } from "./costFigures";
import { planPlace, toLocalInput } from "./PlanPrice";

/* How a credential's real money is formed (0151, #120), on the cost page.
 *
 * Per credential a basis — seat (its plan prices the meter), usage (charges
 * for what was used are the money), or both — dated like the plan: a change
 * holds from a moment on and never overwrites. And usage charges entered by
 * hand (an invoice, or a figure measured until the real one is known), each
 * in its own currency. A dollar figure exists only where the charge is in
 * dollars or carries a rate with its date: the page never converts. */

const BASES: CostBasisKind[] = ["seat", "usage", "seat_and_usage"];

/** An amount in its own currency: dollars as the page writes them, any other
 *  currency with its code — never converted. */
export function fmtMoney(v: number, currency: string): string {
  const cur = currency.toUpperCase();
  return cur === "USD" ? fmtUSD(v) : `${fmtNumber(v, 2)} ${cur}`;
}

/** Counted usage charges per currency, each in its own: "180.00 EUR (no USD
 *  figure) · $5.00" — a dollar figure beside another currency only at the
 *  rates given, never a sum across currencies. */
export function usageList(t: TFunction, list: readonly UsageCurrency[]): string {
  return list.map((c) => {
    // Of it, what the monthly meter counted (0152), and a dollar figure
    // beside another currency only at the rates given.
    const notes = [
      c.meter ? t("costs.basis.usageMeterPart", { amount: fmtMoney(c.meter, c.currency) }) : null,
      c.currency === "USD" ? null : c.usd != null ? t("costs.basis.usageAtRate", { usd: fmtUSD(c.usd) }) : t("costs.basis.noUsd"),
    ].filter((x): x is string => x != null);
    return notes.length > 0 ? `${fmtMoney(c.counted, c.currency)} (${notes.join("; ")})` : fmtMoney(c.counted, c.currency);
  }).join(" · ");
}

/** The usage tile's line under one currency: the charges entered by hand,
 *  the monthly meter's part (0152), and its dollar figure or why none. */
export function usageSub(t: TFunction, c: UsageCurrency): string {
  const first = c.currency === "USD" ? (c.charges > 0 ? t("costs.basis.usageCount", { n: fmtNumber(c.charges, 0) }) : null)
    : c.usd != null ? t("costs.basis.usageAtRate", { usd: fmtUSD(c.usd) }) : t("costs.basis.noRate");
  const meter = c.meter ? t("costs.basis.usageMeterPart", { amount: fmtMoney(c.meter, c.currency) }) : null;
  return [first, meter].filter((x): x is string => x != null).join(" · ");
}

/** A rate a person gave, or that there is none. */
function rateNote(t: TFunction, c: Pick<WindowCharge, "currency" | "fx_to_usd" | "fx_date" | "counted_amount" | "counted_usd">): string | null {
  if (c.currency.toUpperCase() === "USD") return null;
  // Nothing counted has no dollar figure: "–", never $0.00.
  return c.fx_to_usd != null && c.fx_date
    ? t("costs.basis.chargeRate", { rate: fmtNumber(c.fx_to_usd, 4), currency: c.currency.toUpperCase(), date: c.fx_date,
      usd: c.counted_amount > 0 && c.counted_usd != null ? fmtUSD(c.counted_usd) : "–" })
    : t("costs.basis.chargeNoRate");
}

const span = (c: Pick<WindowCharge, "from" | "to">) => `${fmtBucket(c.from, "hour")} – ${fmtBucket(c.to, "hour")}`;

/** A signed amount in its currency: "+5.00 EUR", "−$2.00". */
function signedMoney(v: number, currency: string): string {
  return `${v < 0 ? "−" : "+"}${fmtMoney(Math.abs(v), currency)}`;
}

/** A usage export (0152): what it says over the window, a check and never
 *  counted, beside what the counted charges say over the same span. */
export function exportLine(t: TFunction, x: ExportCheck): string {
  const c = x.charge;
  const parts = [t("costs.basis.exportLabel"), `${fmtMoney(c.amount, c.currency)} · ${span(c)}`];
  if (Math.abs(c.window_amount - c.amount) > 1e-9) parts.push(t("costs.basis.exportInWindow", { amount: fmtMoney(c.window_amount, c.currency) }));
  parts.push(x.counted != null && x.diff != null
    ? t("costs.basis.exportCounted", { counted: fmtMoney(x.counted, c.currency), diff: signedMoney(x.diff, c.currency) })
    : x.otherCurrency ? t("costs.basis.exportOtherCurrency") : t("costs.basis.exportNothingCounted"));
  return parts.join(" · ");
}

/** A meter charge (0152): the money the monthly usage limit makes of its
 *  meter's movement — its limit, points and range. */
function meterLine(t: TFunction, c: WindowCharge): string {
  const cur = c.currency;
  const parts = [
    t("costs.basis.meterLabel", { window: c.window_kind ?? "", limit: fmtMoney(c.limit_amount ?? 0, cur) }),
    span(c),
    t("costs.basis.meterPoints", { pts: fmtNumber(c.points ?? 0, 1), counted: fmtNumber(c.counted_points ?? 0, 1) }),
    c.counted_amount > 0
      ? t("costs.basis.meterCounted", { amount: fmtMoney(c.counted_amount, cur),
        low: fmtMoney(c.counted_low ?? c.counted_amount, cur), high: fmtMoney(c.counted_high ?? c.counted_amount, cur) })
      : t("costs.basis.chargeNotCounted"),
  ];
  const rate = rateNote(t, c);
  if (rate) parts.push(rate);
  return parts.join(" · ");
}

/** "Charged by: …" — the basis in effect at the window's end, or what holds without one. */
export function basisLine(t: TFunction, f: Pick<CredentialFigures, "basis" | "kind">): string {
  if (f.basis === "usage") return t("costs.basis.lineUsage");
  if (f.basis === "seat_and_usage") return t("costs.basis.lineBoth");
  if (f.basis === "seat") return t("costs.basis.lineSeat");
  return t(f.kind === "api_key" ? "costs.basis.lineDefaultBill" : "costs.basis.lineDefaultSeat");
}

/** What a charge counts in the window, and its dollar figure or why none. */
export function chargeLine(t: TFunction, c: WindowCharge): string {
  if (isMeterCharge(c)) return meterLine(t, c);
  if (isExportCharge(c)) return exportLine(t, { charge: c, counted: null, diff: null, otherCurrency: false });
  const parts = [`${fmtMoney(c.amount, c.currency)} · ${span(c)}`];
  // The part a monthly usage limit held over (0152): the meter is that
  // time's money, so it is listed and not counted a second time.
  const superseded = c.superseded_amount ?? 0;
  const counted = Math.abs(c.counted_amount - c.amount) < 1e-9 ? t("costs.basis.chargeCounted")
    : c.counted_amount > 0 ? t("costs.basis.chargeCountedPart", { amount: fmtMoney(c.counted_amount, c.currency) })
    : superseded > 0 ? null : t("costs.basis.chargeNotCounted");
  if (counted) parts.push(counted);
  if (superseded > 0) parts.push(t("costs.basis.superseded", { amount: fmtMoney(superseded, c.currency) }));
  const rate = rateNote(t, c);
  if (rate) parts.push(rate);
  return parts.join(" · ");
}

/** A stored charge as entered: amount, span, its rate or none. */
function storedLine(t: TFunction, c: UsageCharge): string {
  const parts = [`${c.kind === "export" ? `${t("costs.basis.exportLabel")} · ` : ""}${fmtMoney(c.amount, c.currency)} · ${span(c)}`];
  if (c.currency.toUpperCase() !== "USD") {
    parts.push(c.fx_to_usd != null && c.fx_date
      ? t("costs.basis.storedRate", { rate: fmtNumber(c.fx_to_usd, 4), currency: c.currency.toUpperCase(), date: c.fx_date })
      : t("costs.basis.chargeNoRate"));
  }
  return parts.join(" · ");
}

/** A monthly usage limit as stored (0152): amount per month, its window,
 *  its rate or none, and when it held. */
export function limitLine(t: TFunction, l: UsageLimit): string {
  const at = (iso: string) => `${new Date(iso).toISOString().slice(0, 16).replace("T", " ")} UTC`;
  const parts = [t("costs.basis.limitPerMonth", { amount: fmtMoney(l.amount, l.currency), window: l.window_kind })];
  if (l.currency.toUpperCase() !== "USD") {
    parts.push(l.fx_to_usd != null && l.fx_date
      ? t("costs.basis.storedRate", { rate: fmtNumber(l.fx_to_usd, 4), currency: l.currency.toUpperCase(), date: l.fx_date })
      : t("costs.basis.chargeNoRate"));
  }
  parts.push(l.valid_to ? t("costs.basis.limitHeld", { from: at(l.valid_from), to: at(l.valid_to) }) : t("costs.basis.limitSince", { from: at(l.valid_from) }));
  return parts.join(" · ");
}

/** A monthly meter (0152) in words, never mixed into the weekly seat meter:
 *  "Monthly usage limit (overage): 78 % → 83 % (5 points from 4 readings) ·
 *  resets 01.12. UTC · limit 1,000.00 EUR per month · from the meter: 50.00 EUR". */
export function monthlyLine(t: TFunction, m: MonthlyMeter, resetsAt: string | null): string {
  const c = m.coverage;
  const parts = [t("costs.basis.monthlyMeter", { window: c.window_kind, from: fmtNumber(c.baseline_percent, 0), to: fmtNumber(c.last_percent, 0),
    pts: fmtNumber(c.points, 0), n: fmtNumber(c.readings, 0) })];
  if (resetsAt) parts.push(t("costs.basis.monthlyResets", { date: fmtBucket(resetsAt, "day") }));
  parts.push(m.limit ? t("costs.basis.limitPerMonth", { amount: fmtMoney(m.limit.amount, m.limit.currency), window: m.limit.window_kind })
    : t("costs.basis.monthlyNoLimit"));
  const money = usageCurrencies(m.charges);
  if (money.length > 0) parts.push(t("costs.basis.monthlyMoney", { amount: money.map((x) => fmtMoney(x.counted, x.currency)).join(" · ") }));
  else if (m.limit && m.charges.length > 0) parts.push(t("costs.basis.chargeNotCounted"));
  return parts.join(" · ");
}

/** A credential's monthly meters, one line each, beside (never inside) its weekly one. */
export function MonthlyMeters({ f, readings }: { f: Pick<CredentialFigures, "monthly" | "secretKey" | "slot">; readings: readonly UsageReading[] }) {
  const { t } = useTranslation();
  if (!f.monthly || f.monthly.length === 0 || f.secretKey == null) return null;
  return (
    <>
      {f.monthly.map((m) => (
        <p key={m.coverage.window_kind} className="text-xs mt-0 mb-1" data-monthly-meter={m.coverage.window_kind}>
          {monthlyLine(t, m, windowResetsAt(readings, f.secretKey!, f.slot!, m.coverage.window_kind))}
        </p>
      ))}
    </>
  );
}

/** An API key whose bill covey reads (0146): a synced bill stands beside it. */
function hasSyncedBill(f: Pick<CredentialFigures, "kind" | "billed">): boolean {
  return f.kind === "api_key" && f.billed != null && !["unavailable", "unconfigured", "unmapped"].includes(f.billed.state);
}

function saveError(t: TFunction, err: Error, what: "basis" | "charge" | "limit" = "basis"): string {
  if (err instanceof ApiError) {
    // Three 409s: a basis or a limit at the same moment, or a charge over
    // another's span.
    if (err.status === 409) return t(what === "charge" ? "costs.basis.errOverlap" : what === "limit" ? "costs.basis.errLimitExists" : "costs.basis.errConflict");
    if (err.status === 400) return t("costs.basis.errInvalid", { msg: err.message });
    if (err.status === 403) return t("costs.basis.errForbidden", { msg: err.message });
  }
  return err.message;
}

/** One credential's basis line, its usage charges in the window, and for the
 *  roles that may, the forms that set a basis and add a charge. Nothing at
 *  all for a reader when neither was ever set. */
export function CostBasisPanel({ f, name, editable }: { f: CredentialFigures; name: string; editable: boolean }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const uid = useId();
  const [form, setForm] = useState<"none" | "basis" | "charge" | "limit" | "reading">("none");
  const [chargeKind, setChargeKind] = useState<UsageChargeKind>("charge");
  // The monthly usage limit (0152) and a meter reading read off the provider.
  const [limitFrom, setLimitFrom] = useState("");
  const [window_, setWindow] = useState("overage");
  const [percent, setPercent] = useState("");
  const [resetsAt, setResetsAt] = useState("");
  const [readAt, setReadAt] = useState("");
  const now = () => new Date(Math.floor(Date.now() / 60_000) * 60_000);
  const [basis, setBasis] = useState<CostBasisKind>("usage");
  const [basisFrom, setBasisFrom] = useState("");
  const [amount, setAmount] = useState("");
  const [currency, setCurrency] = useState("EUR");
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [rate, setRate] = useState("");
  const [rateDate, setRateDate] = useState("");
  const [note, setNote] = useState("");

  const instances = useQuery({
    queryKey: ["runtime-instances"],
    queryFn: () => api<RuntimeInstance[]>("/runtime-instances"),
    enabled: editable,
  });
  const place = planPlace(f, instances.data);
  const base = place ? `/runtime-instances/${place.runtimeId}/credentials/${place.ord}` : null;
  const done = async () => {
    await qc.invalidateQueries({ queryKey: ["cost"] });
    await qc.invalidateQueries({ queryKey: ["usage-charges"] });
    setForm("none");
  };
  const saveBasis = useMutation({
    mutationFn: (body: { basis: CostBasisKind; valid_from: string }) => put(`${base}/cost-basis`, body),
    onSuccess: done,
  });
  const saveCharge = useMutation({
    mutationFn: (body: Record<string, unknown>) => post(`${base}/usage-charges`, body),
    onSuccess: done,
  });
  const saveLimit = useMutation({
    mutationFn: (body: Record<string, unknown>) => put(`${base}/usage-limit`, body),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ["usage-limit", base] });
      await done();
    },
  });
  const saveReading = useMutation({
    mutationFn: (body: Record<string, unknown>) => post("/cost/usage-readings", body),
    onSuccess: done,
  });
  // The limit history, newest first: read while its form is open.
  const limits = useQuery({
    queryKey: ["usage-limit", base],
    queryFn: () => api<UsageLimit[]>(`${base}/usage-limit`),
    enabled: editable && form === "limit" && base != null,
  });
  const remove = useMutation({
    mutationFn: (id: string) => del(`${base}/usage-charges/${id}`),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ["cost"] });
      await qc.invalidateQueries({ queryKey: ["usage-charges", base] });
    },
  });
  // Every charge of the credential, also those outside the window, with its
  // note: from the credential's own list (security roles), read when opened.
  const [allOpen, setAllOpen] = useState(false);
  const all = useQuery({
    queryKey: ["usage-charges", base],
    queryFn: () => api<UsageCharge[]>(`${base}/usage-charges`),
    enabled: editable && allOpen && base != null,
  });

  const u = f.usage;
  if (!u && !editable) return null;
  const seat = isSeat(f) || f.kind === "subscription";

  const openBasis = () => {
    void instances.refetch();
    setBasis(f.basis === "usage" ? "seat_and_usage" : "usage");
    setBasisFrom(toLocalInput(now()));
    saveBasis.reset();
    setForm("basis");
  };
  const openCharge = () => {
    void instances.refetch();
    const end = new Date(Math.floor(Date.now() / 3600_000) * 3600_000);
    setAmount("");
    setCurrency("EUR");
    setTo(toLocalInput(end));
    setFrom(toLocalInput(new Date(end.getTime() - 86400_000)));
    setRate("");
    setRateDate("");
    setNote("");
    setChargeKind("charge");
    saveCharge.reset();
    setForm("charge");
  };
  const openLimit = () => {
    void instances.refetch();
    setAmount("");
    setCurrency("EUR");
    setRate("");
    setRateDate("");
    setLimitFrom(toLocalInput(now()));
    saveLimit.reset();
    setForm("limit");
  };
  const openReading = () => {
    void instances.refetch();
    const d = new Date();
    // A monthly meter resets on the first of the month (UTC).
    setWindow("overage");
    setPercent("");
    setResetsAt(toLocalInput(new Date(Date.UTC(d.getUTCFullYear(), d.getUTCMonth() + 1, 1))));
    setReadAt(toLocalInput(now()));
    saveReading.reset();
    setForm("reading");
  };

  const fromMs = new Date(from).getTime(), toMs = new Date(to).getTime();
  const amountNum = amount.trim() === "" ? NaN : Number(amount);
  const rateNum = rate.trim() === "" ? null : Number(rate);
  const cur = currency.trim().toUpperCase();
  const chargeValid = Number.isFinite(amountNum) && amountNum >= 0 && /^[A-Z]{3}$/.test(cur)
    && Number.isFinite(fromMs) && Number.isFinite(toMs) && fromMs < toMs
    && (cur === "USD" || (rateNum == null) === (rateDate === "")) && (rateNum == null || rateNum > 0);
  const basisMs = new Date(basisFrom).getTime();
  const limitFromMs = new Date(limitFrom).getTime();
  const limitValid = Number.isFinite(amountNum) && amountNum > 0 && /^[A-Z]{3}$/.test(cur) && Number.isFinite(limitFromMs)
    && (cur === "USD" || (rateNum == null) === (rateDate === "")) && (rateNum == null || rateNum > 0);
  const percentNum = percent.trim() === "" ? NaN : Number(percent);
  const resetsMs = new Date(resetsAt).getTime(), readMs = readAt === "" ? Date.now() : new Date(readAt).getTime();
  const readingValid = /^[a-z][a-z0-9_]{0,39}$/.test(window_) && Number.isFinite(percentNum) && percentNum >= 0 && percentNum <= 1000
    && Number.isFinite(resetsMs) && Number.isFinite(readMs);
  const error = saveBasis.error ? saveError(t, saveBasis.error) : saveCharge.error ? saveError(t, saveCharge.error, "charge")
    : saveLimit.error ? saveError(t, saveLimit.error, "limit") : saveReading.error ? saveError(t, saveReading.error)
    : remove.error ? saveError(t, remove.error) : null;
  const exportOf = new Map((u?.exports ?? []).map((x) => [x.charge.id, x]));
  const deleteButton = (id: string) => editable && base && (
    <button type="button" className="btn sm ghost" style={{ marginLeft: 6 }} disabled={remove.isPending}
      onClick={() => { if (window.confirm(t("costs.basis.confirmDelete"))) remove.mutate(id); }}>
      {t("costs.basis.delete")}
    </button>
  );
  // An API key whose bill covey reads: the charges stand beside that bill,
  // never instead of it — entering the same bill here would count it twice.
  const besideBill = hasSyncedBill(f) && u != null && (u.charges.length > 0 || u.regimes.some((r) => r !== "seat"));

  return (
    <div className="mb-2" data-cost-basis={f.id}>
      <p className="muted text-xs mt-0 mb-1" data-basis-line>
        {basisLine(t, f)}
        {seat && f.basis === "usage" && <> {t("costs.basis.seatOnUsage")}</>}
        {seat && f.basis === "seat_and_usage" && <> {t("costs.basis.seatOnBoth")}</>}
        {u?.changed && <> {t("costs.basis.changed")}</>}
        {u?.upcoming.map((b) => (
          // With its year: a basis can be entered far ahead.
          <span key={b.valid_from} data-basis-upcoming> {t("costs.basis.upcoming", {
            date: `${new Date(b.valid_from).toISOString().slice(0, 16).replace("T", " ")} UTC`, basis: t(`costs.basis.kind_${b.basis}`) })}</span>
        ))}
      </p>
      {besideBill && (
        <p className="text-xs mt-0 mb-1" role="note" style={{ color: "var(--text-warning)" }} data-beside-bill>
          {t("costs.basis.apiKeyBilled")}
        </p>
      )}
      {u && u.uncounted.length > 0 && (
        <p className="text-xs mt-0 mb-1" role="note" style={{ color: "var(--text-warning)" }} data-uncounted>
          {t("costs.basis.uncounted", { n: fmtNumber(u.uncounted.length, 0) })}
        </p>
      )}
      {u && u.charges.length > 0 && (
        <ul className="text-xs mt-0 mb-1" style={{ paddingLeft: 16 }} aria-label={t("costs.basis.chargesList", { name })}>
          {u.charges.map((c) => {
            // A meter charge (0152) has the zero id, shared by several: keyed
            // by its window and span, and not deletable (it is not stored).
            const x = isExportCharge(c) ? exportOf.get(c.id) : undefined;
            return (
              <li key={chargeKey(c)} data-charge={chargeKey(c)} data-charge-kind={c.kind ?? "charge"}>
                {x ? exportLine(t, x) : chargeLine(t, c)}
                {chargeDeletable(c) && deleteButton(c.id)}
              </li>
            );
          })}
        </ul>
      )}
      {editable && base && (
        <div className="text-xs mt-0 mb-1" data-all-charges>
          <button type="button" className="btn sm ghost" aria-expanded={allOpen} onClick={() => setAllOpen((o) => !o)}>
            {allOpen ? "▾" : "▸"} {t("costs.basis.allCharges")}
          </button>
          {allOpen && all.data && all.data.length === 0 && <p className="muted mt-1 mb-0">{t("costs.basis.allChargesEmpty")}</p>}
          {allOpen && all.data && all.data.length > 0 && (
            <ul className="mt-1 mb-0" style={{ paddingLeft: 16 }} aria-label={t("costs.basis.allChargesList", { name })}>
              {all.data.map((c) => (
                <li key={c.id} data-stored-charge={c.id}>
                  {storedLine(t, c)}
                  {c.note && <span className="muted" data-charge-note> · {c.note}</span>}
                  {deleteButton(c.id)}
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
      {editable && base && form === "none" && (
        <div className="flex flex-wrap gap-2">
          <button type="button" className="btn sm ghost" onClick={openBasis}>{t("costs.basis.setBasis")}</button>
          <button type="button" className="btn sm ghost" onClick={openCharge}>{t("costs.basis.addCharge")}</button>
          <button type="button" className="btn sm ghost" onClick={openLimit}>{t("costs.basis.setLimit")}</button>
          <button type="button" className="btn sm ghost" onClick={openReading}>{t("costs.basis.addReading")}</button>
        </div>
      )}
      {editable && form === "limit" && (
        <form className="mt-2" aria-label={t("costs.basis.limitForm", { name })} onSubmit={(e) => {
          e.preventDefault();
          if (!base || !limitValid || saveLimit.isPending) return;
          const body: Record<string, unknown> = { amount: amountNum, currency: cur, valid_from: new Date(limitFromMs).toISOString() };
          if (cur !== "USD" && rateNum != null) Object.assign(body, { fx_to_usd: rateNum, fx_date: rateDate });
          saveLimit.mutate(body);
        }}>
          <div className="flex flex-wrap items-end gap-3">
            <label className="text-xs" htmlFor={`${uid}-lamount`}>
              <span className="block mb-1">{t("costs.basis.limitAmount")}</span>
              <input id={`${uid}-lamount`} type="number" inputMode="decimal" min="0" step="0.01" required value={amount}
                style={{ width: 110 }} onChange={(e) => setAmount(e.target.value)} />
            </label>
            <label className="text-xs" htmlFor={`${uid}-lcur`}>
              <span className="block mb-1">{t("costs.basis.currency")}</span>
              <input id={`${uid}-lcur`} type="text" required maxLength={3} value={currency} style={{ width: 60 }}
                onChange={(e) => setCurrency(e.target.value)} />
            </label>
            {cur !== "USD" && <>
              <label className="text-xs" htmlFor={`${uid}-lrate`}>
                <span className="block mb-1">{t("costs.basis.rate", { currency: cur || "–" })}</span>
                <input id={`${uid}-lrate`} type="number" inputMode="decimal" min="0" step="0.0001" value={rate}
                  style={{ width: 90 }} onChange={(e) => setRate(e.target.value)} />
              </label>
              <label className="text-xs" htmlFor={`${uid}-lrdate`}>
                <span className="block mb-1">{t("costs.basis.rateDate")}</span>
                <input id={`${uid}-lrdate`} type="date" value={rateDate} onChange={(e) => setRateDate(e.target.value)} />
              </label>
            </>}
            <label className="text-xs" htmlFor={`${uid}-lfrom`}>
              <span className="block mb-1">{t("costs.plan.validFrom")}</span>
              <input id={`${uid}-lfrom`} type="datetime-local" required value={limitFrom} onChange={(e) => setLimitFrom(e.target.value)} />
            </label>
            <div className="flex gap-2">
              <button type="submit" className="btn sm primary" disabled={!limitValid || saveLimit.isPending}>
                {saveLimit.isPending ? t("costs.plan.saving") : t("costs.plan.save")}
              </button>
              <button type="button" className="btn sm ghost" disabled={saveLimit.isPending} onClick={() => setForm("none")}>{t("costs.plan.cancel")}</button>
            </div>
          </div>
          <p className="muted text-xs mt-1 mb-0">{t("costs.basis.limitHint")}</p>
          {limits.data && limits.data.length === 0 && <p className="muted text-xs mt-1 mb-0" data-limit-history>{t("costs.basis.limitHistoryEmpty")}</p>}
          {limits.data && limits.data.length > 0 && (
            <ul className="text-xs mt-1 mb-0" style={{ paddingLeft: 16 }} aria-label={t("costs.basis.limitHistory", { name })} data-limit-history>
              {limits.data.map((l) => (
                <li key={`${l.valid_from}/${l.window_kind}`} data-limit={l.valid_from}>{limitLine(t, l)}</li>
              ))}
            </ul>
          )}
        </form>
      )}
      {editable && form === "reading" && (
        <form className="mt-2" aria-label={t("costs.basis.readingForm", { name })} onSubmit={(e) => {
          e.preventDefault();
          if (!place || !readingValid || saveReading.isPending) return;
          saveReading.mutate({ runtime_id: place.runtimeId, ord: place.ord, window_kind: window_, percent: percentNum,
            resets_at: new Date(resetsMs).toISOString(), fetched_at: new Date(readMs).toISOString() });
        }}>
          <div className="flex flex-wrap items-end gap-3">
            <label className="text-xs" htmlFor={`${uid}-rwin`}>
              <span className="block mb-1">{t("costs.basis.readingWindow")}</span>
              <input id={`${uid}-rwin`} type="text" required maxLength={40} value={window_} style={{ width: 110 }}
                onChange={(e) => setWindow(e.target.value.trim())} />
            </label>
            <label className="text-xs" htmlFor={`${uid}-rpct`}>
              <span className="block mb-1">{t("costs.basis.readingPercent")}</span>
              <input id={`${uid}-rpct`} type="number" inputMode="decimal" min="0" max="1000" step="1" required value={percent}
                style={{ width: 80 }} onChange={(e) => setPercent(e.target.value)} />
            </label>
            <label className="text-xs" htmlFor={`${uid}-rreset`}>
              <span className="block mb-1">{t("costs.basis.readingResets")}</span>
              <input id={`${uid}-rreset`} type="datetime-local" required value={resetsAt} onChange={(e) => setResetsAt(e.target.value)} />
            </label>
            <label className="text-xs" htmlFor={`${uid}-rat`}>
              <span className="block mb-1">{t("costs.basis.readingAt")}</span>
              <input id={`${uid}-rat`} type="datetime-local" value={readAt} onChange={(e) => setReadAt(e.target.value)} />
            </label>
            <div className="flex gap-2">
              <button type="submit" className="btn sm primary" disabled={!readingValid || saveReading.isPending}>
                {saveReading.isPending ? t("costs.plan.saving") : t("costs.plan.save")}
              </button>
              <button type="button" className="btn sm ghost" disabled={saveReading.isPending} onClick={() => setForm("none")}>{t("costs.plan.cancel")}</button>
            </div>
          </div>
          <p className="muted text-xs mt-1 mb-0">{t("costs.basis.readingHint")}</p>
        </form>
      )}
      {editable && form === "basis" && (
        <form className="mt-2" aria-label={t("costs.basis.basisForm", { name })} onSubmit={(e) => {
          e.preventDefault();
          if (!base || !Number.isFinite(basisMs) || saveBasis.isPending) return;
          saveBasis.mutate({ basis, valid_from: new Date(basisMs).toISOString() });
        }}>
          <div className="flex flex-wrap items-end gap-3">
            <label className="text-xs" htmlFor={`${uid}-basis`}>
              <span className="block mb-1">{t("costs.basis.basis")}</span>
              <select id={`${uid}-basis`} value={basis} onChange={(e) => setBasis(e.target.value as CostBasisKind)}>
                {BASES.map((b) => <option key={b} value={b}>{t(`costs.basis.kind_${b}`)}</option>)}
              </select>
            </label>
            <label className="text-xs" htmlFor={`${uid}-bfrom`}>
              <span className="block mb-1">{t("costs.plan.validFrom")}</span>
              <input id={`${uid}-bfrom`} type="datetime-local" required value={basisFrom} onChange={(e) => setBasisFrom(e.target.value)} />
            </label>
            <div className="flex gap-2">
              <button type="submit" className="btn sm primary" disabled={!Number.isFinite(basisMs) || saveBasis.isPending}>
                {saveBasis.isPending ? t("costs.plan.saving") : t("costs.plan.save")}
              </button>
              <button type="button" className="btn sm ghost" disabled={saveBasis.isPending} onClick={() => setForm("none")}>{t("costs.plan.cancel")}</button>
            </div>
          </div>
          <p className="muted text-xs mt-1 mb-0">{t("costs.basis.basisHint")}</p>
        </form>
      )}
      {editable && form === "charge" && (
        <form className="mt-2" aria-label={t("costs.basis.chargeForm", { name })} onSubmit={(e) => {
          e.preventDefault();
          if (!base || !chargeValid || saveCharge.isPending) return;
          const body: Record<string, unknown> = { amount: amountNum, currency: cur, from: new Date(fromMs).toISOString(),
            to: new Date(toMs).toISOString(), note, kind: chargeKind };
          if (cur !== "USD" && rateNum != null) Object.assign(body, { fx_to_usd: rateNum, fx_date: rateDate });
          saveCharge.mutate(body);
        }}>
          <div className="flex flex-wrap items-end gap-3">
            <label className="text-xs" htmlFor={`${uid}-kind`}>
              <span className="block mb-1">{t("costs.basis.chargeKind")}</span>
              <select id={`${uid}-kind`} value={chargeKind} onChange={(e) => setChargeKind(e.target.value as UsageChargeKind)}>
                <option value="charge">{t("costs.basis.chargeKind_charge")}</option>
                <option value="export">{t("costs.basis.chargeKind_export")}</option>
              </select>
            </label>
            <label className="text-xs" htmlFor={`${uid}-amount`}>
              <span className="block mb-1">{t("costs.basis.amount")}</span>
              <input id={`${uid}-amount`} type="number" inputMode="decimal" min="0" step="0.01" required value={amount}
                style={{ width: 110 }} onChange={(e) => setAmount(e.target.value)} />
            </label>
            <label className="text-xs" htmlFor={`${uid}-cur`}>
              <span className="block mb-1">{t("costs.basis.currency")}</span>
              <input id={`${uid}-cur`} type="text" required maxLength={3} value={currency} style={{ width: 60 }}
                onChange={(e) => setCurrency(e.target.value)} />
            </label>
            <label className="text-xs" htmlFor={`${uid}-from`}>
              <span className="block mb-1">{t("costs.basis.from")}</span>
              <input id={`${uid}-from`} type="datetime-local" required value={from} onChange={(e) => setFrom(e.target.value)} />
            </label>
            <label className="text-xs" htmlFor={`${uid}-to`}>
              <span className="block mb-1">{t("costs.basis.to")}</span>
              <input id={`${uid}-to`} type="datetime-local" required value={to} onChange={(e) => setTo(e.target.value)} />
            </label>
            {cur !== "USD" && <>
              <label className="text-xs" htmlFor={`${uid}-rate`}>
                <span className="block mb-1">{t("costs.basis.rate", { currency: cur || "–" })}</span>
                <input id={`${uid}-rate`} type="number" inputMode="decimal" min="0" step="0.0001" value={rate}
                  style={{ width: 90 }} onChange={(e) => setRate(e.target.value)} />
              </label>
              <label className="text-xs" htmlFor={`${uid}-rdate`}>
                <span className="block mb-1">{t("costs.basis.rateDate")}</span>
                <input id={`${uid}-rdate`} type="date" value={rateDate} onChange={(e) => setRateDate(e.target.value)} />
              </label>
            </>}
            <label className="text-xs" htmlFor={`${uid}-note`}>
              <span className="block mb-1">{t("costs.basis.note")}</span>
              <input id={`${uid}-note`} type="text" value={note} maxLength={2000} onChange={(e) => setNote(e.target.value)} />
            </label>
            <div className="flex gap-2">
              <button type="submit" className="btn sm primary" disabled={!chargeValid || saveCharge.isPending}>
                {saveCharge.isPending ? t("costs.plan.saving") : t("costs.plan.save")}
              </button>
              <button type="button" className="btn sm ghost" disabled={saveCharge.isPending} onClick={() => setForm("none")}>{t("costs.plan.cancel")}</button>
            </div>
          </div>
          <p className="muted text-xs mt-1 mb-0">{t(chargeKind === "export" ? "costs.basis.exportHint" : "costs.basis.chargeHint")}</p>
        </form>
      )}
      {error && <p className="text-xs mt-1 mb-0" role="alert" style={{ color: "var(--text-danger)" }}>{error}</p>}
    </div>
  );
}
