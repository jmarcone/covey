import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { render } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { BOIDS_FPS, PublicBackground, boidsFrameDue, typingInCard } from "./chrome";

/* The sign-in background must not cost the page its responsiveness (#134).
   jsdom paints nothing, so what is testable is the switch in front of the
   paint: whether the loop runs at all, at which size and how often. */

describe("boidsFrameDue", () => {
  it("lets the first frame and every 33 ms through, and skips the ticks between", () => {
    expect(BOIDS_FPS).toBe(30);
    expect(boidsFrameDue(1000, 0)).toBe(true);
    // A 60 Hz display ticks every 16.7 ms: one in two draws, not one in three.
    expect(boidsFrameDue(1016.7, 1000)).toBe(false);
    expect(boidsFrameDue(1033.4, 1000)).toBe(true);
    // 144 Hz: 6.9 ms per tick.
    expect(boidsFrameDue(1006.9, 1000)).toBe(false);
    expect(boidsFrameDue(1034.7, 1000)).toBe(true);
  });
});

describe("typingInCard", () => {
  afterEach(() => {
    document.body.innerHTML = "";
  });
  it("is true for a field in the card only", () => {
    document.body.innerHTML = `<div class="login-card"><input id="a"/><button id="b"></button></div><input id="c"/>`;
    expect(typingInCard(document.getElementById("a"))).toBe(true);
    expect(typingInCard(document.getElementById("b"))).toBe(false);
    expect(typingInCard(document.getElementById("c"))).toBe(false);
    expect(typingInCard(null)).toBe(false);
  });
});

describe("useBoids", () => {
  let raf: ReturnType<typeof vi.fn>;
  let caf: ReturnType<typeof vi.fn>;
  let focused: boolean;
  let hidden: boolean;
  // The step functions the raf mock was handed, so a test can run a frame.
  let steps: ((t: number) => void)[];
  let ctx: Record<string, ReturnType<typeof vi.fn>>;

  beforeEach(() => {
    steps = [];
    raf = vi.fn((cb: (t: number) => void) => {
      steps.push(cb);
      return steps.length;
    });
    caf = vi.fn();
    vi.stubGlobal("requestAnimationFrame", raf);
    vi.stubGlobal("cancelAnimationFrame", caf);
    vi.stubGlobal("matchMedia", () => ({ matches: false }));
    vi.stubGlobal("devicePixelRatio", 2);
    focused = true;
    hidden = false;
    vi.spyOn(document, "hasFocus").mockImplementation(() => focused);
    vi.spyOn(document, "hidden", "get").mockImplementation(() => hidden);
    ctx = Object.fromEntries(
      ["clearRect", "save", "restore", "translate", "rotate", "beginPath", "moveTo", "lineTo", "stroke"].map(
        (k) => [k, vi.fn()],
      ),
    );
    vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue(ctx as unknown as CanvasRenderingContext2D);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    document.body.innerHTML = "";
  });

  it("draws one canvas pixel per CSS pixel, even at DPR 2", () => {
    const { container } = render(<PublicBackground />);
    const canvas = container.querySelector("canvas")!;
    expect(canvas.width).toBe(window.innerWidth);
    expect(canvas.height).toBe(window.innerHeight);
    expect(raf).toHaveBeenCalledTimes(1);
  });

  it("does not start in a hidden tab or a window without focus, and resumes", () => {
    hidden = true;
    render(<PublicBackground />);
    expect(raf).not.toHaveBeenCalled();
    hidden = false;
    document.dispatchEvent(new Event("visibilitychange"));
    expect(raf).toHaveBeenCalledTimes(1);
    focused = false;
    window.dispatchEvent(new Event("blur"));
    expect(caf).toHaveBeenCalledTimes(1);
    focused = true;
    window.dispatchEvent(new Event("focus"));
    expect(raf).toHaveBeenCalledTimes(2);
  });

  it("stops while a field of the card has the focus and resumes when it leaves", () => {
    document.body.innerHTML = `<div class="login-card"><input id="email"/></div>`;
    render(<PublicBackground />);
    expect(raf).toHaveBeenCalledTimes(1);
    document.getElementById("email")!.focus();
    expect(caf).toHaveBeenCalledTimes(1);
    document.getElementById("email")!.blur();
    expect(raf).toHaveBeenCalledTimes(2);
  });

  it("redraws the standing frame when a resize clears the canvas while paused", () => {
    document.body.innerHTML = `<div class="login-card"><input id="email"/></div>`;
    render(<PublicBackground />);
    steps[steps.length - 1](1000);
    document.getElementById("email")!.focus();
    const strokes = ctx.stroke.mock.calls.length;
    // Assigning canvas.width clears the bitmap (HTML spec); no step runs now.
    window.dispatchEvent(new Event("resize"));
    expect(ctx.stroke.mock.calls.length).toBeGreaterThan(strokes);
  });

  it("every painted frame after a resize shows the swarm while the loop runs", () => {
    render(<PublicBackground />);
    steps[steps.length - 1](1000);
    const before = ctx.stroke.mock.calls.length;
    window.dispatchEvent(new Event("resize")); // fires before the frame callbacks
    steps[steps.length - 1](1016.7); // a tick the 30 fps cap skips
    expect(ctx.stroke.mock.calls.length).toBeGreaterThan(before);
  });

  it("paints the swarm once before the loop may run (no frame scheduled)", () => {
    focused = false;
    render(<PublicBackground />);
    expect(raf).not.toHaveBeenCalled();
    expect(ctx.stroke).toHaveBeenCalled();
  });

  it("draws at most every other 60 Hz tick (30 fps cap in the loop)", () => {
    render(<PublicBackground />);
    const clears = ctx.clearRect.mock.calls.length;
    steps[steps.length - 1](1000);
    steps[steps.length - 1](1016.7);
    steps[steps.length - 1](1033.4);
    expect(ctx.clearRect.mock.calls.length - clears).toBe(2);
  });

  it("cancels the pending frame at unmount", () => {
    const { unmount } = render(<PublicBackground />);
    unmount();
    expect(caf).toHaveBeenCalledTimes(1);
  });

  it("leaves no listener and no frame behind at unmount, even from a pause", () => {
    // Signing in with Enter in a field unmounts the card while the loop is
    // paused (raf = 0); a leaked listener would restart it on a detached canvas.
    document.body.innerHTML = `<div class="login-card"><input id="email"/></div>`;
    const { unmount } = render(<PublicBackground />);
    document.getElementById("email")!.focus();
    unmount();
    const started = raf.mock.calls.length;
    const cancelled = caf.mock.calls.length;
    const strokes = ctx.stroke.mock.calls.length;
    document.getElementById("email")!.blur();
    document.dispatchEvent(new Event("visibilitychange"));
    window.dispatchEvent(new Event("blur"));
    window.dispatchEvent(new Event("focus"));
    window.dispatchEvent(new Event("resize"));
    expect(raf.mock.calls.length).toBe(started);
    expect(caf.mock.calls.length).toBe(cancelled);
    expect(ctx.stroke.mock.calls.length).toBe(strokes);
  });

  it("does not start at prefers-reduced-motion", () => {
    vi.stubGlobal("matchMedia", () => ({ matches: true }));
    render(<PublicBackground />);
    expect(raf).not.toHaveBeenCalled();
  });
});

describe("sign-in stylesheet (#134)", () => {
  // Read as text: a stylesheet import comes back empty under vitest.
  const css = readFileSync(resolve(__dirname, "../styles.css"), "utf8");
  const rule = (selector: string) => {
    const m = css.match(new RegExp(`(?:^|\\n)${selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}\\s*\\{([^}]*)\\}`));
    if (!m) throw new Error(`no rule for ${selector}`);
    return m[1];
  };

  it("never blurs the aurora or its moving circles", () => {
    const rules = [...css.matchAll(/(?:^|\n)\s*([^{}]*\.aurora[^{}]*)\{([^}]*)\}/g)];
    expect(rules.length).toBeGreaterThan(1);
    for (const [, selector, body] of rules) {
      expect(body, selector).not.toMatch(/filter\s*:/);
    }
  });

  it("gives the card a static focus ring, not a spinning one", () => {
    expect(css).not.toMatch(/\.login-card::before/);
    expect(rule(".login-card:focus-within")).toMatch(/border-color/);
    expect(rule(".login-card:focus-within")).toMatch(/box-shadow/);
  });

  it("drops the card's transition at prefers-reduced-motion", () => {
    const m = css.match(/@media \(prefers-reduced-motion: reduce\) \{([\s\S]*?)\n\}/);
    expect(m).not.toBeNull();
    expect(m![1]).toMatch(/\.login-card\s*\{\s*transition:\s*none/);
  });
});
