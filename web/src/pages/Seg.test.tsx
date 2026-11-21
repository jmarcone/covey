import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Seg } from "./Seg";

// jsdom has no layout: give the switch a width of 200 and its segments 100 each.
function layout(bar: HTMLElement) {
  Object.defineProperty(bar, "clientWidth", { value: 200, configurable: true });
  Object.defineProperty(bar, "scrollWidth", { value: 400, configurable: true });
  bar.getBoundingClientRect = () => ({ left: 0, right: 200 } as DOMRect);
  [...bar.querySelectorAll("button")].forEach((b, i) => {
    b.getBoundingClientRect = () => ({ left: i * 100 - bar.scrollLeft, right: i * 100 + 100 - bar.scrollLeft } as DOMRect);
  });
}

describe("Seg", () => {
  it("scrolls the chosen segment into view inside itself when the switch is wider than its room", () => {
    const options = [1, 2, 3, 4].map((v) => ({ v, label: `Option ${v}` }));
    const { container, rerender } = render(<Seg value={1} options={options} onChange={() => {}} />);
    const bar = container.querySelector("div div") as HTMLElement;
    layout(bar);
    rerender(<Seg value={4} options={options} onChange={() => {}} />);
    expect(bar.scrollLeft).toBe(204); // the end of the last segment (400) plus 4, less the width
    rerender(<Seg value={1} options={options} onChange={() => {}} />);
    expect(bar.scrollLeft).toBe(0);
  });
});
