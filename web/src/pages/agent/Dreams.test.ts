import { describe, expect, it } from "vitest";
import { logDetail } from "./Dreams";

/* The wiki log names the page beside each row; the summary only adds
   something if it says more than the verb and the page title. The wiki
   writes its verbs in English (internal/memory); older rows carry German. */

describe("logDetail", () => {
  it("drops the English verb in front of a summary that differs from the page name", () => {
    expect(logDetail("new page: Pricing table", "Customer ACME")).toBe("Pricing table");
    expect(logDetail("appended: Contract renewal in March", "Customer ACME")).toBe("Contract renewal in March");
    expect(logDetail("edited: Pricing table", "Customer ACME")).toBe("Pricing table");
    expect(logDetail("deleted: Old contacts", "Customer ACME")).toBe("Old contacts");
  });

  it("still reads rows written with the German verbs", () => {
    expect(logDetail("neue Seite: Kunde ACME", "Kunde ACME")).toBe("");
    expect(logDetail("ergänzt: Vertrag im März", "Kunde ACME")).toBe("Vertrag im März");
  });

  it("keeps a summary that says more than the page name", () => {
    expect(logDetail("appended: Contract renewal in March", "Customer ACME")).toBe("Contract renewal in March");
    expect(logDetail("duplicate merged", "Customer ACME")).toBe("duplicate merged");
  });
});
