// Deprecated entry point retained for old script consumers. A platform build
// must never publish beta. Use the complete lifecycle gate in publish-release.
import { pathToFileURL } from 'node:url';
export function resolveBetaTarget({ tag, prerelease, latestStable }) {
  return prerelease ? latestStable ?? null : tag;
}
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  console.error('Standalone beta publication is disabled; use .github/scripts/publish-release.mjs after every platform job.');
  process.exitCode = 1;
}
