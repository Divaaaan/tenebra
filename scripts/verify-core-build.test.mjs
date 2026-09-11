import { test } from 'node:test';
import assert from 'node:assert/strict';
import { verifyBuildInfo } from './verify-core-build.mjs';
const info = 'core.exe: go1.26.8\n\tpath\tgithub.com/Divaaaan/tenebra/cmd/tenebra-core\n\tbuild\tGOOS=windows\n\tbuild\tGOARCH=amd64\n\tbuild\tvcs.revision=abc123\n\tbuild\tvcs.modified=false\n';
const expected = { goVersion: '1.26.8', revision: 'abc123', os: 'windows', arch: 'amd64' };
test('build evidence validates exact toolchain, target, and clean source revision', () => {
  assert.doesNotThrow(() => verifyBuildInfo(info, expected));
  for (const [before, after] of [['go1.26.8', 'go1.26.7'], ['GOARCH=amd64', 'GOARCH=arm64'], ['GOOS=windows', 'GOOS=linux'], ['abc123', 'old123'], ['vcs.modified=false', 'vcs.modified=true']]) {
    assert.throws(() => verifyBuildInfo(info.replace(before, after), expected));
  }
  assert.throws(() => verifyBuildInfo('', expected));
});
