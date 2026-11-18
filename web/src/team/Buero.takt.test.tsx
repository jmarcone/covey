import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent } from "@testing-library/react";
import type { Agent, Department } from "../api";
import { mockFetch, renderWithProviders, testPrincipal, useGerman } from "../test/render";

/* The renderer is the one thing replaced: jsdom has no WebGL. Everything else
   — the camera, the life, the takt, the overlay — is the real one. */
const maler = vi.hoisted(() => ({
  render: vi.fn(),
  dispose: vi.fn(),
  setSize: vi.fn(),
  shadowMap: { needsUpdate: false },
}));
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

/* requestAnimationFrame in the hand: what is queued is what the browser
   would be asked to run. */
let warteschlange: Map<number, FrameRequestCallback>;
let n: number;
function rahmenLaufen() {
  const dran = [...warteschlange];
  warteschlange.clear();
  for (const [, cb] of dran) cb(performance.now());
}
async function beruhigen() {
  for (let i = 0; i < 60 && warteschlange.size; i++) await act(async () => rahmenLaufen());
}

describe("the office's frame loop", () => {
  beforeEach(() => {
    useGerman();
    warteschlange = new Map();
    n = 0;
    maler.render.mockClear();
    vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => (warteschlange.set(++n, cb), n));
    vi.stubGlobal("cancelAnimationFrame", (id: number) => void warteschlange.delete(id));
    vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} });
    mockFetch({ "/org": { office_furnishing: "normal" }, "/auth/me/profile": { department_id: null } });
  });

  function zeigen() {
    return renderWithProviders(<Buero agents={leute} departments={[abteilung]} laufend={[]} wartetBei={new Set()} me={testPrincipal()} />);
  }

  it("queues nothing once the scene has settled, and one run for a change of the camera", async () => {
    const { container } = zeigen();
    await beruhigen();
    expect(warteschlange.size).toBe(0);
    expect(maler.render).toHaveBeenCalled();

    const gezeichnet = maler.render.mock.calls.length;
    // Nobody is awake: there is nothing for the life to do, and nothing is drawn.
    await new Promise((r) => setTimeout(r, 600));
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
});
