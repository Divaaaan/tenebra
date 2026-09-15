import { test } from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
const bash=process.platform==='win32'?'C:/Program Files/Git/bin/bash.exe':'bash';
function source(commit) {
  const env={...process.env};delete env.TENEBRA_SOURCE_COMMIT;
  if(commit!==undefined)env.TENEBRA_SOURCE_COMMIT=commit;
  return execFileSync(bash,['--noprofile','--norc','-c','source packaging/arch/PKGBUILD\nprintf "%s\\n" "${source[0]}"'],{env,encoding:'utf8',stdio:['ignore','pipe','pipe'],timeout:10000}).trim();
}
test('Arch keeps tagged releases by default and accepts only a full immutable candidate commit',()=>{
  assert.equal(source(),'git+https://github.com/Divaaaan/tenebra.git#tag=v0.6.1');
  assert.equal(source('a'.repeat(40)),'git+https://github.com/Divaaaan/tenebra.git#commit='+'a'.repeat(40));
  for(const bad of ['main','v0.6.0','abcdef1','A'.repeat(40),'a'.repeat(40)+';false'])assert.throws(()=>source(bad));
});
