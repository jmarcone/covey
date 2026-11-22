import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, Route, Routes, useLocation, useNavigate, useSearchParams } from "react-router";
import AgentsFrame from "./AgentsFrame";
import Landing from "./Landing";
import { chosenView, forgetUnsavedViews, fromView, rememberView } from "./views";
import { mockFetch, testPrincipal, useGerman } from "../../test/render";
import type { Principal } from "../../api";

const me = (over: Partial<Principal> = {}): Principal => ({ ...testPrincipal(), TeamSurface: true, ...over });

function List() {
  return (
    <>
      <p>die Liste</p>
      <output aria-label="Suche">{useLocation().search}</output>
    </>
  );
}

// The browser's Back and Forward, and an address the page rewrites itself.
let browser: { back(): void; forward(): void; dropQuery(): void };
function Browser() {
  const go = useNavigate();
  const [, setSearch] = useSearchParams();
  browser = { back: () => go(-1), forward: () => go(1), dropQuery: () => setSearch({}, { replace: true }) };
  return null;
}

/* The root and the list as the shell routes them, with the bar above. A tab
   is a fresh mount: what survives it is what the browser stored. */
function app(principal: Principal, route: string | { pathname: string; search?: string; state?: unknown } = "/") {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0, staleTime: 0 } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[route]}>
        <Browser />
        <AgentsFrame teamSurface={!!principal.TeamSurface} principal={principal.ID} />
        <Routes>
          <Route path="/" element={<Landing me={principal} office={<p>das Büro</p>} />} />
          <Route path="/agents" element={<List />} />
          <Route path="/org" element={<p>das Organigramm</p>} />
          <Route path="/agents/:id" element={<p>die Seite</p>} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  useGerman();
  localStorage.clear();
  sessionStorage.clear();
  vi.unstubAllGlobals();
  forgetUnsavedViews();
});

const AGENT = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee";
const denied = () => {
  throw new Error("denied");
};

describe("the view the root opens", () => {
  it("is the list while nothing is chosen, the office one click away", () => {
    app(me());
    expect(screen.getByText("die Liste")).toBeInTheDocument();
    expect(screen.queryByText("das Büro")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("link", { name: "Büro" }));
    expect(screen.getByText("das Büro")).toBeInTheDocument();
  });

  it("is the office after the office was chosen, in every later tab", () => {
    app(me());
    fireEvent.click(screen.getByRole("link", { name: "Büro" }));
    expect(chosenView(me().ID, true)).toBe("office");

    // A reload or a new tab: a fresh mount at the root, the same storage.
    sessionStorage.clear();
    app(me());
    expect(screen.getAllByText("das Büro")).toHaveLength(2);
  });

  it("is the list again after the list was chosen", () => {
    rememberView(me().ID, "office");
    app(me());
    fireEvent.click(screen.getAllByRole("link", { name: "Liste" })[0]);
    expect(chosenView(me().ID, true)).toBe("list");

    app(me());
    expect(screen.getAllByText("die Liste").length).toBeGreaterThan(0);
    expect(screen.queryByText("das Büro")).not.toBeInTheDocument();
  });

  it("is kept per principal and does not leak to another one on the same browser", () => {
    const ada = me({ ID: "aaaaaaaa-0000-0000-0000-000000000001" });
    const bob = me({ ID: "bbbbbbbb-0000-0000-0000-000000000002" });
    rememberView(ada.ID, "office");
    expect(localStorage.getItem(`covey.agents.view.${ada.ID}`)).toBe("office");
    expect(chosenView(ada.ID, true)).toBe("office");
    expect(chosenView(bob.ID, true)).toBe("list");

    app(bob);
    expect(screen.getByText("die Liste")).toBeInTheDocument();
    // Bob's pick is Bob's: Ada keeps hers.
    fireEvent.click(screen.getByRole("link", { name: "Büro" }));
    expect(chosenView(ada.ID, true)).toBe("office");
    rememberView(ada.ID, "list");
    expect(chosenView(bob.ID, true)).toBe("office");
  });

  it("is the list when storage throws, without an error", () => {
    // The setup swaps localStorage for a plain object of its own.
    vi.stubGlobal("localStorage", { getItem: denied, setItem: denied });
    expect(() => rememberView(me().ID, "office")).not.toThrow();
    app(me({ ID: "cccccccc-0000-0000-0000-000000000003" }));
    expect(screen.getByText("die Liste")).toBeInTheDocument();
  });

  // Chrome with "Block all cookies" and Firefox with dom.storage.enabled=false
  // throw on every access; the office must stay one click away.
  it("keeps the office one click away when storage refuses every access", () => {
    vi.stubGlobal("localStorage", { getItem: denied, setItem: denied });
    app(me());
    expect(screen.getByText("die Liste")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("link", { name: "Büro" }));
    expect(screen.getAllByText("das Büro").length).toBeGreaterThan(0);
    expect(chosenView(me().ID, true)).toBe("office");
    // And the way back to the list is a pick too.
    fireEvent.click(screen.getAllByRole("link", { name: "Liste" })[0]);
    expect(screen.getAllByText("die Liste").length).toBeGreaterThan(0);
    expect(chosenView(me().ID, true)).toBe("list");
  });

  it("keeps the office one click away when a write fails over an older list", () => {
    vi.stubGlobal("localStorage", { getItem: () => "list", setItem: denied });
    app(me());
    fireEvent.click(screen.getByRole("link", { name: "Büro" }));
    expect(screen.getAllByText("das Büro").length).toBeGreaterThan(0);
    expect(chosenView(me().ID, true)).toBe("office");
  });

  it("lets a write that goes through again take over from the pick kept for the tab", () => {
    vi.stubGlobal("localStorage", { getItem: () => "list", setItem: denied });
    rememberView(me().ID, "office");
    expect(chosenView(me().ID, true)).toBe("office");
    vi.unstubAllGlobals();
    rememberView(me().ID, "list");
    expect(chosenView(me().ID, true)).toBe("list");
    expect(localStorage.getItem(`covey.agents.view.${me().ID}`)).toBe("list");
  });

  it("stores the choice on the way back from an agent's page", () => {
    mockFetch({ [`/api/v1/agents/${AGENT}`]: { id: AGENT, display_name: "Cost Controller" } });
    rememberView(me().ID, "list");
    app(me(), { pathname: `/agents/${AGENT}`, state: fromView("office") });
    expect(screen.getByText("die Seite")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("link", { name: "Zurück: Büro" }));
    expect(chosenView(me().ID, true)).toBe("office");
    expect(screen.getAllByText("das Büro").length).toBeGreaterThan(0);
  });

  for (const TeamSurface of [true, false]) {
    it(`is the org chart after it was chosen (team surface ${TeamSurface ? "on" : "off"})`, () => {
      const first = app(me({ TeamSurface }), "/agents");
      fireEvent.click(screen.getByRole("link", { name: "Organigramm" }));
      expect(screen.getByText("das Organigramm")).toBeInTheDocument();
      expect(chosenView(me().ID, TeamSurface)).toBe("org");
      first.unmount();

      sessionStorage.clear();
      app(me({ TeamSurface }));
      expect(screen.getByText("das Organigramm")).toBeInTheDocument();
    });
  }

  it("opens the list for a department on the root, whatever was chosen", () => {
    rememberView(me().ID, "org");
    app(me(), "/?dept=d1");
    expect(screen.getByText("die Liste")).toBeInTheDocument();
    expect(screen.getByLabelText("Suche")).toHaveTextContent("?dept=d1");
  });

  it("passes the query of the root on to the list", () => {
    app(me(), "/?dept=d1");
    expect(screen.getByText("die Liste")).toBeInTheDocument();
    expect(screen.getByLabelText("Suche")).toHaveTextContent("?dept=d1");
  });

  it("stays the list without the team surface, whatever was stored", () => {
    rememberView(me().ID, "office");
    expect(chosenView(me().ID, false)).toBe("list");
    app(me({ TeamSurface: false }));
    expect(screen.getByText("die Liste")).toBeInTheDocument();
    expect(screen.queryByText("das Büro")).not.toBeInTheDocument();
  });

  for (const [pick, shown] of [["Organigramm", "das Organigramm"], ["Liste", "die Liste"]]) {
    it(`keeps the office on the entry that showed it: ${pick}, Back, Forward`, async () => {
      rememberView(me().ID, "office");
      app(me());
      fireEvent.click(screen.getByRole("link", { name: pick }));
      expect(screen.getByText(shown)).toBeInTheDocument();
      await act(async () => browser.back());
      expect(screen.getByText("das Büro")).toBeInTheDocument();
      expect(screen.getByRole("link", { name: "Büro" })).toHaveAttribute("aria-current", "page");
      await act(async () => browser.forward());
      expect(screen.getByText(shown)).toBeInTheDocument();
    });
  }

  // The office takes ?dept= out of the address, and the entry's state goes with it.
  it("stays the office reached with Back when the page rewrites its own address", async () => {
    rememberView(me().ID, "org");
    app(me(), { pathname: "/", search: "?dept=d1", state: { view: "office" } });
    expect(screen.getByText("das Büro")).toBeInTheDocument();
    await act(async () => browser.dropQuery());
    expect(screen.getByText("das Büro")).toBeInTheDocument();
  });

  it("does not bring the office back where the installation has none", () => {
    app(me({ TeamSurface: false }), { pathname: "/", state: { view: "office" } });
    expect(screen.getByText("die Liste")).toBeInTheDocument();
  });

  it("ignores a stored value that is no view", () => {
    localStorage.setItem(`covey.agents.view.${me().ID}`, "lounge");
    expect(chosenView(me().ID, true)).toBe("list");
  });
});
