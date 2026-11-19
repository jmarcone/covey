import { beforeEach, describe, expect, it } from "vitest";
import { render, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, useLocation } from "react-router";
import Buero from "./Buero";
import type { Agent, Department } from "../api";
import { mockFetch, testPrincipal, useGerman } from "../test/render";

const dept = (id: string, name: string) => ({ id, org_id: "o", name, description: "", color: "", leads: [], created_at: "" }) as Department;
const agent = (id: string, department_id?: string) =>
  ({ id, slug: id, display_name: id, job_title: "", status: "sleeping", killed: false, department_id }) as Agent;

function Adresse() {
  const { search } = useLocation();
  return <p data-testid="adresse">{search}</p>;
}

function zeige(route: string, p: { agents: Agent[]; departments: Department[]; bereit: boolean }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0, staleTime: 0 } } });
  const ui = (bereit: boolean) => (
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[route]}>
        <Buero agents={p.agents} departments={p.departments} laufend={[]} wartetBei={new Set()} me={testPrincipal()} bereit={bereit} />
        <Adresse />
      </MemoryRouter>
    </QueryClientProvider>
  );
  const r = render(ui(p.bereit));
  return { ...r, adresse: () => r.getByTestId("adresse").textContent, nochmal: (bereit: boolean) => r.rerender(ui(bereit)) };
}

/* `?dept=` is what the agent page's department step sets. The office shows a
   place and does not filter: an id it has no room for is dropped from the
   address once the data is in — and not before, and not never. The WebGL
   stage itself does not exist in jsdom; the address is what can be checked. */
describe("Das Büro und ?dept=", () => {
  beforeEach(() => {
    useGerman();
    mockFetch({ "/api/v1/org": { id: "o", name: "Org", office_furnishing: "normal" } });
  });

  it("drops an id in an organisation without departments and without agents", async () => {
    const b = zeige("/?dept=gone", { agents: [], departments: [], bereit: true });
    await waitFor(() => expect(b.adresse()).toBe(""));
  });

  it("drops an id the departments do not have", async () => {
    const b = zeige("/?dept=gone", { agents: [agent("a1", "d1")], departments: [dept("d1", "Entwicklung")], bereit: true });
    await waitFor(() => expect(b.adresse()).toBe(""));
  });

  it("drops none when nobody is without a department", async () => {
    const b = zeige("/?dept=none", { agents: [agent("a1", "d1")], departments: [dept("d1", "Entwicklung")], bereit: true });
    await waitFor(() => expect(b.adresse()).toBe(""));
  });

  it("keeps the id while the data is still on its way, and judges it once it is in", async () => {
    const b = zeige("/?dept=d1", { agents: [], departments: [], bereit: false });
    await new Promise((r) => setTimeout(r, 30));
    expect(b.adresse()).toBe("?dept=d1");
    b.nochmal(true);
    await waitFor(() => expect(b.adresse()).toBe(""));
  });

  it("keeps an id that has a room, and none when somebody is without a department", async () => {
    const mit = zeige("/?dept=d1", { agents: [agent("a1", "d1")], departments: [dept("d1", "Entwicklung")], bereit: true });
    await new Promise((r) => setTimeout(r, 30));
    expect(mit.adresse()).toBe("?dept=d1");
    mit.unmount();

    const ohne = zeige("/?dept=none", { agents: [agent("a1")], departments: [], bereit: true });
    await new Promise((r) => setTimeout(r, 30));
    expect(ohne.adresse()).toBe("?dept=none");
  });
});
