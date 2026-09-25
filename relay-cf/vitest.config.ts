import { cloudflareTest } from "@cloudflare/vitest-pool-workers";
import { defineConfig } from "vitest/config";

export default defineConfig({
  plugins: [
    cloudflareTest({
      wrangler: { configPath: "./wrangler.toml" },
      miniflare: {
        bindings: {
          ADMIN_TOKEN: "test-admin-token",
          PUBLIC_ORIGIN: "https://relay.test",
          QUEUE_MAX_FRAMES: "5",
        },
      },
    }),
  ],
  test: {
    testTimeout: 20000,
  },
});
