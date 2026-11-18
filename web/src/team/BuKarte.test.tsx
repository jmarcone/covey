import { beforeEach, describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { BuKarte } from "./Buero";
import type { Agent } from "../api";
import { testPrincipal, useGerman } from "../test/render";

const agent = {
  id: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
  slug: "cost-controller",
  display_name: "Cost Controller",
  job_title: "Cost Controller",
  status: "sleeping",
  killed: false,
} as Agent;

function Ziel() {
  const { pathname, state } = useLocation();
  return <p data-testid="ziel">{`${pathname} ${JSON.stringify(state)}`}</p>;
}

/* The card at a desk has two ways out: the conversation, and the agent's own
   page. The page is reached with the office as its way back. */
describe("Die Karte im Büro", () => {
  beforeEach(() => useGerman());

  function zeige(onOeffnen = () => {}) {
    const qc = new QueryClient();
    return render(
      <QueryClientProvider client={qc}>
        <MemoryRouter initialEntries={["/"]}>
          <Routes>
            <Route
              path="/"
              element={<BuKarte agent={agent} wartet={false} me={testPrincipal()} onOeffnen={onOeffnen} onSchliessen={() => {}} />}
            />
            <Route path="*" element={<Ziel />} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    );
  }

  it("führt über den Namen auf die Seite des Agenten, vom Büro aus", async () => {
    zeige();
    const link = screen.getByRole("link", { name: "Cost Controller" });
    expect(link.getAttribute("href")).toBe(`/agents/${agent.id}`);
    await userEvent.click(link);
    expect(screen.getByTestId("ziel")).toHaveTextContent(`/agents/${agent.id} {"from":"office"}`);
  });

  it("hat neben dem Gespräch einen Knopf für Profil und Einstellungen", async () => {
    let offen = 0;
    zeige(() => offen++);
    await userEvent.click(screen.getByRole("button", { name: "Gespräch öffnen" }));
    expect(offen).toBe(1);

    await userEvent.click(screen.getByRole("link", { name: "Profil und Einstellungen" }));
    expect(screen.getByTestId("ziel")).toHaveTextContent(`/agents/${agent.id} {"from":"office"}`);
  });
});
