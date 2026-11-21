import { useId } from "react";
import { useTranslation } from "react-i18next";
import { BASES, BASIS_INFO, CASH_BASIS_ENABLED, useBasis, useWide, type Basis, type BasisSupport } from "./costBasisSwitch";
import { Seg } from "./Seg";

/* The Basis switch in the cost pages' filter bar, its lock chip and its tag (cost home PR 9). */

/** The switch: a segmented row from 1600px wide, a select below. Cash stands as an option, disabled, until signed off. */
export function BasisControl() {
  const { t } = useTranslation();
  const { basis, setBasis } = useBasis();
  const wide = useWide();
  const descId = `${useId().replace(/[^a-zA-Z0-9_-]/g, "")}-basis`;
  const options = [
    ...BASES.map((b) => ({ v: b as Basis | "cash", label: t(`costs.basisSwitch.option.${b}`), title: t(`costs.basisSwitch.desc.${b}`) })),
    { v: "cash" as const, label: t("costs.basisSwitch.option.cash"), disabled: !CASH_BASIS_ENABLED, title: t("costs.basisSwitch.desc.cash") },
  ];
  const pick = (v: Basis | "cash") => { if (v !== "cash") setBasis(v); };
  return (
    <span className="cost-basis" data-basis-control={basis} role="group" aria-label={t("costs.basisSwitch.label")} aria-describedby={descId}>
      <span className="muted">{t("costs.basisSwitch.label")}</span>
      {wide ? (
        <Seg<Basis | "cash"> value={basis} onChange={pick} options={options} />
      ) : (
        <select className="cost-basis-select" value={basis} aria-label={t("costs.basisSwitch.label")} aria-describedby={descId}
          onChange={(e) => pick(e.target.value as Basis | "cash")}>
          {options.map((o) => <option key={o.v} value={o.v} disabled={"disabled" in o && o.disabled}>{o.label}</option>)}
        </select>
      )}
      <span id={descId} className="sr-only">{t(`costs.basisSwitch.desc.${basis}`)}</span>
    </span>
  );
}

/** A link that opened with a basis the page cannot show (cash before sign-off, an unknown word): said once, in the bar. */
export function BasisPrunedNote() {
  const { t } = useTranslation();
  const { pruned } = useBasis();
  if (!pruned) return null;
  return <p className="muted text-xs" role="status" data-basis-pruned={pruned}>{t(`costs.basisSwitch.pruned.${pruned}`)}</p>;
}

/** A block that keeps the token price under another basis says so, with the reason and a way back to token price. */
export function BasisLockChip({ support, basis }: { support: BasisSupport; basis: Basis }) {
  const { t } = useTranslation();
  const { setBasis } = useBasis();
  if (support.state !== "locked") return null;
  return (
    <span className="cost-basis-lock" data-basis-lock={support.reason} role="note">
      {t("costs.basisSwitch.lockChip", { basis: t(`costs.basisSwitch.option.${basis}`), reason: t(`costs.basisSwitch.lock.${support.reason}`) })}{" "}
      <button type="button" className="cost-basis-totoken" onClick={() => setBasis("token")} data-basis-to-token>{t("costs.basisSwitch.toToken")}</button>
    </span>
  );
}

/** The basis a column, tile or series is in: its swatch and word, "≈ est." for an estimate. */
export function BasisTag({ basis }: { basis: Basis }) {
  const { t } = useTranslation();
  const info = BASIS_INFO[basis];
  return (
    <span className="cost-basis-tag" data-basis-tag={basis}>
      <span className={`cost-basis-swatch${info.estimate ? " hatched" : ""}`} style={{ background: info.colorVar }} aria-hidden />
      {t(`costs.basisSwitch.tag.${basis}`)}
    </span>
  );
}
