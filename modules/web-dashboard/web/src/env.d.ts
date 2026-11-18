// Two build-time facts (injected by vite's `define`):
//
//   __NEWGATE_DEMO__  true = the "clickable demo page" on the site. It has no backend, so
//                     the model dropdown does not ask an upstream (asking only yields a
//                     404 and puts "could not ask" on the screen -- which is not what this
//                     page is meant to demonstrate).
//
// `declare const` rather than `import.meta.env`: these two are not environment variables,
// they are decided by the **build configuration** (see define in vite.config.ts /
// vite.demo.config.ts). Misspell an environment variable name and the symptom is
// `undefined` silently turning false; misspell one here and it goes red at the
// `pnpm check` layer.
declare const __NEWGATE_DEMO__: boolean;
