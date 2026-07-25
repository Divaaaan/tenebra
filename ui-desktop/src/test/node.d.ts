/* The app itself never touches Node — it ships into a WebView — so the front end
   carries no ambient Node typings, and pulling @types/node in just to satisfy one
   test would put `fs`, `process` and friends in scope for every file in src/.
   The stylesheet guard test (src/styles/settings.css.test.ts) does have to read a
   file from disk: Vitest stubs CSS imports out (test.css is off), so `?raw` hands
   back an empty string and the sheet can only be reached through the filesystem.
   Declare exactly the sliver it needs, and nothing else. */
declare module "node:fs" {
  export function readFileSync(path: string | URL, encoding: "utf8"): string;
}
