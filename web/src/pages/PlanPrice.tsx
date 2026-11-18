import { useId, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import { ApiError, api, put, type CredentialPlan, type RuntimeInstance, type UsageReading } from "../api";
import { fmtBucket, fmtNumber, fmtUSD } from "../format";
import type { CredentialFigures } from "./costFigures";

/* The plan price of a subscription credential, set from the cost page.
 *
 * The real figure and the multiple divide the meter's points by what a point
 * costs, and that needs the plan's price (spec/07 D13). The endpoint is
 * PUT /runtime-instances/{id}/credentials/{ord}/plan; it appends a price valid
 * from a moment and never overwrites. The price follows the secret value, but
 * the endpoint is addressed by the place the credential sits at, and the server
 * resolves that place when the request arrives. A reading or a stored plan
 * keeps the ord it was written with, which a reorder has since given to
 * another secret, so the place comes from the runtime instances as they are
 * now. */

const WEEK_MS = 7 * 86400_000;

/** The roles the plan endpoint lets through (securityRoles in httpapi). */
export const canSetPlan = (role: string) => role === "org_admin" || role === "security";

/** Where the credential sits now: any instance and ord whose credential holds
 *  its secret key and slot. The price follows the secret value, so every such
 *  place is right. Null: it sits nowhere (or the instances are not loaded),
 *  and there is nowhere to send the price to. */
export function planPlace(f: CredentialFigures, instances: readonly RuntimeInstance[] | null | undefined): { runtimeId: string; ord: number } | null {
  for (const rt of instances ?? []) {
    const c = rt.creds.find((x) => x.secret_key === f.secretKey && x.secret_slot === f.slot);
    if (c) return { runtimeId: rt.id, ord: c.ord };
  }
  return null;
}

/** The latest weekly reset the credential's readings name, in ms; NaN when
 *  they name none. A five-hour window does not say when the week began. */
function weeklyReset(f: CredentialFigures, readings: readonly UsageReading[]): number {
  let latest = NaN;
  for (const r of readings) {
    if (r.secret_key !== f.secretKey || r.secret_slot !== f.slot || !r.resets_at) continue;
    if (r.window_kind !== "seven_day" && r.window_kind !== "") continue;
    const ms = Date.parse(r.resets_at);
    if (!Number.isNaN(ms) && !(ms <= latest)) latest = ms;
  }
  return latest;
}

/** The start of the current meter week: the latest weekly reset the
 *  credential's readings name, minus seven days. A reset already behind us
 *  (the last reading is old) starts a week itself, so it is stepped forward
 *  to the week that contains `now`. Without a reading: now. */
export function defaultValidFrom(f: CredentialFigures, readings: readonly UsageReading[], now: Date): Date {
  const latest = weeklyReset(f, readings);
  if (Number.isNaN(latest)) return now;
  let start = latest - WEEK_MS;
  while (start + WEEK_MS <= now.getTime()) start += WEEK_MS;
  return new Date(start);
}

/** Why the form starts where it does: the meter week, now for want of a
 *  weekly reset, or now because a price is being corrected. */
export type PresetWhy = "week" | "now" | "correct";

/** Where the form starts: the start of the current meter week. When the
 *  credential's plan already starts there or later, the form corrects a
 *  price, and a second plan at the same instant is a conflict (the server
 *  never overwrites): then now, to the minute, so the correction holds from
 *  here on. */
export function presetValidFrom(f: CredentialFigures, readings: readonly UsageReading[], now: Date): { at: Date; why: PresetWhy } {
  const week = defaultValidFrom(f, readings, now);
  const planFrom = f.plan ? Date.parse(f.plan.valid_from) : NaN;
  if (planFrom >= week.getTime()) return { at: new Date(Math.floor(now.getTime() / 60_000) * 60_000), why: "correct" };
  return { at: week, why: Number.isNaN(weeklyReset(f, readings)) ? "now" : "week" };
}

/** An instant as the value of a datetime-local input: the reader's clock,
 *  to the minute. */
export function toLocalInput(d: Date): string {
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
}

/** "$200.00/mo since 13/11 UTC" — the plan valid now. */
function currentPlan(t: TFunction, p: CredentialPlan): string {
  const price = p.currency.toUpperCase() === "USD" ? fmtUSD(p.amount) : `${fmtNumber(p.amount, p.amount % 1 ? 2 : 0)} ${p.currency}`;
  return t("costs.plan.current", {
    price, per: t(p.period === "year" ? "costs.fig.perYear" : "costs.fig.perMonth"), since: fmtBucket(p.valid_from, "day"),
  });
}

/** The server stored the price at another credential than the form's. */
class PlanMoved extends Error {}

/** What a failed save says, in the reader's language; the server's own text
 *  in parentheses where it says more than the status. */
function saveError(t: TFunction, err: Error): string {
  if (err instanceof PlanMoved) return t("costs.plan.errMoved");
  if (err instanceof ApiError) {
    // A plan starting at that very instant: the server does not overwrite.
    if (err.status === 409) return t("costs.plan.errConflict");
    if (err.status === 400) return t("costs.plan.errInvalid", { msg: err.message });
    if (err.status === 403) return t("costs.plan.errForbidden", { msg: err.message });
  }
  return err.message;
}

/** One credential's plan line: what is stored, and for the roles that may,
 *  the form that stores a new one. */
export function PlanPrice({ f, readings, editable }: { f: CredentialFigures; readings: readonly UsageReading[]; editable: boolean }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const uid = useId();
  const [open, setOpen] = useState(false);
  const [amount, setAmount] = useState("");
  const [period, setPeriod] = useState("month");
  const [from, setFrom] = useState("");
  // The default as an instant: sent as it is while the field is untouched, so
  // the minute the input shows does not round a reset that has seconds.
  const [fallback, setFallback] = useState<Date>(() => new Date());
  const [why, setWhy] = useState<PresetWhy>("now");

  // Only the roles that see the form need the place. The list is the one the
  // instances page keeps, so it is fetched once for all credentials.
  const instances = useQuery({
    queryKey: ["runtime-instances"],
    queryFn: () => api<RuntimeInstance[]>("/runtime-instances"),
    enabled: editable,
  });
  const place = planPlace(f, instances.data);
  const save = useMutation({
    mutationFn: async (body: { amount: number; currency: string; period: string; valid_from: string }) => {
      const saved = await put<CredentialPlan>(`/runtime-instances/${place!.runtimeId}/credentials/${place!.ord}/plan`, body);
      // The server resolved the place when the request arrived. A reorder
      // since the list was fetched gave it to another secret, and the price
      // is stored there now: say so, and look again for the next try.
      if (saved?.secret_key !== f.secretKey || saved?.secret_slot !== f.slot) {
        void qc.invalidateQueries({ queryKey: ["cost"] });
        void qc.invalidateQueries({ queryKey: ["runtime-instances"] });
        throw new PlanMoved();
      }
      return saved;
    },
    // The real figure and the multiple read the plan: every cost query is stale.
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ["cost"] });
      setOpen(false);
    },
  });

  const show = () => {
    // A reorder since the list was fetched moves the ord: look again while the
    // form is filled in, the place is read when it is saved. Save waits for
    // the answer.
    void instances.refetch();
    const preset = presetValidFrom(f, readings, new Date());
    setFallback(preset.at);
    setWhy(preset.why);
    setFrom(toLocalInput(preset.at));
    setAmount("");
    setPeriod("month");
    save.reset();
    setOpen(true);
  };

  const amountNum = amount.trim() === "" ? NaN : Number(amount);
  const whenMs = from === toLocalInput(fallback) ? fallback.getTime() : new Date(from).getTime();
  const valid = Number.isFinite(amountNum) && amountNum >= 0 && Number.isFinite(whenMs);
  const hint = { week: "costs.plan.fromHintWeek", now: "costs.plan.fromHintNow", correct: "costs.plan.fromHintCorrect" }[why];

  return (
    <div className="mb-2" data-plan-price={f.id}>
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <span className="muted text-xs">{f.plan ? currentPlan(t, f.plan) : t("costs.plan.none")}</span>
        {editable && place && !open && (
          <button type="button" className="btn sm ghost" onClick={show}>{t("costs.plan.set")}</button>
        )}
      </div>
      {editable && place && open && (
        <form
          className="mt-3"
          aria-label={t("costs.plan.set")}
          onSubmit={(e) => {
            e.preventDefault();
            if (!valid || save.isPending || instances.isFetching) return;
            save.mutate({ amount: amountNum, currency: "USD", period, valid_from: new Date(whenMs).toISOString() });
          }}
        >
          <div className="flex flex-wrap items-end gap-3">
            <label className="text-xs" htmlFor={`${uid}-amount`}>
              <span className="block mb-1">{t("costs.plan.amount")}</span>
              <input id={`${uid}-amount`} type="number" inputMode="decimal" min="0" step="0.01" required value={amount}
                style={{ width: 120 }} onChange={(e) => setAmount(e.target.value)} />
            </label>
            <label className="text-xs" htmlFor={`${uid}-period`}>
              <span className="block mb-1">{t("costs.plan.period")}</span>
              <select id={`${uid}-period`} value={period} onChange={(e) => setPeriod(e.target.value)}>
                <option value="month">{t("costs.plan.month")}</option>
                <option value="year">{t("costs.plan.year")}</option>
              </select>
            </label>
            <label className="text-xs" htmlFor={`${uid}-from`}>
              <span className="block mb-1">{t("costs.plan.validFrom")}</span>
              <input id={`${uid}-from`} type="datetime-local" required value={from} onChange={(e) => setFrom(e.target.value)} />
            </label>
            <div className="flex gap-2">
              <button type="submit" className="btn sm primary" disabled={!valid || save.isPending || instances.isFetching}>
                {save.isPending ? t("costs.plan.saving") : t("costs.plan.save")}
              </button>
              <button type="button" className="btn sm ghost" disabled={save.isPending} onClick={() => setOpen(false)}>{t("costs.plan.cancel")}</button>
            </div>
          </div>
          <p className="muted text-xs mt-1 mb-0">
            {/* The input is the reader's clock; the cost page is UTC. */}
            {Number.isFinite(whenMs) && <>{fmtBucket(new Date(whenMs).toISOString(), "hour")} · </>}
            {t(hint)}
          </p>
          {save.error && (
            <p className="text-xs mt-1 mb-0" role="alert" style={{ color: "var(--text-danger)" }}>{saveError(t, save.error)}</p>
          )}
        </form>
      )}
    </div>
  );
}
