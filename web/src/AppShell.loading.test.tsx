import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import i18n from "./i18n";
import { testPrincipal } from "./test/render";
import AppShell from "./AppShell";

// A cost page's bundle that is still on its way, one that never arrives, and another page's on its way.
vi.mock("./pages/CostHome", () => new Promise(() => {}));
vi.mock("./pages/Costs", () => new Promise(() => {}));
vi.mock("./pages/CostWork", () => new Promise(() => {}));
vi.mock("./pages/OutsideCosts", () => new Promise(() => {}));
vi.mock("./pages/CostHealth", () => new Promise(() => {}));
vi.mock("./pages/Secrets", () => new Promise(() => {}));
vi.mock("./pages/CostModels", () => {
  throw new Error("the bundle did not arrive");
});

function shell(route: string) {
  // The shell's queries that expect an object instead of a list.
  const objects: Record<string, unknown> = {
    inbox: { items: [], total: 0 },
    onboarding: { steps: [], done: true },
    version: { version: "test", commit: "0000000", go: "", built_at: "", dirty: false },
  };
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) =>
    new Response(JSON.stringify(objects[String(input).split("?")[0].split("/").pop()!] ?? []),
      { status: 200, headers: { "Content-Type": "application/json" } })));
  return render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })}>
      <MemoryRouter initialEntries={[route]}><AppShell me={testPrincipal()} onLogout={() => {}} /></MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("a page's bundle on its way", () => {
  it.each(["/costs", "/costs/details", "/costs/work", "/costs/outside", "/costs/health"])("%s says that it loads", async (route) => {
    const { container } = shell(route);
    await screen.findByText(i18n.t("costs.home.loading") as string);
    expect(container.querySelector('[aria-busy="true"]')).not.toBeNull();
  });

  it("another page keeps no placeholder", async () => {
    const { container } = shell("/secrets");
    await new Promise((r) => setTimeout(r, 50));
    expect(container.querySelector("[data-cost-loading]")).toBeNull();
  });

  it("a cost page's bundle that did not arrive is an error, not an empty screen", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    shell("/costs/models");
    expect((await screen.findByRole("alert", undefined, { timeout: 10_000 })).textContent).toBe(i18n.t("costs.home.bundleError"));
  });
});
