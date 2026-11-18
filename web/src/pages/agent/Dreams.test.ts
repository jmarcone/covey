import { describe, expect, it } from "vitest";
import { logDetail } from "./Dreams";

/* The wiki log names the page beside each row; the summary only adds
   something if it says more than the verb and the page title. The wiki
   writes its verbs in English (internal/memory); older rows carry German. */

describe("logDetail", () => {
  it("drops the English verb and the repeated page title", () => {
    expect(logDetail("new page: Customer ACME", "Customer ACME")).toBe("");
    expect(logDetail("appended: Customer ACME", "Customer ACME")).toBe("");
    expect(logDetail("edited: Customer ACME", "Customer ACME")).toBe("");
    expect(logDetail("deleted: Customer ACME", "Customer ACME")).toBe("");
  });

  it("still reads rows written with the German verbs", () => {
    expect(logDetail("neue Seite: Kunde ACME", "Kunde ACME")).toBe("");
    expect(logDetail("ergänzt: Kunde ACME", "Kunde ACME")).toBe("");
  });

  it("keeps a summary that says more than the page name", () => {
    expect(logDetail("appended: Contract renewal in March", "Customer ACME")).toBe("Contract renewal in March");
    expect(logDetail("duplicate merged", "Customer ACME")).toBe("duplicate merged");
  });
});
