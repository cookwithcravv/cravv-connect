import { describe, expect, it } from "vitest";
import worker from "../src/index";
import type { Env } from "../src/env";
import { testEnv } from "./helpers";

describe("worker router", () => {
  it("answers an unexpected exception with a fixed 500 JSON body", async () => {
    const env: Env = {
      ...testEnv,
      IP_LIMITER: {
        limit: () => {
          throw new Error("secret internals");
        },
      } as unknown as RateLimit,
    };
    const res = await worker.fetch(new Request("https://relay.test/v1/blobs", { method: "POST" }), env);
    expect(res.status).toBe(500);
    expect(await res.json()).toEqual({ code: "internal", message: "internal error" });
  });
});
