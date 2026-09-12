import { test } from 'node:test';
import assert from 'node:assert/strict';
import { generateKeyPairSync, sign, createHash } from 'node:crypto';
import { verifyUpdaterSignature, makeCandidate, verifyCandidate, verifyAcceptance, assertPrepareIdentity, promoteCandidate } from './signed-candidate.mjs';

const sha = b => createHash('sha256').update(b).digest('hex');
const { privateKey, publicKey } = generateKeyPairSync('ed25519');
const keyId = Buffer.from('0102030405060708', 'hex');
const rawKey = publicKey.export({ type: 'spki', format: 'der' }).subarray(-32);
const pubkey = Buffer.from('untrusted comment: test key\n' + Buffer.concat([Buffer.from('Ed'), keyId, rawKey]).toString('base64') + '\n').toString('base64');
function signature(bytes, algorithm = 'ED') {
  const message = algorithm === 'ED' ? createHash('blake2b512').update(bytes).digest() : bytes;
  const sig = sign(null, message, privateKey), comment = 'timestamp:12345\tfile:test';
  return Buffer.from('untrusted comment: test\n' + Buffer.concat([Buffer.from(algorithm), keyId, sig]).toString('base64') + '\ntrusted comment: ' + comment + '\n' + sign(null, Buffer.concat([sig, Buffer.from(comment)]), privateKey).toString('base64') + '\n').toString('base64');
}
for (const algorithm of ['Ed', 'ED']) test(`verifies ${algorithm} payload and authenticated comment; rejects different bytes`, () => {
  const bytes = Buffer.from('signed executable fixture'), encoded = signature(bytes, algorithm);
  assert.equal(verifyUpdaterSignature(bytes, encoded, pubkey).valid, true);
  assert.throws(() => verifyUpdaterSignature(Buffer.from('replaced'), encoded, pubkey));
  const changed = Buffer.from(encoded, 'base64').toString().replace('timestamp:12345', 'timestamp:54321');
  assert.throws(() => verifyUpdaterSignature(bytes, Buffer.from(changed).toString('base64'), pubkey));
});
test('rejects malformed records, unknown algorithms, key IDs and another signing key', () => {
  const bytes = Buffer.from('fixture'), encoded = signature(bytes);
  for (const mutation of [s => s + '!', s => s.slice(2), s => Buffer.from(Buffer.from(s, 'base64').toString() + 'extra\n').toString('base64')]) assert.throws(() => verifyUpdaterSignature(bytes, mutation(encoded), pubkey));
  const lines = Buffer.from(encoded, 'base64').toString().trim().split('\n');
  for (const offset of [0, 2]) { const copy = [...lines], raw = Buffer.from(copy[1], 'base64'); raw[offset] ^= 1; copy[1] = raw.toString('base64'); assert.throws(() => verifyUpdaterSignature(bytes, Buffer.from(copy.join('\n')).toString('base64'), pubkey)); }
  const otherKey = generateKeyPairSync('ed25519').publicKey.export({type:'spki',format:'der'}).subarray(-32);
  const otherPubkey = Buffer.from('untrusted comment: other\n'+Buffer.concat([Buffer.from('Ed'),keyId,otherKey]).toString('base64')+'\n').toString('base64');
  assert.throws(()=>verifyUpdaterSignature(bytes,encoded,otherPubkey));
});
const sourceSha = 'a'.repeat(40), sourceTree = 'b'.repeat(40), repo = 'Divaaaan/tenebra';
function fixture() {
  const files = new Map();
  files.set('release-notes.md',Buffer.from('# Tenebra 0.6.0\n\nCandidate-bound release notes.\n'));
  for (const name of ['Tenebra_0.6.0_x64-setup.exe', 'Tenebra_0.6.0_universal.dmg', 'Tenebra_universal.app.tar.gz', 'Tenebra_0.6.0_amd64.deb', 'Tenebra_0.6.0_amd64.AppImage', 'tenebra-0.6.0-1-x86_64.pkg.tar.zst']) { const bytes = Buffer.from(name); files.set(name, bytes); files.set(name + '.sig', Buffer.from(signature(bytes))); }
  for (const [name, os, arch] of [['windows','windows','amd64'], ['macos-arm64','darwin','arm64'], ['macos-amd64','darwin','amd64'], ['linux','linux','amd64'], ['arch','linux','amd64']]) files.set(`core-buildinfo-${name}.json`, Buffer.from(JSON.stringify({ goVersion:'1.26.8', revision:sourceSha, os, arch, sha256:'c'.repeat(64), metadata:`core: go1.26.8\n build GOOS=${os}\n build GOARCH=${arch}\n build vcs.revision=${sourceSha}\n build vcs.modified=false\n` })));
  const context = { sourceSha, sourceTree, repo, version:'0.6.0', goVersion:'1.26.8', runId:123, runAttempt:1, pubkey };
  const candidate = makeCandidate(files, context);
  const acceptance = { schema:1, kind:'tenebra-desktop-acceptance', state:'pass', version:'0.6.0', sourceSha, sourceTree, prepareRunId:123, prepareAttempt:1, artifactId:456, artifactSha256:'d'.repeat(64), manifestSha256:sha(Buffer.from(JSON.stringify(candidate, null, 2)+'\n')), files:candidate.files, evidence:[{ kind:'windows-install-service-ui-tunnel-protection', sha256:'e'.repeat(64) }] };
  return { files, candidate, acceptance, context };
}
test('candidate covers all signed desktop bundles, fixed Go metadata and final stable URLs', () => {
  const { files, candidate } = fixture();
  assert.equal(verifyCandidate(candidate, files, pubkey), true);
  assert.equal(candidate.updater.platforms['windows-x86_64'].url, 'https://github.com/Divaaaan/tenebra/releases/download/v0.6.0/Tenebra_0.6.0_x64-setup.exe');
  assert.equal(candidate.files.length, 18);
  assert.equal(candidate.updater.notes,files.get('release-notes.md').toString());
});
test('release notes are required, bounded UTF-8 for the exact version and cannot inject an ownership marker',()=>{
  const f=fixture();
  for(const content of [null,Buffer.from('# Tenebra 0.5.11\n'),Buffer.from([0xff]),Buffer.from('# Tenebra 0.6.0\n<!-- tenebra-promotion-sha256:'+'a'.repeat(64)+' -->'),Buffer.from('# Tenebra 0.6.0\n'+'x'.repeat(65536))]) {
    const files=new Map(f.files);if(content===null)files.delete('release-notes.md');else files.set('release-notes.md',content);
    assert.throws(()=>makeCandidate(files,f.context));
  }
  const files=new Map(f.files);files.set('release-notes.md',Buffer.from('# Tenebra 0.6.0\n\nAltered later.\n'));
  assert.throws(()=>verifyCandidate(f.candidate,files,pubkey));
});
test('fails on replaced bytes, missing platform, unknown path, wrong core source or dirty build', () => {
  const f = fixture();
  for (const mutate of [files => files.set('Tenebra_0.6.0_x64-setup.exe',Buffer.from('wrong')), files => files.delete('Tenebra_0.6.0_universal.dmg'), files => files.set('../escape',Buffer.from('bad')), files => { const name='core-buildinfo-windows.json', report=JSON.parse(files.get(name)); report.revision='f'.repeat(40); files.set(name,Buffer.from(JSON.stringify(report))); }, files => { const name='core-buildinfo-windows.json', report=JSON.parse(files.get(name)); report.metadata=report.metadata.replace('modified=false','modified=true'); files.set(name,Buffer.from(JSON.stringify(report))); }]) { const files = new Map(f.files); mutate(files); assert.throws(() => makeCandidate(files, f.context)); }
});
test('acceptance binds every byte plus source, run, attempt, artifact identity and native evidence', () => {
  const { candidate, acceptance } = fixture();
  assert.equal(verifyAcceptance(candidate, acceptance, { artifactId:456, artifactSha256:'d'.repeat(64), manifestSha256:acceptance.manifestSha256 }), true);
  for (const mutate of [a => a.state='failed', a => a.sourceSha='f'.repeat(40), a => a.prepareRunId++, a => a.prepareAttempt++, a => a.artifactId++, a => a.artifactSha256='f'.repeat(64), a => a.manifestSha256='f'.repeat(64), a => a.files.pop(), a => a.files[0].sha256='f'.repeat(64), a => a.evidence=[], a => a.evidence[0].kind='build-only']) { const copy=structuredClone(acceptance); mutate(copy); assert.throws(() => verifyAcceptance(candidate,copy,{artifactId:456,artifactSha256:'d'.repeat(64),manifestSha256:acceptance.manifestSha256})); }
});
test('only exact main dispatch can request signing, not PRs, tags, forks or historical checkout', () => {
  const context={ event:'workflow_dispatch', ref:'refs/heads/main', sha:sourceSha, sourceSha, repo, expectedRepo:repo };
  assert.equal(assertPrepareIdentity(context), true);
  for(const change of [{event:'pull_request'},{ref:'refs/tags/v0.6.0'},{sha:'f'.repeat(40)},{repo:'other/fork'}]) assert.throws(()=>assertPrepareIdentity({...context,...change}));
});
function promotionFixture() {
  const f=fixture(),effects=[],uploaded=new Map(),state={tag:null,release:null};let failure=null;
  const fail=stage=>{if(failure===stage){failure=null;throw Error('injected '+stage);}};
  const api={
    getState:async()=>structuredClone(state),
    createDraft:async(tag,commit,promotionId)=>{state.release={id:42,isDraft:true,promotionId};effects.push('draft');fail('after-draft');return structuredClone(state.release);},
    createTag:async(tag,commit)=>{state.tag={type:'commit',sha:commit};effects.push('tag');fail('after-tag');},
    ensureAsset:async(id,name,bytes)=>{if(uploaded.has(name)){assert.deepEqual(uploaded.get(name),bytes);return;}uploaded.set(name,Buffer.from(bytes));effects.push('upload:'+name);fail('after-upload');},
    verifyUploaded:async(id,files,{partial=false}={})=>{for(const [name,bytes] of uploaded){assert.ok(files.has(name));assert.deepEqual(bytes,files.get(name));}if(!partial){assert.equal(uploaded.size,files.size);fail('readback');}},
    publish:async()=>{state.release.isDraft=false;effects.push('publish');fail('after-publish');},
    updateChannel:async()=>effects.push('channel'),
  };
  return {f,api,effects,uploaded,state,setFailure:stage=>{failure=stage;},run:()=>promoteCandidate({...f,api,artifact:{artifactId:456,artifactSha256:'d'.repeat(64),manifestSha256:f.acceptance.manifestSha256}})};
}
test('unknown existing tag or draft fails without mutation',async()=>{
  for(const state of [{tag:{type:'commit',sha:sourceSha},release:null},{tag:null,release:{id:42,isDraft:true,promotionId:'0'.repeat(64)}},{tag:{type:'commit',sha:'f'.repeat(40)},release:{id:42,isDraft:false,promotionId:'0'.repeat(64)}}]){const p=promotionFixture();Object.assign(p.state,state);await assert.rejects(p.run);assert.deepEqual(p.effects,[]);}
});
for(const stage of ['after-draft','after-tag','after-upload','after-publish']) test(`resume exact owned promotion after ${stage}, without retag/rebuild/asset overwrite`,async()=>{
  const p=promotionFixture();p.setFailure(stage);await assert.rejects(p.run,new RegExp(stage));const before=new Map(p.uploaded);
  await p.run();assert.equal(p.effects.filter(x=>x==='draft').length,1);assert.equal(p.effects.filter(x=>x==='tag').length,1);assert.equal(p.effects.filter(x=>x==='publish').length,1);assert.equal(p.effects.at(-1),'channel');
  for(const [name,bytes] of before)assert.deepEqual(p.uploaded.get(name),bytes);
  assert.deepEqual(p.uploaded.get('Tenebra_0.6.0_x64-setup.exe'),p.f.files.get('Tenebra_0.6.0_x64-setup.exe'));
});
test('owned retry refuses changed tag, acceptance, or any preexisting bytes before more uploads',async()=>{
  for(const mutate of [p=>p.state.tag.sha='f'.repeat(40),p=>p.state.release.promotionId='0'.repeat(64),p=>p.uploaded.set([...p.uploaded.keys()][0],Buffer.from('replaced'))]){const p=promotionFixture();p.setFailure('after-upload');await assert.rejects(p.run);mutate(p);const count=p.effects.length;await assert.rejects(p.run);assert.equal(p.effects.length,count);}
});
test('failed final readback cannot publish or advance updater clients',async()=>{const p=promotionFixture();p.setFailure('readback');await assert.rejects(p.run);assert.ok(!p.effects.includes('publish'));assert.ok(!p.effects.includes('channel'));});
