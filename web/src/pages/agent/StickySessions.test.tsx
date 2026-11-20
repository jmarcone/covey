import { describe, it, expect, beforeEach, vi } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import i18n from "../../i18n";
import type { Agent } from "../../api";
import { StickySessions, stickyOf } from "./StickySessions";
import { mockFetch, renderWithProviders } from "../../test/render";

// The sticky-session setting (#130): policy, window and cap go to the server
// together (PATCH /agents/{id}/sticky takes all three), the unchanged ones as
// they stand.

const agent = (over: Partial<Agent> = {}) =>
  ({ id: "a1", slug: "covey-dev", ...over }) as Agent;

function lastPatch(fetchMock: ReturnType<typeof vi.fn>) {
  const call = [...fetchMock.mock.calls].reverse().find((c: unknown[]) => (c[1] as RequestInit | undefined)?.method === "PATCH");
  if (!call) throw new Error("no PATCH sent");
  return { url: String(call[0]), body: JSON.parse(String((call[1] as RequestInit).body)) };
}

describe("StickySessions", () => {
  beforeEach(() => {
    i18n.changeLanguage("en");
  });

  it("falls back to the defaults for a server that does not send the fields", () => {
    expect(stickyOf(agent())).toEqual({ policy: "fresh", window_minutes: 55, context_cap: 200_000 });
  });

  it("sends policy, window and cap together when the policy changes", async () => {
    mockFetch({ "PATCH /api/v1/agents/a1/sticky": { ok: true } });
    renderWithProviders(
      <StickySessions
        agent={agent({ sticky_policy: "fresh", sticky_window_minutes: 30, sticky_context_cap: 150_000 })}
        editable
        row={{}}
      />,
    );
    expect(screen.getByText(/the reviewer should stay fresh/)).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Sticky sessions"), { target: { value: "ab" } });
    await waitFor(() => expect(lastPatch(fetch as never).url).toBe("/api/v1/agents/a1/sticky"));
    expect(lastPatch(fetch as never).body).toEqual({ policy: "ab", window_minutes: 30, context_cap: 150_000 });
  });

  it("saves the window on blur and keeps the rest", async () => {
    mockFetch({ "PATCH /api/v1/agents/a1/sticky": { ok: true } });
    renderWithProviders(<StickySessions agent={agent({ sticky_policy: "conditional" })} editable row={{}} />);
    const input = screen.getByLabelText("window (minutes)") as HTMLInputElement;
    fireEvent.change(input, { target: { value: "40" } });
    fireEvent.blur(input);
    await waitFor(() =>
      expect(lastPatch(fetch as never).body).toEqual({ policy: "conditional", window_minutes: 40, context_cap: 200_000 }),
    );
  });

  it("refuses an out-of-range window without sending it", () => {
    const { calls } = mockFetch({});
    renderWithProviders(<StickySessions agent={agent({ sticky_policy: "conditional" })} editable row={{}} />);
    const input = screen.getByLabelText("window (minutes)") as HTMLInputElement;
    fireEvent.change(input, { target: { value: "0" } });
    fireEvent.blur(input);
    expect(calls).toEqual([]);
    expect(input.value).toBe("55");
  });

  it("is read-only without the right to edit", () => {
    mockFetch({});
    renderWithProviders(<StickySessions agent={agent({ sticky_policy: "conditional" })} editable={false} row={{}} />);
    expect((screen.getByLabelText("Sticky sessions") as HTMLSelectElement).disabled).toBe(true);
    expect((screen.getByLabelText("context cap (tokens)") as HTMLInputElement).disabled).toBe(true);
  });
});
