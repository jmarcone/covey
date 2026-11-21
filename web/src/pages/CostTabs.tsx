import { useTranslation } from "react-i18next";
import { Link, useLocation } from "react-router";
import { useCostQuery } from "./costView";

/** The cost pages' tabs (cost home PR 5), on every cost page: each keeps the window and view in its link. */
export function CostTabs() {
  const { t } = useTranslation();
  const { pathname } = useLocation();
  const q = useCostQuery(["source"]);
  const tabs: [string, string][] = [
    ["/costs", t("costs.home.tabOverview")],
    ["/costs/models", t("costs.home.tabModels")],
    ["/costs/work", t("costs.home.tabWork")],
    ["/costs/details", t("costs.home.tabDetails")],
    ["/costs/outside", t("costs.home.tabOutside")],
    ["/costs/health", t("costs.home.tabHealth")],
  ];
  return (
    <nav className="cost-tabs" aria-label={t("costs.home.tabsLabel")}>
      {tabs.map(([to, label]) => (
        <Link key={to} to={`${to}${q}`} className={pathname === to ? "on" : undefined} aria-current={pathname === to ? "page" : undefined}>{label}</Link>
      ))}
    </nav>
  );
}
