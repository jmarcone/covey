import { useEffect, useRef } from "react";

/* A row that scrolls sideways shows where it is clipped. The hook keeps
 * `data-fade` on the element at "start", "end", "both" or "" (nothing is
 * clipped); app.css turns that into a mask on the clipped edge. The attribute
 * is written directly, not through state: scrolling would otherwise render the
 * component on every frame. `deps` re-measures when the content changes. */
export function useEdgeFade<T extends HTMLElement>(deps: unknown[] = []) {
  const ref = useRef<T>(null);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const measure = () => {
      const start = el.scrollLeft > 1;
      const end = el.scrollLeft + el.clientWidth < el.scrollWidth - 1;
      el.dataset.fade = start && end ? "both" : start ? "start" : end ? "end" : "";
    };
    measure();
    el.addEventListener("scroll", measure, { passive: true });
    // The box changes with the window, the content with its children.
    const ro = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(measure);
    ro?.observe(el);
    for (const child of Array.from(el.children)) ro?.observe(child);
    return () => {
      el.removeEventListener("scroll", measure);
      ro?.disconnect();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps);
  return ref;
}
