import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import i18n from "./i18n";
import { testPrincipal } from "./test/render";
import App from "./App";

// The signed-in interface's own bundle still on its way.
vi.mock("./AppShell", () => new Promise(() => {}));

function app(route: string) {
  // jsdom has no live stream.
  vi.stubGlobal("EventSource", class { addEventListener() {} close() {} });
  vi.stubGlobal("fetch", vi.fn(async () =>
    new Response(JSON.stringify(testPrincipal()), { status: 200, headers: { "Content-Type": "application/json" } })));
  return render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })}>
      <MemoryRouter initialEntries={[route]}><App /></MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("the interface's bundle on its way", () => {
  it("a cost page says that it loads from the first bundle on", async () => {
    app("/costs/details");
    expect(await screen.findByText(i18n.t("costs.home.loading") as string)).toBeInTheDocument();
  });

  it("another page waits without a placeholder", async () => {
    const { container } = app("/agents");
    await new Promise((r) => setTimeout(r, 50));
    expect(container.querySelector("[data-cost-loading]")).toBeNull();
  });
});
