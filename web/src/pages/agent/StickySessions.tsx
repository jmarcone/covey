import { useState, type CSSProperties } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { patch, type Agent, type StickyPolicy } from "../../api";

/* Sticky sessions (#130): a developer's fix round on the same pull request
   resumes the session of its previous round instead of reading everything
   again; the reviewer stays fresh (a review round is a fresh adversarial
   review). The server takes policy, window and cap together, so every change
   sends all three — the others as they stand. */

export const STICKY_DEFAULTS = { policy: "fresh" as StickyPolicy, window: 55, cap: 200_000 };
const POLICIES: StickyPolicy[] = ["fresh", "conditional", "always", "ab"];

export type StickyBody = { policy: StickyPolicy; window_minutes: number; context_cap: number };

export function stickyOf(agent: Agent): StickyBody {
  return {
    policy: agent.sticky_policy ?? STICKY_DEFAULTS.policy,
    window_minutes: agent.sticky_window_minutes ?? STICKY_DEFAULTS.window,
    context_cap: agent.sticky_context_cap ?? STICKY_DEFAULTS.cap,
  };
}

export function StickySessions({ agent, editable, row }: { agent: Agent; editable: boolean; row: CSSProperties }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const cur = stickyOf(agent);
  // A refused change must not stay on screen as if it were set: the select
  // shows what is saved (or what is being saved), and a failed save remounts
  // the number fields with the saved values.
  const [epoch, setEpoch] = useState(0);
  const save = useMutation({
    mutationFn: (body: StickyBody) => patch(`/agents/${agent.id}/sticky`, body),
    // Pending until the agent is read again, so the select never falls back
    // to the old value between the answer and the refetch.
    onSuccess: () =>
      Promise.all([
        qc.invalidateQueries({ queryKey: ["agent", agent.id] }),
        qc.invalidateQueries({ queryKey: ["agents"] }),
      ]),
    onError: () => setEpoch((e) => e + 1),
  });
  const shownPolicy = save.isPending && save.variables ? save.variables.policy : cur.policy;
  const change = (part: Partial<StickyBody>) => {
    const next = { ...cur, ...part };
    if (next.policy !== cur.policy || next.window_minutes !== cur.window_minutes || next.context_cap !== cur.context_cap) {
      save.mutate(next);
    }
  };
  const disabled = !editable || save.isPending;
  // The window and the cap only matter where a resume is conditional.
  const bounded = cur.policy === "conditional" || cur.policy === "ab";
  const label: CSSProperties = { display: "flex", alignItems: "center", gap: 6 };
  return (
    <div style={row} data-sticky>
      <span className="text-sm">{t("agent.settings.sticky")}</span>
      <div style={{ display: "flex", flexDirection: "column", gap: 6 }}>
        <select
          aria-label={t("agent.settings.sticky")}
          value={shownPolicy}
          disabled={disabled}
          onChange={(e) => change({ policy: e.target.value as StickyPolicy })}
        >
          {POLICIES.map((p) => (
            <option key={p} value={p}>
              {t(`agent.settings.stickyPolicy.${p}`)}
            </option>
          ))}
        </select>
        <label className="text-xs muted" style={label}>
          <input
            key={`stickyWindow:${cur.window_minutes}:${epoch}`}
            type="number"
            min={1}
            max={10080}
            aria-label={t("agent.settings.stickyWindow")}
            defaultValue={cur.window_minutes}
            disabled={disabled || !bounded}
            onBlur={(e) => {
              const v = Math.trunc(Number(e.target.value));
              if (v >= 1 && v <= 10080) change({ window_minutes: v });
              else e.target.value = String(cur.window_minutes);
            }}
            onKeyDown={(e) => e.key === "Enter" && (e.target as HTMLInputElement).blur()}
            className="mono"
            style={{ width: 90 }}
          />
          {t("agent.settings.stickyWindow")}
        </label>
        <label className="text-xs muted" style={label}>
          <input
            key={`stickyCap:${cur.context_cap}:${epoch}`}
            type="number"
            min={1}
            step={1000}
            aria-label={t("agent.settings.stickyCap")}
            defaultValue={cur.context_cap}
            disabled={disabled || !bounded}
            onBlur={(e) => {
              const v = Math.trunc(Number(e.target.value));
              if (v >= 1) change({ context_cap: v });
              else e.target.value = String(cur.context_cap);
            }}
            onKeyDown={(e) => e.key === "Enter" && (e.target as HTMLInputElement).blur()}
            className="mono"
            style={{ width: 90 }}
          />
          {t("agent.settings.stickyCap")}
        </label>
      </div>
      <span className="muted text-xs">
        {save.isError ? (
          <span style={{ color: "var(--error)" }}>{String((save.error as Error)?.message ?? "")}</span>
        ) : (
          t("agent.settings.stickyHint")
        )}
      </span>
    </div>
  );
}
