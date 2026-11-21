import { useId } from "react";
import { useTranslation } from "react-i18next";
import type { NoFigureReason } from "./costBasisSwitch";

/** A figure that does not exist here, said as such: a dash and the reason's word, the full sentence for a screen
 *  reader and on hover. Never $0.00 (cost home proposal, honesty rule 6). */
export function NoFigure({ reason, detail }: { reason: NoFigureReason; detail?: string }) {
  const { t } = useTranslation();
  const id = `${useId().replace(/[^a-zA-Z0-9_-]/g, "")}-nf`;
  const long = t(`costs.noFigure.long.${reason}`, { detail: detail ?? "" });
  return (
    <span className="cost-nofigure muted" data-no-figure={reason} title={long} aria-describedby={id} tabIndex={0}>
      – {t(`costs.noFigure.short.${reason}`)}
      <span id={id} className="sr-only">{long}</span>
    </span>
  );
}
