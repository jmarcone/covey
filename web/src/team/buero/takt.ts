/* The beat of the office: when a frame is drawn, and when none is.
 *
 * The first version asked the browser for a frame sixty times a second for as
 * long as the page was open, drew whether or not anything had moved, and kept
 * on doing so with the tab in the background. That is a render loop, not a
 * picture: seventy sleeping colleagues cost the machine as much as seventy
 * running ones.
 *
 * Here a frame is an answer to a question. `bild` draws one and says what is
 * still needed afterwards:
 *
 *   weiter  something is travelling (the camera's turn) — the next frame at
 *           the display's pace;
 *   leben   only the ambient life walks about — the next frame in at least
 *           1/30 s. It is atmosphere, not information (leben.ts), and does
 *           not earn sixty frames;
 *   ruht    nothing moves. No frame is queued, not even a timer for one.
 *
 * Whoever changes something that has to be seen calls `anstossen`: the camera
 * on every movement, the pointer, a rebuild, a change of the data. Any number
 * of calls before the next frame make exactly one.
 *
 * At rest the ambient life still has to be given its time, or nobody would
 * ever get up again. That is `leise`: a coarse timer advances the life
 * without drawing and reports whether something started to move on the
 * visible floor — only then is a frame woken. With "reduced motion" there is
 * no `leise` and therefore no timer at all.
 *
 * A hidden tab and a stage scrolled out of view stop everything, timer
 * included, and resume with one frame on return.
 */

export type Bedarf = "weiter" | "leben" | "ruht";

/** The browser's clocks, replaceable so that a test can hold them in its hand. */
export type Uhr = {
  jetzt(): number;
  rahmen(cb: () => void): number;
  rahmenAbbrechen(id: number): void;
  frist(cb: () => void, ms: number): number;
  fristAbbrechen(id: number): void;
};

const browserUhr: Uhr = {
  jetzt: () => performance.now(),
  rahmen: (cb) => requestAnimationFrame(() => cb()),
  rahmenAbbrechen: (id) => cancelAnimationFrame(id),
  frist: (cb, ms) => window.setTimeout(cb, ms),
  fristAbbrechen: (id) => window.clearTimeout(id),
};

/** Ambient life alone is drawn at most this often. */
export const LEBEN_FPS = 30;
/** The timer is set this much short of the frame interval: it never fires
 *  early, and the frame it asks for runs at the next vsync. Aimed at exactly
 *  33.3 ms the timer lands just after a vsync and the frame a whole refresh
 *  later — 20 fps on a 60 Hz display instead of 30. */
export const VORLAUF_MS = 4;
/** How often the life is advanced while nothing is drawn. */
export const RUHE_MS = 250;
/** The longest step the life is given at once, in seconds: after a pause the
 *  figures go on where they were instead of jumping. */
const SCHRITT_MAX = 0.064;

export type Grund = "tab" | "bild";

export type Takt = {
  /** Something visible changed: draw once more. */
  anstossen(): void;
  /** The tab or the stage is (no longer) in view. */
  sichtbar(grund: Grund, an: boolean): void;
  /** Whether a frame, or the timer that leads to one, is queued. */
  readonly wartet: boolean;
  beenden(): void;
};

export function erschaffeTakt(opt: {
  /** Draws one frame. `roh` is the time since the last run in seconds, capped. */
  bild(roh: number): Bedarf;
  /** At rest: advances the life by `dt` seconds without drawing. True when
   *  something on the visible floor has started to move. Absent when the life
   *  does not run (reduced motion). */
  leise?(dt: number): boolean;
  uhr?: Uhr;
}): Takt {
  const uhr = opt.uhr ?? browserUhr;
  let rahmen: number | null = null;
  let frist: number | null = null;
  let letzte = uhr.jetzt();
  let imBild = false;
  let tot = false;
  const gesperrt = new Set<Grund>();

  const abraeumen = () => {
    if (rahmen !== null) uhr.rahmenAbbrechen(rahmen);
    if (frist !== null) uhr.fristAbbrechen(frist);
    rahmen = frist = null;
  };

  function einreihen() {
    if (rahmen !== null) return;
    if (frist !== null) {
      uhr.fristAbbrechen(frist);
      frist = null;
    }
    rahmen = uhr.rahmen(lauf);
  }

  function lauf() {
    rahmen = null;
    if (tot || gesperrt.size) return;
    const anfang = uhr.jetzt();
    const roh = Math.min(SCHRITT_MAX, Math.max(0, anfang - letzte) / 1000);
    letzte = anfang;
    imBild = true;
    let bedarf: Bedarf;
    try {
      bedarf = opt.bild(roh);
    } finally {
      imBild = false;
    }
    if (bedarf === "weiter") einreihen();
    else if (bedarf === "leben") {
      frist = uhr.frist(() => {
        frist = null;
        einreihen();
      }, Math.max(0, 1000 / LEBEN_FPS - (uhr.jetzt() - anfang) - VORLAUF_MS));
    } else ruhen();
  }

  /* Nothing to draw: only the life's time still passes. */
  function ruhen() {
    if (!opt.leise) return;
    frist = uhr.frist(() => {
      frist = null;
      if (tot || gesperrt.size) return;
      const jetzt = uhr.jetzt();
      const dt = Math.min(RUHE_MS / 1000, Math.max(0, jetzt - letzte) / 1000);
      letzte = jetzt;
      if (opt.leise!(dt)) einreihen();
      else ruhen();
    }, RUHE_MS);
  }

  einreihen();
  return {
    anstossen() {
      /* A change made while the frame is being drawn is drawn by that frame. */
      if (tot || gesperrt.size || imBild) return;
      einreihen();
    },
    sichtbar(grund, an) {
      if (tot) return;
      if (an) gesperrt.delete(grund);
      else gesperrt.add(grund);
      if (gesperrt.size) {
        abraeumen();
        return;
      }
      /* Back in view: the time that passed is not the life's to catch up on. */
      letzte = uhr.jetzt();
      einreihen();
    },
    get wartet() {
      return rahmen !== null || frist !== null;
    },
    beenden() {
      tot = true;
      abraeumen();
    },
  };
}

/** Ties a taktgeber to what the person can see: the tab (`visibilitychange`)
 *  and the stage in the viewport (`IntersectionObserver`). Without the latter
 *  the stage counts as visible. Returns the function that lets go again. */
export function sichtWaechter(
  buehne: Element,
  takt: Pick<Takt, "sichtbar">,
  doc: Pick<Document, "hidden" | "addEventListener" | "removeEventListener"> = document,
  IO: typeof IntersectionObserver | undefined = typeof IntersectionObserver === "undefined" ? undefined : IntersectionObserver,
): () => void {
  const tab = () => takt.sichtbar("tab", !doc.hidden);
  doc.addEventListener("visibilitychange", tab);
  if (doc.hidden) tab();
  const io = IO
    ? new IO((eintraege) => {
        /* The last entry is the newest. */
        const e = eintraege[eintraege.length - 1];
        if (e) takt.sichtbar("bild", e.isIntersecting);
      })
    : null;
  io?.observe(buehne);
  return () => {
    doc.removeEventListener("visibilitychange", tab);
    io?.disconnect();
  };
}
