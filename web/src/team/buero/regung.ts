/* Did anything move on the floor that is shown?
 *
 * The takt draws only while something moves, and the life (leben.ts) has many
 * ways to move people: a walk along a route, a push apart between two who
 * stand too close, an arrival from the stairs, a guest who comes or goes.
 * Predicting that from the figures' flags would have to be mirrored in every
 * change to the life. So it is not predicted but looked at: the positions on
 * the shown floor are noted before the life takes its step and compared after
 * it. "Idle" is then true by construction — nothing moved.
 */
import type { Leben } from "./leben";

/** Less than this (plan units, cm) is no movement. Small enough that the
 *  slowest walker (42 cm/s) counts as moving in one frame of a 240 Hz display
 *  (0.18) — a frame that is woken by the pointer must not mistake a walk for
 *  standing still — and large enough that the last sub-millimetre of a push
 *  apart (a geometric tail) ends. */
export const REGUNG_MIN = 0.05;

export type Regung = {
  /** Notes where everyone on `etage` stands, before the life's step. */
  merken(l: Leben, etage: number): void;
  /** Whether anyone on `etage` moved since `merken`, or arrived or left. */
  veraendert(l: Leben, etage: number): boolean;
  /** `merken`, the step, `veraendert`. */
  schritt(l: Leben, etage: number, dt: number): boolean;
};

export function erschaffeRegung(): Regung {
  /* Figures: x, y (NaN while on another floor). Guests on the shown floor:
     id, x, y. Reused between calls; the life is ticked every 250 ms at rest. */
  let alt: number[] = [];
  let neu: number[] = [];

  const lesen = (l: Leben, etage: number, ziel: number[]) => {
    let n = 0;
    for (const f of l.figuren) {
      const da = f.hier === etage;
      ziel[n++] = da ? f.pos.x : NaN;
      ziel[n++] = da ? f.pos.y : NaN;
    }
    for (const g of l.gaeste) {
      if (g.etage !== etage) continue;
      ziel[n++] = g.id;
      ziel[n++] = g.pos.x;
      ziel[n++] = g.pos.y;
    }
    ziel.length = n;
  };

  const merken = (l: Leben, etage: number) => lesen(l, etage, alt);
  const veraendert = (l: Leben, etage: number) => {
    lesen(l, etage, neu);
    let anders = neu.length !== alt.length;
    for (let i = 0; !anders && i < neu.length; i++) {
      const a = alt[i], b = neu[i];
      /* NaN on both sides: on another floor before and after. NaN on one: arrived or left. */
      if (Number.isNaN(a) || Number.isNaN(b)) anders = Number.isNaN(a) !== Number.isNaN(b);
      else anders = Math.abs(a - b) > REGUNG_MIN;
    }
    [alt, neu] = [neu, alt];
    return anders;
  };
  return {
    merken,
    veraendert,
    schritt(l, etage, dt) {
      merken(l, etage);
      l.tick(dt);
      return veraendert(l, etage);
    },
  };
}
