import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import i18n from "./i18n";
import { testPrincipal } from "./test/render";
import App from "./App";

// The signed-in interface's own bundle never arrives. A file of its own: a module is mocked once per file.
vi.mock("./AppShell", () => {
  throw new Error("the bundle did not arrive");
});

describe("the interface's bundle that did not arrive", () => {
  it("is an error on a cost page, not a wait that ends in an empty screen", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    // jsdom has no live stream.
    vi.stubGlobal("EventSource", class { addEventListener() {} close() {} });
    vi.stubGlobal("fetch", vi.fn(async () =>
      new Response(JSON.stringify(testPrincipal()), { status: 200, headers: { "Content-Type": "application/json" } })));
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })}>
        <MemoryRouter initialEntries={["/costs/details"]}><App /></MemoryRouter>
      </QueryClientProvider>,
    );
    expect((await screen.findByRole("alert")).textContent).toBe(i18n.t("costs.home.bundleError"));
  });
});
