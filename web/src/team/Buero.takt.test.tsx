import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent } from "@testing-library/react";
import type { Agent, Department, Laufend } from "../api";
import { mockFetch, renderWithProviders, testPrincipal, useGerman } from "../test/render";

/* The renderer is the one thing replaced: jsdom has no WebGL. Everything else
   — the camera, the life, the takt, the overlay — is the real one. */
const maler = vi.hoisted(() => ({
  render: vi.fn(),
  dispose: vi.fn(),
  setSize: vi.fn(),
  shadowMap: { needsUpdate: false },
}));
/* The life that was made, so that a test can put somebody on a route or next
   to somebody else — what the data or a stroll would do, at a time of the
   test's choosing. */
const lebenDa = vi.hoisted(() => ({ l: null as import("./buero/leben").Leben | null }));
vi.mock("./buero/leben", async (orig) => {
  const echt = await orig<typeof import("./buero/leben")>();
  return {
    ...echt,
    erschaffeLeben: (...a: Parameters<typeof echt.erschaffeLeben>) => (lebenDa.l = echt.erschaffeLeben(...a)),
  };
});
vi.mock("./buero/kamera", async (orig) => ({
  ...(await orig<typeof import("./buero/kamera")>()),
  erschaffeMaler: (buehne: HTMLElement) => {
    const domElement = document.createElement("canvas");
    buehne.prepend(domElement);
    return { ...maler, domElement };
  },
}));

import Buero from "./Buero";

const abteilung = { id: "d1", org_id: "o", name: "Support", description: "", color: "", leads: [] } as unknown as Department;
const leute = ["a", "b", "c"].map(
  (k) => ({ id: `id-${k}`, slug: `agent-${k}`, display_name: `Agent ${k}`, status: "sleeping", killed: false, department_id: "d1" }) as unknown as Agent,
);

const wach = leute.map((a) => ({ ...a, status: "active" }) as Agent);

/* ResizeObserver in the hand: what the browser would report for an element,
   when the test says so. A wrapper whose only child is absolutely positioned
   reports 0 × 0 — the real thing, not a simplification. */
const beobachter = new Set<{ cb: ResizeObserverCallback; ziele: Set<Element> }>();
function melden(el: Element, b: number, h: number) {
  for (const o of beobachter)
    if (o.ziele.has(el))
      o.cb([{ target: el, borderBoxSize: [{ inlineSize: b, blockSize: h }], contentRect: { width: b, height: h } } as unknown as ResizeObserverEntry], null as never);
}

/* requestAnimationFrame in the hand: what is queued is what the browser
   would be asked to run. */
let warteschlange: Map<number, FrameRequestCallback>;
let n: number;
/* The clock is the test's too: a frame is 16 ms after the one before, however
   fast or slow the machine runs the test. The camera's ride and the life's
   step are functions of that time. */
let jetzt: number;
function rahmenLaufen() {
  const dran = [...warteschlange];
  warteschlange.clear();
  jetzt += 16;
  for (const [, cb] of dran) cb(jetzt);
}
async function beruhigen() {
  for (let i = 0; i < 60 && warteschlange.size; i++) await act(async () => rahmenLaufen());
}

describe("the office's frame loop", () => {
  beforeEach(() => {
    useGerman();
    warteschlange = new Map();
    n = 0;
    jetzt = 1000;
    vi.spyOn(performance, "now").mockImplementation(() => jetzt);
    maler.render.mockReset();
    // As three.js does: a drawn shadow map is not drawn again unless asked.
    maler.render.mockImplementation(() => void (maler.shadowMap.needsUpdate = false));
    maler.shadowMap.needsUpdate = false;
    lebenDa.l = null;
    beobachter.clear();
    vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => (warteschlange.set(++n, cb), n));
    vi.stubGlobal("cancelAnimationFrame", (id: number) => void warteschlange.delete(id));
    vi.stubGlobal(
      "ResizeObserver",
      class {
        o: { cb: ResizeObserverCallback; ziele: Set<Element> };
        constructor(cb: ResizeObserverCallback) {
          this.o = { cb, ziele: new Set() };
          beobachter.add(this.o);
        }
        observe(el: Element) { this.o.ziele.add(el); }
        unobserve(el: Element) { this.o.ziele.delete(el); }
        disconnect() { this.o.ziele.clear(); beobachter.delete(this.o); }
      },
    );
    mockFetch({ "/org": { office_furnishing: "normal" }, "/auth/me/profile": { department_id: null } });
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  type Eigen = { agents: Agent[]; laufend: Laufend[]; wartetBei: Set<string> };
  let aendern: (e: Partial<Eigen>) => void;
  function Haus(a: Eigen) {
    const [e, setzen] = useState(a);
    aendern = (neu) => setzen((alt) => ({ ...alt, ...neu }));
    return <Buero agents={e.agents} departments={[abteilung]} laufend={e.laufend} wartetBei={e.wartetBei} me={testPrincipal()} />;
  }
  function zeigen(agents: Agent[] = leute) {
    return renderWithProviders(<Haus agents={agents} laufend={[]} wartetBei={new Set()} />);
  }
  /* The life's clock and the takt's timers together: `ms` pass, in steps of
     `schritt`; frames that were asked for run when the browser would run them. */
  async function vergehen(ms: number, schritt = 16) {
    for (let t = 0; t < ms; t += schritt) {
      jetzt += schritt;
      await act(async () => {
        // The frames asked for before this step run at its vsync; what the
        // timers ask for now runs at the next one, as in the browser.
        const dran = [...warteschlange];
        warteschlange.clear();
        vi.advanceTimersByTime(schritt);
        for (const [, cb] of dran) cb(jetzt);
      });
    }
  }

  it("queues nothing once the scene has settled, and one run for a change of the camera", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    const { container } = zeigen();
    await beruhigen();
    expect(warteschlange.size).toBe(0);
    expect(maler.render).toHaveBeenCalled();

    const gezeichnet = maler.render.mock.calls.length;
    // Nobody is awake: there is nothing for the life to do, and nothing is drawn.
    // (No guest turns up by chance in these seconds.)
    vi.spyOn(Math, "random").mockReturnValue(0.9999);
    const schritte = vi.spyOn(lebenDa.l!, "tick");
    await vergehen(600, 250);
    vi.mocked(Math.random).mockRestore();
    // The life was given its time ...
    expect(schritte.mock.calls.length).toBeGreaterThanOrEqual(2);
    // ... and, with nobody moving, nothing was drawn.
    expect(warteschlange.size).toBe(0);
    expect(maler.render.mock.calls.length).toBe(gezeichnet);

    // A key on the stage moves the camera: exactly one more run.
    fireEvent.keyDown(window, { key: "ArrowLeft" });
    expect(warteschlange.size).toBe(1);
    fireEvent.keyDown(window, { key: "ArrowLeft" });
    expect(warteschlange.size).toBe(1);
    await beruhigen();
    expect(maler.render.mock.calls.length).toBe(gezeichnet + 1);
    expect(warteschlange.size).toBe(0);

    // So does the pointer: the faces look after it.
    fireEvent.pointerMove(container.querySelector(".bu-buehne")!, { clientX: 40, clientY: 40 });
    expect(warteschlange.size).toBe(1);
    await beruhigen();
    expect(warteschlange.size).toBe(0);
  });

  it("rides on frame by frame during a quarter turn, then stops", async () => {
    const { getByTitle } = zeigen();
    await beruhigen();
    fireEvent.click(getByTitle(/Nach links drehen/));
    expect(warteschlange.size).toBe(1);
    let bilder = 0;
    while (warteschlange.size && bilder < 200) {
      bilder++;
      await act(async () => rahmenLaufen());
    }
    expect(bilder).toBeGreaterThan(2);
    expect(bilder).toBeLessThan(200);
    expect(warteschlange.size).toBe(0);
  });

  it("reads no size from the page while it draws a frame", async () => {
    const { container, getByTitle } = zeigen();
    await beruhigen();
    // Open a card too: its height is the other thing the frame used to read.
    fireEvent.click(container.querySelector("button.bu-wer")!);
    await beruhigen();
    // The pointer handler reads the stage's position — an event, not a frame:
    // it runs before the spies, the frame it asks for runs under them.
    fireEvent.pointerMove(container.querySelector(".bu-buehne")!, { clientX: 40, clientY: 40 });
    const gelesen = [
      vi.spyOn(HTMLElement.prototype, "offsetWidth", "get"),
      vi.spyOn(HTMLElement.prototype, "offsetHeight", "get"),
      vi.spyOn(Element.prototype, "clientWidth", "get"),
      vi.spyOn(Element.prototype, "clientHeight", "get"),
      vi.spyOn(Element.prototype, "getBoundingClientRect"),
    ];
    fireEvent.click(getByTitle(/Nach links drehen/));
    const davor = maler.render.mock.calls.length;
    for (let i = 0; i < 6; i++) await act(async () => rahmenLaufen());
    await beruhigen();
    expect(maler.render.mock.calls.length).toBeGreaterThan(davor + 2);
    expect(gelesen.map((g) => g.mock.calls.length)).toEqual([0, 0, 0, 0, 0]);
  });

  it("stops while the tab is hidden and draws once on return", async () => {
    zeigen();
    await beruhigen();
    const gezeichnet = maler.render.mock.calls.length;
    Object.defineProperty(document, "hidden", { value: true, configurable: true });
    document.dispatchEvent(new Event("visibilitychange"));
    fireEvent.keyDown(window, { key: "ArrowLeft" });
    expect(warteschlange.size).toBe(0);
    Object.defineProperty(document, "hidden", { value: false, configurable: true });
    document.dispatchEvent(new Event("visibilitychange"));
    expect(warteschlange.size).toBe(1);
    await beruhigen();
    expect(maler.render.mock.calls.length).toBe(gezeichnet + 1);
  });

  it("follows the 'reduced motion' setting when it changes, not only at the start", async () => {
    let aendern: (() => void) | undefined;
    const medium = { matches: false, addEventListener: (_: string, f: () => void) => (aendern = f), removeEventListener: () => {} };
    vi.stubGlobal("matchMedia", (q: string) => (q.includes("reduced-motion") ? medium : { matches: false, addEventListener() {}, removeEventListener() {} }));
    const { container } = zeigen();
    await beruhigen();
    expect(container.querySelector(".bu-buehne.ruht")).toBeNull();
    medium.matches = true;
    await act(async () => aendern?.());
    await beruhigen();
    expect(container.querySelector(".bu-buehne.ruht")).not.toBeNull();
    expect(warteschlange.size).toBe(0);
  });

  /* What React changes and the frame has to put on the page. Each of these
     wakes the takt by its own path; the tests below fail when one is gone. */
  describe("wakes one frame for", () => {
    it("a click on a colleague, and puts the card above the head by its own height", async () => {
      // The stage is as large as a desktop's; the card's clamp must not bite.
      vi.spyOn(Element.prototype, "clientWidth", "get").mockReturnValue(1000);
      vi.spyOn(Element.prototype, "clientHeight", "get").mockReturnValue(2000);
      const { container } = zeigen();
      await beruhigen();
      expect(warteschlange.size).toBe(0);
      const gezeichnet = maler.render.mock.calls.length;
      fireEvent.click(container.querySelector("button.bu-wer")!);
      expect(warteschlange.size).toBe(1);
      const huelle = container.querySelector<HTMLElement>(".bu-karte-huelle")!;
      // The wrapper is a 0 × 0 box — its child, the card, is absolute. The
      // card is what has a height.
      const karte = huelle.firstElementChild!;
      const ort = (el: HTMLElement) => /translate3d\((-?[\d.]+)px, (-?[\d.]+)px/.exec(el.style.transform)!.slice(1).map(Number);
      // Before anything is measured, and when the browser reports no height,
      // the card is taken to be 170 high; the page shows a card, not a point.
      await beruhigen();
      expect(maler.render.mock.calls.length).toBe(gezeichnet + 1);
      const [, y] = ort(container.querySelector<HTMLElement>("button.bu-wer.gewaehlt")!);
      expect(ort(huelle)[1]).toBeCloseTo(y - 170 - 34);
      act(() => melden(karte, 0, 0));
      await beruhigen();
      expect(ort(huelle)[1]).toBeCloseTo(y - 170 - 34);
      act(() => {
        melden(huelle, 0, 0);
        melden(karte, 264, 220);
      });
      expect(warteschlange.size).toBe(1);
      await beruhigen();
      const [, ky] = ort(huelle);
      expect(y - 220 - 34).toBeGreaterThan(8);
      expect(ky).toBeCloseTo(y - 220 - 34);
    });

    it("a change of state that rebuilds: the shadow map is drawn once, with that frame", async () => {
      zeigen();
      await beruhigen();
      expect(maler.shadowMap.needsUpdate).toBe(false);
      // Somebody starts working: their screen lights up, which is geometry.
      act(() => aendern({ laufend: [{ agent_id: "id-a", step: "x" } as unknown as Laufend] }));
      expect(maler.shadowMap.needsUpdate).toBe(true);
      expect(warteschlange.size).toBe(1);
      const gezeichnet = maler.render.mock.calls.length;
      await beruhigen();
      expect(maler.render.mock.calls.length).toBe(gezeichnet + 1);
      expect(maler.shadowMap.needsUpdate).toBe(false);
      // A turn of the camera is not a change of the scene: no new shadow map.
      fireEvent.keyDown(window, { key: "ArrowLeft" });
      await beruhigen();
      expect(maler.shadowMap.needsUpdate).toBe(false);
    });

    it("a resize of the stage: the canvas was cleared and is drawn again", async () => {
      const { container } = zeigen();
      await beruhigen();
      expect(warteschlange.size).toBe(0);
      act(() => melden(container.querySelector(".bu-buehne")!, 800, 500));
      expect(warteschlange.size).toBe(1);
    });
  });

  describe("the life on the shown floor", () => {
    beforeEach(() => {
      vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
      vi.spyOn(Math, "random").mockReturnValue(0.9999); // no guest, no stroll of its own
    });

    it("keeps the frames coming while somebody walks, and stops when they arrive", async () => {
      zeigen(wach);
      await beruhigen();
      const l = lebenDa.l!;
      for (const f of l.figuren) f.pause = 1e9;
      const f = l.figuren[0];
      f.route = [{ x: f.pos.x + 20000, y: f.pos.y }];
      const davor = maler.render.mock.calls.length;
      // The walk starts in the life's slow tick; from then on the frames are the life's.
      await vergehen(1500);
      const bilder = maler.render.mock.calls.length - davor;
      // At most 30 a second, and far more than the life's four ticks a second.
      expect(bilder).toBeGreaterThan(20);
      expect(bilder).toBeLessThanOrEqual(1500 / 33 + 2);
      expect(f.route.length).toBeGreaterThan(0);

      f.route = [];
      await vergehen(200);
      const stand = maler.render.mock.calls.length;
      await vergehen(2000);
      expect(maler.render.mock.calls.length).toBe(stand);
      expect(warteschlange.size).toBe(0);
    });

    it("draws two who stand too close and are pushed apart, without anyone walking", async () => {
      zeigen(wach);
      await beruhigen();
      const l = lebenDa.l!;
      for (const f of l.figuren) f.pause = 1e9;
      const [a, b] = l.figuren;
      // Two in the queue at the front desk, one on top of the other: a push, not a walk.
      for (const [f, dy] of [[a, 0], [b, 10]] as const) {
        f.zustand = "wartet";
        f.route = [];
        f.pos = { x: a.sitz.x + 100, y: a.sitz.y + 100 + dy };
      }
      expect(warteschlange.size).toBe(0);
      const davor = maler.render.mock.calls.length;
      // One slow tick of the life, which pushes them apart and draws nothing itself ...
      await vergehen(300, 50);
      expect(Math.hypot(a.pos.x - b.pos.x, a.pos.y - b.pos.y)).toBeGreaterThan(10);
      // ... and the buttons are moved to where they stand now.
      expect(maler.render.mock.calls.length).toBeGreaterThan(davor);
      // Settled: no more frames.
      await vergehen(1500, 50);
      const stand = maler.render.mock.calls.length;
      await vergehen(1000, 50);
      expect(maler.render.mock.calls.length).toBe(stand);
    });
  });
});
