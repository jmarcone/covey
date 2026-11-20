/* Sizes of the overlay's elements, measured when they change — not in the frame.
 *
 * A sign has to know how wide it is to be pushed away from its neighbour, the
 * card how tall it is to sit above a head. Reading `offsetWidth` for that in
 * every frame forces the browser to lay out the page after the frame's own
 * style writes, for every sign, sixty times a second. A ResizeObserver says
 * when a size changes and what it is now; the frame reads the cached value and
 * only writes.
 */

export type Mass = { b: number; h: number };

export type Masse = {
  /** Starts (or, with `null`, stops) measuring an element under a key. Calling
   *  again with the same element changes nothing. */
  beobachten(schluessel: string, el: Element | null): void;
  /** The last measured size; `ersatz` until the first measurement arrives. */
  mass(schluessel: string, ersatz: Mass): Mass;
  /** Begins observing everything registered so far. Restartable after `beenden`. */
  starten(): void;
  beenden(): void;
};

/** `geaendert` is called after a size changed — the stage then draws once more. */
export function erschaffeMasse(
  geaendert: () => void,
  RO: typeof ResizeObserver | undefined = typeof ResizeObserver === "undefined" ? undefined : ResizeObserver,
): Masse {
  const elemente = new Map<string, Element>();
  const schluessel = new WeakMap<Element, string>();
  const werte = new Map<string, Mass>();
  let beobachter: ResizeObserver | null = null;

  const ein = (k: string, el: Element) => {
    schluessel.set(el, k);
    beobachter?.observe(el);
  };
  const aus = (k: string, el: Element) => {
    beobachter?.unobserve(el);
    schluessel.delete(el);
    werte.delete(k);
  };

  return {
    beobachten(k, el) {
      const alt = elemente.get(k);
      if (alt === el || (!alt && !el)) return;
      if (alt) aus(k, alt);
      if (el) {
        elemente.set(k, el);
        ein(k, el);
      } else elemente.delete(k);
    },
    mass: (k, ersatz) => werte.get(k) ?? ersatz,
    starten() {
      if (beobachter || !RO) return;
      beobachter = new RO((eintraege) => {
        for (const e of eintraege) {
          const k = schluessel.get(e.target);
          if (k === undefined) continue;
          const kasten = e.borderBoxSize?.[0];
          werte.set(k, kasten ? { b: kasten.inlineSize, h: kasten.blockSize } : { b: e.contentRect.width, h: e.contentRect.height });
        }
        geaendert();
      });
      for (const [k, el] of elemente) beobachter.observe(el), schluessel.set(el, k);
    },
    beenden() {
      beobachter?.disconnect();
      beobachter = null;
    },
  };
}
