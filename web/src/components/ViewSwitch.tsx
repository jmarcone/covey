import { Link } from "react-router";
import { useTranslation } from "react-i18next";
import { viewUrl, type AgentsView } from "../pages/agents/views";

const LABEL: Record<AgentsView, string> = { office: "team.ueberblick", list: "agents.view.list", org: "nav.org" };

/* The switch between the views of the agents area. A row of links and not a
 * tablist: each view is a page with its own address, and the way back from an
 * agent's page is a click on the segment it came from.
 *
 * `current` is the view the person is in. On an agent's page it is null and
 * `origin` marks the view it was reached from — filled like the current one,
 * without its weight. A click on a segment is the person's choice of view
 * (`onPick`); the way back from an agent's page is one too, as it names the
 * view the person works in. */
export default function ViewSwitch({
  views,
  current,
  origin,
  onPick,
}: {
  views: AgentsView[];
  current: AgentsView | null;
  origin?: AgentsView;
  onPick?: (view: AgentsView) => void;
}) {
  const { t } = useTranslation();
  const label = (v: AgentsView) => t(LABEL[v]);
  return (
    <nav className="av-switch" aria-label={t("agents.views")}>
      {views.map((v) => {
        const back = current == null && v === origin;
        return (
          <Link
            key={v}
            to={viewUrl(v)}
            onClick={() => onPick?.(v)}
            // The segment keeps this word's room at the current view's weight (app.css).
            data-label={label(v)}
            className={v === current ? "on" : back ? "on back" : undefined}
            aria-current={v === current ? "page" : undefined}
            aria-label={back ? t("agents.backTo", { view: label(v) }) : undefined}
            // A phone may cut the word short: the title has it whole, as a crumb step's does.
            title={back ? t("agents.backTo", { view: label(v) }) : label(v)}
          >
            <span>{label(v)}</span>
          </Link>
        );
      })}
    </nav>
  );
}
