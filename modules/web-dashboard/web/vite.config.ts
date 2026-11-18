import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import { mockModelList } from "./vite.mock-modellist";

// base must be "/ui/": the output is embedded into the binary and mounted under the
// porthub's /ui prefix (see Prefix and AssetDir in module.go). This correspondence has one
// source of truth only -- changing it here means changing that constant at the same time,
// and the symptom of the two disagreeing is "blank page, a pile of 404s in the console",
// which is hard to spot as a wrong prefix at a glance.
//
// outDir is web/dist (committed to version control): Go's go:embed reads it, so `go build`
// and CI stay fully offline, and the front-end toolchain only appears on dev machines.
export default defineConfig({
  base: "/ui/",
  // The product one **has** a backend: the model dropdown really asks
  // /ui/api/ext/modellist.
  define: { __NEWGATE_DEMO__: "false" },
  // mockModelList only takes effect under `vite dev` (the plugin declares apply: "serve"
  // itself): it answers /ui/api/ext/modellist in the real backend's shape, so the model
  // dropdown can be verified end to end already at the front-end stage. No path of the
  // output goes through it.
  plugins: [svelte(), mockModelList()],
  build: {
    outDir: "dist",
    emptyOutDir: true,
    target: "es2022",
    // No chunk splitting: the output is embedded into the binary for offline intranet use,
    // and one bundle is easier to trace than several request paths (devtools shows exactly
    // one js).
    rollupOptions: { output: { manualChunks: undefined } },
  },
});
