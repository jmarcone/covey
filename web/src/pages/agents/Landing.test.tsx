import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, Route, Routes } from "react-router";
import AgentsFrame from "./AgentsFrame";
import Landing from "./Landing";
import { chosenView, rememberView } from "./views";
import { testPrincipal, useGerman } from "../../test/render";
import type { Principal } from "../../api";

const me = (over: Partial<Principal> = {}): Principal => ({ ...testPrincipal(), TeamSurface: true, ...over });

/* The root and the list as the shell routes them, with the bar above. A tab
   is a fresh mount: what survives it is what the browser stored. */
function app(principal: Principal, route = "/") {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0, staleTime: 0 } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[route]}>
        <AgentsFrame teamSurface={!!principal.TeamSurface} principal={principal.ID} />
        <Routes>
          <Route path="/" element={<Landing me={principal} office={<p>das Büro</p>} />} />
          <Route path="/agents" element={<p>die Liste</p>} />
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
});

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
    const denied = () => {
      throw new Error("denied");
    };
    vi.stubGlobal("localStorage", { getItem: denied, setItem: denied });
    expect(() => rememberView(me().ID, "office")).not.toThrow();
    expect(chosenView(me().ID, true)).toBe("list");
    app(me());
    expect(screen.getByText("die Liste")).toBeInTheDocument();
  });

  it("stays the list without the team surface, whatever was stored", () => {
    rememberView(me().ID, "office");
    expect(chosenView(me().ID, false)).toBe("list");
    app(me({ TeamSurface: false }));
    expect(screen.getByText("die Liste")).toBeInTheDocument();
    expect(screen.queryByText("das Büro")).not.toBeInTheDocument();
  });

  it("ignores a stored value that is no view", () => {
    localStorage.setItem(`covey.agents.view.${me().ID}`, "lounge");
    expect(chosenView(me().ID, true)).toBe("list");
  });
});
