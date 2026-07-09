// @ts-check
import js from "@eslint/js";
import tseslint from "typescript-eslint";

// Flat ESLint config for the front end. The bar is the community "recommended"
// level on purpose, not a maximal one: it should catch real mistakes (unused
// symbols, unsafe comparisons, shadowed globals, accidental `any`) without
// drowning the build in stylistic noise. TypeScript's strict compiler
// (tsconfig.json) already does the heavy type checking, and the test globals are
// provided by Vitest, so `no-undef` is left to the type-checker.
export default tseslint.config(
  {
    // Build output, dependencies, the Rust shell and coverage reports are not
    // ours to lint.
    ignores: ["dist/**", "coverage/**", "src-tauri/**", "node_modules/**"],
  },
  js.configs.recommended,
  ...tseslint.configs.recommended,
);
