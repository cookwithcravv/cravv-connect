import { describe, expect, it } from "vitest";
import { describeError } from "../src/rpc";

// Cloudflare marks cross-object errors with flags that say where the fault was; the log
// line keeps them so an incident can be told apart after the fact.
describe("describeError", () => {
  it("keeps the name and message", () => {
    expect(describeError(new TypeError("boom"))).toBe("TypeError: boom");
  });

  it("adds Cloudflare's retryable, overloaded and remote flags when set", () => {
    const err = Object.assign(new Error("Network connection lost."), { retryable: true, remote: true });
    expect(describeError(err)).toBe("Error: Network connection lost. [retryable remote]");
    const busy = Object.assign(new Error("Durable Object is overloaded."), { overloaded: true });
    expect(describeError(busy)).toBe("Error: Durable Object is overloaded. [overloaded]");
  });

  it("describes a non-Error value", () => {
    expect(describeError("plain")).toBe("plain");
  });
});
