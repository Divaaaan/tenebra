import { test } from 'node:test';
import assert from 'node:assert/strict';
import { verifyPrepareRun, verifyArtifact, safeArchiveNames, publisher, acquireCandidate } from './candidate-github.mjs';
import {sha256} from './signed-candidate.mjs';
const source='a'.repeat(40), acceptance={prepareRunId:123,prepareAttempt:2,sourceSha:source,artifactId:456,artifactSha256:'b'.repeat(64)};
test('Actions archive uses JSON API negotiation but verifies the downloaded binary before extraction',async t=>{
  const requests=[];
  t.mock.method(globalThis,'fetch',async(url,options)=>{
    requests.push({url,method:options.method,accept:options.headers.Accept});
    assert.equal(options.method,'GET');
    if(url.endsWith('/actions/runs/123'))return new Response(JSON.stringify({id:123,run_attempt:2,event:'workflow_dispatch',head_branch:'main',head_sha:source,path:'.github/workflows/desktop-candidate.yml',status:'completed',conclusion:'success',repository:{full_name:'Divaaaan/tenebra'}}));
    if(url.endsWith('/actions/artifacts/456'))return new Response(JSON.stringify({id:456,name:'tenebra-signed-desktop-123-2',expired:false,digest:'sha256:'+acceptance.artifactSha256,size_in_bytes:5,workflow_run:{id:123,head_sha:source,head_branch:'main'}}));
    if(url.endsWith('/actions/artifacts/456/zip')){
      // Matches the live Actions endpoint: octet-stream negotiation returns 415.
      if(options.headers.Accept!=='application/vnd.github+json')return new Response('{}',{status:415});
      // Deliberately invalid digest; must reach binary SHA verification, never
      // parse this non-JSON body or invoke unzip/candidate verification on it.
      return new Response(Buffer.from([0x50,0x4b,0xff,0x00,0xfe]),{headers:{'Content-Type':'application/zip'}});
    }
    throw Error('unexpected request');
  });
  await assert.rejects(()=>acquireCandidate(acceptance,'unused-key','test-token'),/downloaded ZIP SHA differs/);
  assert.equal(requests.length,3);
  assert.ok(requests.every(r=>r.accept==='application/vnd.github+json'));
});
test('release asset byte verification still requests octet-stream',async t=>{
  const notes=Buffer.from('# Tenebra 0.6.0\n\nNotes.\n'),promotionId='c'.repeat(64),bytes=Buffer.from([0xff,0x00,0x7b,0xfe]);
  const candidate={version:'0.6.0',sourceSha:source,files:[{name:'release-notes.md',sha256:sha256(notes)}]};
  let downloaded=false;
  t.mock.method(globalThis,'fetch',async(url,options)=>{
    assert.equal(options.method,'GET');
    if(url.endsWith('/git/ref/tags/v0.6.0'))return new Response(JSON.stringify({object:{type:'commit',sha:source}}));
    if(url.endsWith('/releases/42'))return new Response(JSON.stringify({id:42,tag_name:'v0.6.0',prerelease:false,draft:true,body:`<!-- tenebra-promotion-sha256:${promotionId} -->\n${notes}`,assets:[{id:8,name:'file.exe',state:'uploaded',size:bytes.length}]}));
    if(url.endsWith('/releases/assets/8')){assert.equal(options.headers.Accept,'application/octet-stream');downloaded=true;return new Response(bytes);}
    throw Error('unexpected request');
  });
  await publisher('test-token',candidate,notes).verifyUploaded(42,new Map([['file.exe',bytes]]),{promotionId});
  assert.equal(downloaded,true);
});
test('artifact acquisition pins an actual completed main dispatch, not a PR/tree-equivalent build',()=>{
  const run={id:123,run_attempt:2,event:'workflow_dispatch',head_branch:'main',head_sha:source,path:'.github/workflows/desktop-candidate.yml',status:'completed',conclusion:'success',repository:{full_name:'Divaaaan/tenebra'}};
  assert.equal(verifyPrepareRun(run,acceptance),true);
  for(const delta of [{id:124},{run_attempt:1},{event:'pull_request'},{head_branch:'feature'},{head_sha:'c'.repeat(40)},{path:'.github/workflows/release.yml'},{status:'in_progress'},{conclusion:'failure'},{repository:{full_name:'other/fork'}}]) assert.throws(()=>verifyPrepareRun({...run,...delta},acceptance));
});
test('artifact must belong to accepted run/commit and exact immutable ID, digest and attempt name',()=>{
  const a={id:456,name:'tenebra-signed-desktop-123-2',expired:false,digest:'sha256:'+'b'.repeat(64),size_in_bytes:500,workflow_run:{id:123,head_sha:source,head_branch:'main'}};
  assert.equal(verifyArtifact(a,acceptance),true);
  for(const delta of [{id:457},{name:'tenebra-signed-desktop-123-1'},{expired:true},{digest:null},{digest:'sha256:'+'f'.repeat(64)},{workflow_run:{id:124,head_sha:source,head_branch:'main'}},{size_in_bytes:0}]) assert.throws(()=>verifyArtifact({...a,...delta},acceptance));
});
test('ZIP entries are flat, unique, bounded and require candidate.json',()=>{
  assert.deepEqual(safeArchiveNames('candidate.json\nTenebra_0.6.0_x64-setup.exe\n'),['candidate.json','Tenebra_0.6.0_x64-setup.exe']);
  for(const raw of ['../candidate.json\n','candidate.json\ncandidate.json\n','candidate.json\na/b\n','candidate.json\n--help\n','file.exe\n','candidate.json\n'+Array.from({length:70},(_,i)=>`file${i}`).join('\n')]) assert.throws(()=>safeArchiveNames(raw));
});

test('publication adapter preserves candidate-bound notes and refuses body changes before mutation',async t=>{
  const notes=Buffer.from('# Tenebra 0.6.0\n\nExact reviewed notes.\n'),promotionId='c'.repeat(64);
  const candidate={version:'0.6.0',sourceSha:source,files:[{name:'release-notes.md',sha256:sha256(notes)}]};
  const mutations=[];let release;
  t.mock.method(globalThis,'fetch',async(url,options)=>{
    if(url.endsWith('/git/ref/heads/main'))return new Response(JSON.stringify({object:{type:'commit',sha:source}}));
    if(url.endsWith('/releases/latest'))return new Response(JSON.stringify({id:11,tag_name:'v0.5.11',draft:false,prerelease:false}));
    if(url.endsWith('/releases')&&options.method==='POST'){
      mutations.push('draft');release={...JSON.parse(options.body),id:42,assets:[]};return new Response(JSON.stringify(release));
    }
    if(url.endsWith('/git/ref/tags/v0.6.0'))return new Response(JSON.stringify({object:{type:'commit',sha:source}}));
    if(url.endsWith('/releases/42')&&options.method==='GET')return new Response(JSON.stringify(release));
    throw Error('unexpected request');
  });
  assert.throws(()=>publisher('test-token',candidate,Buffer.from('# Tenebra 0.6.0\n\nOther notes.\n')));
  const api=publisher('test-token',candidate,notes);
  await api.createDraft('v0.6.0',source,promotionId);
  assert.equal(release.body,`<!-- tenebra-promotion-sha256:${promotionId} -->\n${notes}`);
  await api.verifyUploaded(42,new Map(),{partial:true,promotionId});
  release.body+='Unreviewed claim.\n';
  await assert.rejects(()=>api.publish(42,'v0.6.0',promotionId),/notes changed/);
  assert.deepEqual(mutations,['draft']);
});
test('publication adapter leaves incomplete starter asset intact and fails closed',async t=>{
  const notes=Buffer.from('# Tenebra 0.6.0\n\nNotes.\n'),promotionId='c'.repeat(64),methods=[];
  const candidate={version:'0.6.0',sourceSha:source,files:[{name:'release-notes.md',sha256:sha256(notes)}]};
  t.mock.method(globalThis,'fetch',async(url,options)=>{
    methods.push(options.method);
    if(url.endsWith('/git/ref/tags/v0.6.0'))return new Response(JSON.stringify({object:{type:'commit',sha:source}}));
    if(url.endsWith('/releases/42'))return new Response(JSON.stringify({id:42,tag_name:'v0.6.0',prerelease:false,draft:true,body:`<!-- tenebra-promotion-sha256:${promotionId} -->\n${notes}`,assets:[{id:8,name:'file.exe',state:'starter',size:0}]}));
    throw Error('unexpected request');
  });
  await assert.rejects(()=>publisher('test-token',candidate,notes).verifyUploaded(42,new Map([['file.exe',Buffer.from('bytes')]]),{partial:true,promotionId}),/incomplete/);
  assert.deepEqual(methods,['GET','GET']);
});

for(const changed of ['newer-latest','advanced-main'])test(`publication rechecks ${changed} immediately before making a draft Latest`,async t=>{
  const notes=Buffer.from('# Tenebra 0.6.0\n\nNotes.\n'),promotionId='c'.repeat(64),writes=[];
  const candidate={version:'0.6.0',sourceSha:source,files:[{name:'release-notes.md',sha256:sha256(notes)}]};
  t.mock.method(globalThis,'fetch',async(url,options)=>{
    if(options.method!=='GET'){writes.push(options.method);return new Response('{}');}
    if(url.endsWith('/git/ref/heads/main'))return new Response(JSON.stringify({object:{type:'commit',sha:changed==='advanced-main'?'d'.repeat(40):source}}));
    if(url.endsWith('/releases/latest'))return new Response(JSON.stringify({id:99,tag_name:changed==='newer-latest'?'v0.6.1':'v0.5.11',draft:false,prerelease:false}));
    if(url.endsWith('/git/ref/tags/v0.6.0'))return new Response(JSON.stringify({object:{type:'commit',sha:source}}));
    if(url.endsWith('/releases/42'))return new Response(JSON.stringify({id:42,tag_name:'v0.6.0',prerelease:false,draft:true,body:`<!-- tenebra-promotion-sha256:${promotionId} -->\n${notes}`,assets:[]}));
    throw Error('unexpected request');
  });
  await assert.rejects(()=>publisher('test-token',candidate,notes).publish(42,'v0.6.0',promotionId));assert.deepEqual(writes,[]);
});
