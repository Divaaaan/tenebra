import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import { renderUninstallHelper, sourcePath, outputPath } from './embed-uninstall-helper.mjs';

test('embedded uninstall helper is exactly the reviewed source and fits NSIS strings', () => {
  const source = fs.readFileSync(sourcePath, 'utf8').replaceAll('\r\n', '\n');
  const generated = fs.readFileSync(outputPath, 'utf8').replaceAll('\r\n', '\n');
  assert.equal(generated, renderUninstallHelper(source));
  const chunks = [...generated.matchAll(/StrCpy \$0 "([A-Za-z0-9+/=]{64,})"/g)].map(m => m[1]);
  assert.equal(Buffer.from(chunks.join(''), 'base64').toString('utf16le'), source);
  assert.ok(generated.split('\n').every(line => line.length < 900));
  assert.ok(generated.includes('$$s=')); // literal PowerShell $, not an NSIS variable
  for (let i = 0; i < chunks.length; i++) {
    assert.ok(generated.includes(`SetEnvironmentVariableW(w "TENEBRA_RELEASE_PS${i}", p 0)`));
  }
});

test('host protection cleanup is exclusive to explicit uninstall before service deletion', () => {
  const hooks = fs.readFileSync(new URL('../ui-desktop/src-tauri/installer-hooks.nsh', import.meta.url), 'utf8');
  const uninstall = hooks.slice(hooks.indexOf('!macro NSIS_HOOK_PREUNINSTALL'));
  assert.equal((hooks.match(/!insertmacro TenebraReleaseHostProtection/g) ?? []).length, 1);
  assert.match(uninstall, /TenebraStopService[\s\S]*\$UpdateMode <> 1[\s\S]*TenebraReleaseHostProtection[\s\S]*sc\.exe" delete tenebra/);
  assert.ok(!uninstall.includes('${FileExists}')); // missing core must fail closed
  assert.equal((uninstall.match(/!insertmacro TenebraProbeHostProtection/g) ?? []).length, 2);
});
