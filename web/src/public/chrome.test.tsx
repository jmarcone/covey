import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render } from "@testing-library/react";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { BOIDS_FPS, PublicBackground } from "./chrome";

/* The sign-in page ran at single-digit fps (#134). The swarm's three limits
   are testable without a browser: how often it draws, when it stands still,
   and at which resolution. The aurora and the focus ring are in the stylesheet
   and are read from the file, as theme.test.ts does. */

let queue = new Map<number, FrameRequestCallback>();
let nextId = 1;
let clock = 0;
let clears = 0;
let focused = true;
let reduced = false;

// Runs the frames a display at the given rate would deliver for `ms`.
function run(ms: number, hz = 60) {
  const gap = 1000 / hz;
  const end = clock + ms;
  while (clock + gap <= end) {
    clock += gap;
    const due = [...queue.entries()];
    queue = new Map();
    for (const [, cb] of due) cb(clock);
  }
}

beforeEach(() => {
  queue = new Map();
  nextId = 1;
  clock = 1000;
  clears = 0;
  focused = true;
  reduced = false;
  vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => {
    queue.set(nextId, cb);
    return nextId++;
  });
  vi.stubGlobal("cancelAnimationFrame", (id: number) => queue.delete(id));
  vi.stubGlobal("matchMedia", () => ({ matches: reduced }));
  vi.spyOn(document, "hasFocus").mockImplementation(() => focused);
  HTMLCanvasElement.prototype.getContext = (() =>
    new Proxy(
      { clearRect: () => clears++ },
      { get: (t, k) => (k in t ? (t as never)[k] : () => {}), set: () => true },
    )) as never;
  Object.defineProperty(window, "devicePixelRatio", { value: 2, configurable: true });
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  Object.defineProperty(document, "hidden", { value: false, configurable: true });
});

function mount() {
  return render(
    <>
      <PublicBackground />
      <div className="login-card">
        <input data-testid="in" />
      </div>
      <button data-testid="out">x</button>
    </>,
  );
}

describe("useBoids", () => {
  it("draws at most BOIDS_FPS times a second, even on a 144 Hz display", () => {
    mount();
    const before = clears;
    run(1000, 144);
    const drawn = clears - before;
    expect(drawn).toBeGreaterThan(BOIDS_FPS - 4);
    expect(drawn).toBeLessThanOrEqual(BOIDS_FPS + 1);
  });

  it("draws at 30 fps on a 60 Hz display", () => {
    mount();
    const before = clears;
    run(1000, 60);
    expect(clears - before).toBeGreaterThan(BOIDS_FPS - 3);
    expect(clears - before).toBeLessThanOrEqual(BOIDS_FPS + 1);
  });

  it("keeps the canvas at one pixel per CSS pixel, however dense the screen", () => {
    const { container } = mount();
    const c = container.querySelector("canvas")!;
    expect(c.width).toBe(window.innerWidth);
    expect(c.height).toBe(window.innerHeight);
  });

  it("stops while a field of the sign-in card has focus and goes on after", () => {
    const { getByTestId } = mount();
    run(200);
    const input = getByTestId("in");
    act(() => input.focus());
    expect(queue.size).toBe(0);
    const frozen = clears;
    run(500);
    expect(clears).toBe(frozen);
    act(() => getByTestId("out").focus());
    run(200);
    expect(clears).toBeGreaterThan(frozen);
  });

  it("does not stop for a field outside the sign-in card", () => {
    const { container } = render(
      <>
        <PublicBackground />
        <input data-testid="free" />
      </>,
    );
    const free = container.querySelector<HTMLInputElement>("[data-testid=free]")!;
    act(() => free.focus());
    expect(queue.size).toBe(1);
  });

  it("stands still while the tab is hidden and resumes when it is shown", () => {
    mount();
    Object.defineProperty(document, "hidden", { value: true, configurable: true });
    act(() => void document.dispatchEvent(new Event("visibilitychange")));
    expect(queue.size).toBe(0);
    Object.defineProperty(document, "hidden", { value: false, configurable: true });
    act(() => void document.dispatchEvent(new Event("visibilitychange")));
    expect(queue.size).toBe(1);
  });

  it("stands still while the window has lost focus", () => {
    mount();
    act(() => void window.dispatchEvent(new Event("blur")));
    expect(queue.size).toBe(0);
    act(() => void window.dispatchEvent(new Event("focus")));
    expect(queue.size).toBe(1);
  });

  it("does not start in a window that has no focus yet", () => {
    focused = false;
    mount();
    expect(queue.size).toBe(0);
  });

  it("does not run at all with reduced motion", () => {
    reduced = true;
    mount();
    expect(queue.size).toBe(0);
  });

  it("leaves no frame behind when it unmounts", () => {
    const { unmount } = mount();
    unmount();
    expect(queue.size).toBe(0);
  });
});

describe("sign-in page styles", () => {
  const css = readFileSync(resolve(__dirname, "..", "styles.css"), "utf8");
  // The declarations of every rule whose selector list mentions `sel`.
  const rules = (sel: string) =>
    [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)]
      .filter((m) => m[1].includes(sel))
      .map((m) => m[2]);

  it("never blurs the aurora: a filter on a layer with moving children re-blurs the viewport every frame", () => {
    const own = rules(".aurora").concat(rules(".aurora i"));
    expect(own.length).toBeGreaterThan(0);
    for (const body of own) expect(body).not.toMatch(/(^|[;\s])(backdrop-)?filter\s*:/);
  });

  it("animates the aurora by transform only", () => {
    const keys = [...css.matchAll(/@keyframes aurora-[abc]\s*\{([\s\S]*?\})\s*\}/g)];
    expect(keys).toHaveLength(3);
    for (const k of keys) expect(k[1].replace(/transform\s*:[^;]*;/g, "")).not.toMatch(/[a-z-]+\s*:/);
  });

  it("gives the sign-in card no animated border on focus, a static ring instead", () => {
    const withBefore = css.match(/[^{}]*\.login-card::before[^{}]*\{/g);
    expect(withBefore).toBeNull();
    expect(css).not.toMatch(/\.login-card:focus-within::before/);
    const ring = rules(".login-card:focus-within").join(";");
    expect(ring).toMatch(/border-color/);
    expect(ring).toMatch(/box-shadow/);
    expect(ring).not.toMatch(/animation/);
  });

  it("switches the aurora's motion off for reduced motion", () => {
    const block = css.slice(css.indexOf("@media (prefers-reduced-motion: reduce)"));
    expect(block).toMatch(/\.aurora i[\s\S]*animation: none/);
    expect(block).toMatch(/\.boids \{ display: none; \}/);
  });
});
