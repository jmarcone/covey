import { useId } from "react";

export function Seg<T extends string | number>({
  value,
  options,
  onChange,
}: {
  value: T;
  options: { v: T; label: string; disabled?: boolean; title?: string }[];
  onChange: (v: T) => void;
}) {
  // A disabled option says why in visible text under the switch, and is
  // described by it (not by its title alone, which a touch screen or a
  // keyboard never shows).
  const uid = useId().replace(/[^a-zA-Z0-9_-]/g, "");
  const reasons = [...new Set(options.filter((o) => o.disabled && o.title).map((o) => o.title!))];
  const reasonId = (title?: string) => (title && reasons.includes(title) ? `${uid}-r${reasons.indexOf(title)}` : undefined);
  return (
    <span style={{ display: "inline-flex", flexDirection: "column", alignItems: "flex-end", maxWidth: "100%" }}>
    {/* One option per agent grows past any width with a large fleet: the
       control scrolls inside itself rather than widening the page (#463). */}
    <div style={{ display: "inline-flex", gap: 2, maxWidth: "100%", overflowX: "auto", scrollbarWidth: "thin", background: "var(--surface-1)", border: "0.5px solid var(--border)", borderRadius: 8, padding: 2 }}>
      {options.map((o) => {
        const active = o.v === value;
        return (
          <button
            type="button"
            key={String(o.v)}
            onClick={() => onChange(o.v)}
            aria-pressed={active}
            disabled={o.disabled}
            title={o.title}
            aria-describedby={o.disabled ? reasonId(o.title) : undefined}
            style={{
              border: "none",
              flexShrink: 0,
              whiteSpace: "nowrap",
              borderRadius: 6,
              padding: "5px 12px",
              fontSize: 13,
              cursor: o.disabled ? "not-allowed" : "pointer",
              opacity: o.disabled ? 0.45 : 1,
              background: active ? "var(--surface-2)" : "transparent",
              color: active ? "var(--text-primary)" : "var(--text-secondary)",
              fontWeight: active ? 600 : 400,
              boxShadow: active ? "0 1px 3px var(--shadow-xs)" : "none",
            }}
          >
            {o.label}
          </button>
        );
      })}
    </div>
    {reasons.map((r, i) => (
      <span key={r} id={`${uid}-r${i}`} className="muted" data-seg-reason style={{ fontSize: 11, marginTop: 2, textAlign: "right" }}>{r}</span>
    ))}
    </span>
  );
}
