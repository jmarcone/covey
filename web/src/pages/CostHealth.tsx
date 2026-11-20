import { useQuery } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { api, type CostHealthCheck, type CostHealthReport, type CostHealthStatus } from "../api";
import { fmtDelta, fmtNumber } from "../format";
import { uiLocale } from "../i18n";

/* Cost health (#178): whether the figures on the cost page can be trusted
 * right now. The server runs the checks (GET /cost/health, kept five minutes)
 * and decides the worst status; this file words them in the reader's
 * language, shows them on /costs/health, and gives the cost page its badge
 * and the "incomplete" marks. A red check never makes a figure smaller or
 * hides it: the part it affects is marked, and the figure stays. */

export const HEALTH_PATH = "/costs/health";

export function useCostHealth() {
  return useQuery({
    queryKey: ["cost", "health"],
    queryFn: () => api<CostHealthReport>("/cost/health"),
    // The server keeps a report five minutes; asking more often buys nothing.
    refetchInterval: 60_000,
  });
}

/** The check that gives the report its status — the first one with it, as
 *  the server's HealthReport.Worst; null when nothing warns or fails. */
export function worstCheck(rep: CostHealthReport | undefined): CostHealthCheck | null {
  if (!rep || (rep.status !== "warn" && rep.status !== "fail")) return null;
  return rep.checks.find((c) => c.status === rep.status) ?? null;
}

/** The parts of the cost page a failed check leaves incomplete: a stopped
 *  uploader (c1) the outside sessions; no meter reading at all (c2) or a drop
 *  nobody recorded (c3) the multiple. */
export function incompleteParts(rep: CostHealthReport | undefined): { outside: CostHealthCheck | null; multiple: CostHealthCheck | null } {
  const failed = (id: string) => rep?.checks.find((c) => c.id === id && c.status === "fail") ?? null;
  return { outside: failed("c1"), multiple: failed("c3") ?? failed("c2") };
}

const ago = (minutes: number | undefined) => (minutes == null ? "–" : fmtDelta(minutes * 60_000));
const when = (iso: string | undefined) => (iso ? new Date(iso).toLocaleString(uiLocale()) : "–");

/** The check's reason as a sentence. */
export function reasonText(t: TFunction, c: CostHealthCheck): string {
  const f = c.facts;
  const key = `costs.health.reason.${c.id}_${c.reason}`;
  switch (`${c.id}_${c.reason}`) {
    case "c1_fresh":
      return t(key, { ago: ago(f.last_push_minutes) });
    case "c1_stale":
      return t(key, {
        hosts: (f.stale_hosts ?? []).map((h) => `${h.source} @ ${h.host} (${ago(h.minutes)})`).join(", "),
        ago: ago(f.last_push_minutes),
      });
    case "c2_fresh":
    case "c2_stale":
      return t(c.reason === "stale" && f.fleet_killed ? `${key}Paused` : key,
        { ago: ago(f.reading_minutes), pct: f.week_percent != null ? `${fmtNumber(f.week_percent, 0)} %` : "–" });
    case "c3_drops":
      return t(key, { n: f.drops ?? 0, when: when(f.last_drop_at) });
    case "c4_duplicates":
      return t(key, { outside: f.external_duplicates ?? 0, agents: f.agent_duplicates ?? 0 });
    case "c5_priced":
      return t(key, { n: f.models ?? 0 });
    case "c5_unpriced":
      return t(key, { models: (f.unpriced ?? []).join(", ") });
    case "c6_missing":
      return t(key, { credentials: (f.credentials ?? []).join(", ") });
    case "c7_ok":
    case "c7_failed":
      return t(key, { ago: f.last_success_at ? fmtDelta(Date.parse(c.checked_at) - Date.parse(f.last_success_at)) : "–" });
    case "c8_pre_meter":
      return t(key, { n: f.calls ?? 0 });
    default:
      // A reason this build does not know (a newer server): the code, never nothing.
      return t(key, { defaultValue: c.reason });
  }
}

const PILL: Record<CostHealthStatus, string> = { pass: "ok", warn: "warn", fail: "err", info: "mut" };

export function StatusPill({ status }: { status: CostHealthStatus }) {
  const { t } = useTranslation();
  return <span className={`pill ${PILL[status]}`} data-health-status={status}>{t(`costs.health.status.${status}`)}</span>;
}

/** The badge beside the cost page's headline: the worst status and its
 *  reason, linking to /costs/health. While the checks have not answered
 *  there is no badge; when they could not run, it says so — never green. */
export function CostHealthBadge() {
  const { t } = useTranslation();
  const q = useCostHealth();
  if (!q.data) {
    if (!q.isError) return null;
    return (
      <Link to={HEALTH_PATH} className="text-xs" data-health-badge="error" style={{ display: "inline-flex", alignItems: "center", gap: 6 }}>
        <span className="pill mut">{t("costs.health.unavailable")}</span>
      </Link>
    );
  }
  const w = worstCheck(q.data);
  const reason = w ? `${t(`costs.health.name.${w.id}`)}: ${reasonText(t, w)}` : t("costs.health.allPassed");
  return (
    <Link to={HEALTH_PATH} className="text-xs" data-health-badge={q.data.status} title={reason}
      aria-label={t("costs.health.badgeLabel", { status: t(`costs.health.status.${q.data.status}`), reason })}
      style={{ display: "inline-flex", alignItems: "center", gap: 6, minWidth: 0, maxWidth: 520 }}>
      <StatusPill status={q.data.status} />
      <span className="muted" style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{reason}</span>
    </Link>
  );
}

/** "incomplete" beside a part a failed check affects; the reason on hover
 *  and the checks one click away. */
export function IncompleteMark({ check, part }: { check: CostHealthCheck; part: "outside" | "multiple" }) {
  const { t } = useTranslation();
  const reason = `${t(`costs.health.name.${check.id}`)}: ${reasonText(t, check)}`;
  return (
    <Link to={HEALTH_PATH} className="pill err" data-incomplete={part} title={reason}
      aria-label={t(`costs.health.incomplete.${part}`, { reason })} style={{ fontWeight: 400 }}>
      {t("costs.health.incomplete.mark")}
    </Link>
  );
}

/** /costs/health: every check with its status and reason, and when they ran. */
export default function CostHealthPage() {
  const { t } = useTranslation();
  const q = useCostHealth();
  return (
    <div>
      <div className="mb-1"><Link to="/costs" className="text-sm">← {t("costs.outside.back")}</Link></div>
      <div className="flex items-baseline gap-3 mb-1">
        <h1 className="text-[22px]">{t("costs.health.title")}</h1>
        {q.data && <StatusPill status={q.data.status} />}
      </div>
      <p className="muted text-sm mb-4" style={{ maxWidth: 680 }}>{t("costs.health.hint")}</p>
      {q.isError && !q.data && <p className="text-sm" role="alert" data-health-error>{t("costs.health.loadError")}</p>}
      {q.data && <>
        <p className="muted text-xs mb-3" data-health-checked>{t("costs.health.checkedAt", { when: when(q.data.checked_at) })}</p>
        <div className="card">
          <table className="w-full text-sm" data-health-table>
            <tbody>
              {q.data.checks.map((c) => (
                <tr key={c.id} data-health-check={c.id} style={{ verticalAlign: "top" }}>
                  <td style={{ padding: "6px 8px", width: 90 }}><StatusPill status={c.status} /></td>
                  <td style={{ padding: "6px 8px", width: 220, fontWeight: 600 }}>{t(`costs.health.name.${c.id}`)}</td>
                  <td style={{ padding: "6px 8px" }}>
                    {reasonText(t, c)}
                    {c.facts.last_error && <div className="mt-1"><code className="text-xs">{c.facts.last_error}</code></div>}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </>}
    </div>
  );
}
