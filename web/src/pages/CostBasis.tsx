import { useId, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import { ApiError, api, del, post, put, type CostBasisKind, type RuntimeInstance, type WindowCharge } from "../api";
import { fmtBucket, fmtNumber, fmtUSD } from "../format";
import { isSeat, type CredentialFigures } from "./costFigures";
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

/** "Charged by: …" — the basis at the window's end, or what holds without one. */
export function basisLine(t: TFunction, f: Pick<CredentialFigures, "basis" | "kind">): string {
  if (f.basis === "usage") return t("costs.basis.lineUsage");
  if (f.basis === "seat_and_usage") return t("costs.basis.lineBoth");
  if (f.basis === "seat") return t("costs.basis.lineSeat");
  return t(f.kind === "api_key" ? "costs.basis.lineDefaultBill" : "costs.basis.lineDefaultSeat");
}

/** What a charge counts in the window, and its dollar figure or why none. */
export function chargeLine(t: TFunction, c: WindowCharge): string {
  const parts = [`${fmtMoney(c.amount, c.currency)} · ${fmtBucket(c.from, "hour")} – ${fmtBucket(c.to, "hour")}`];
  const counted = Math.abs(c.counted_amount - c.amount) < 1e-9 ? t("costs.basis.chargeCounted")
    : c.counted_amount > 0 ? t("costs.basis.chargeCountedPart", { amount: fmtMoney(c.counted_amount, c.currency) })
    : t("costs.basis.chargeNotCounted");
  parts.push(counted);
  if (c.currency.toUpperCase() !== "USD") {
    parts.push(c.fx_to_usd != null && c.fx_date
      ? t("costs.basis.chargeRate", { rate: fmtNumber(c.fx_to_usd, 4), currency: c.currency.toUpperCase(), date: c.fx_date,
        usd: c.counted_usd != null ? fmtUSD(c.counted_usd) : "–" })
      : t("costs.basis.chargeNoRate"));
  }
  return parts.join(" · ");
}

function saveError(t: TFunction, err: Error): string {
  if (err instanceof ApiError) {
    if (err.status === 409) return t("costs.basis.errConflict");
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
  const [form, setForm] = useState<"none" | "basis" | "charge">("none");
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
  const remove = useMutation({
    mutationFn: (id: string) => del(`${base}/usage-charges/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["cost"] }),
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
    saveCharge.reset();
    setForm("charge");
  };

  const fromMs = new Date(from).getTime(), toMs = new Date(to).getTime();
  const amountNum = amount.trim() === "" ? NaN : Number(amount);
  const rateNum = rate.trim() === "" ? null : Number(rate);
  const cur = currency.trim().toUpperCase();
  const chargeValid = Number.isFinite(amountNum) && amountNum >= 0 && /^[A-Z]{3}$/.test(cur)
    && Number.isFinite(fromMs) && Number.isFinite(toMs) && fromMs < toMs
    && (cur === "USD" || (rateNum == null) === (rateDate === "")) && (rateNum == null || rateNum > 0);
  const basisMs = new Date(basisFrom).getTime();
  const error = saveBasis.error ? saveError(t, saveBasis.error) : saveCharge.error ? saveError(t, saveCharge.error)
    : remove.error ? saveError(t, remove.error) : null;

  return (
    <div className="mb-2" data-cost-basis={f.id}>
      <p className="muted text-xs mt-0 mb-1" data-basis-line>
        {basisLine(t, f)}
        {seat && f.basis === "usage" && <> {t("costs.basis.seatOnUsage")}</>}
        {seat && f.basis === "seat_and_usage" && <> {t("costs.basis.seatOnBoth")}</>}
        {u?.changed && <> {t("costs.basis.changed")}</>}
      </p>
      {u && u.uncounted.length > 0 && (
        <p className="text-xs mt-0 mb-1" role="note" style={{ color: "var(--text-warning)" }} data-uncounted>
          {t("costs.basis.uncounted", { n: fmtNumber(u.uncounted.length, 0) })}
        </p>
      )}
      {u && u.charges.length > 0 && (
        <ul className="text-xs mt-0 mb-1" style={{ paddingLeft: 16 }} aria-label={t("costs.basis.chargesList", { name })}>
          {u.charges.map((c) => (
            <li key={c.id} data-charge={c.id}>
              {chargeLine(t, c)}
              {c.note && <span className="muted" data-charge-note> · {c.note}</span>}
              {editable && base && (
                <button type="button" className="btn sm ghost" style={{ marginLeft: 6 }} disabled={remove.isPending}
                  onClick={() => { if (window.confirm(t("costs.basis.confirmDelete"))) remove.mutate(c.id); }}>
                  {t("costs.basis.delete")}
                </button>
              )}
            </li>
          ))}
        </ul>
      )}
      {editable && base && form === "none" && (
        <div className="flex flex-wrap gap-2">
          <button type="button" className="btn sm ghost" onClick={openBasis}>{t("costs.basis.setBasis")}</button>
          <button type="button" className="btn sm ghost" onClick={openCharge}>{t("costs.basis.addCharge")}</button>
        </div>
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
            to: new Date(toMs).toISOString(), note };
          if (cur !== "USD" && rateNum != null) Object.assign(body, { fx_to_usd: rateNum, fx_date: rateDate });
          saveCharge.mutate(body);
        }}>
          <div className="flex flex-wrap items-end gap-3">
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
          <p className="muted text-xs mt-1 mb-0">{t("costs.basis.chargeHint")}</p>
        </form>
      )}
      {error && <p className="text-xs mt-1 mb-0" role="alert" style={{ color: "var(--text-danger)" }}>{error}</p>}
    </div>
  );
}
