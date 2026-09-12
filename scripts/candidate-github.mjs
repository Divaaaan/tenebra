// The only network/mutation adapter for manual signed candidates. Prepare has
// no contents:write; promotion uses only the Actions GITHUB_TOKEN.
import { readFileSync, writeFileSync, mkdtempSync, unlinkSync, rmdirSync, mkdirSync } from 'node:fs';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { execFileSync } from 'node:child_process';
import { pathToFileURL } from 'node:url';
import { sha256, jsonBytes, verifyCandidate, promoteCandidate, assertPrepareIdentity, releaseNotes, assertStablePromotionState } from './signed-candidate.mjs';
import { publishCompleteRelease } from './release-lifecycle.mjs';
import { githubReleaseApi } from './release-api.mjs';
const repo='Divaaaan/tenebra', base=`https://api.github.com/repos/${repo}`;
const positive=value=>Number.isSafeInteger(value)&&value>0;
function demand(ok,message){if(!ok)throw Error(message);}
export function verifyPrepareRun(run,a) {
  demand(run.id===a.prepareRunId && run.run_attempt===a.prepareAttempt && run.event==='workflow_dispatch' && run.head_branch==='main' && run.head_sha===a.sourceSha && run.path==='.github/workflows/desktop-candidate.yml' && run.status==='completed' && run.conclusion==='success' && run.repository?.full_name===repo,'prepare run provenance differs'); return true;
}
export function verifyArtifact(artifact,a) {
  demand(artifact.id===a.artifactId && artifact.name===`tenebra-signed-desktop-${a.prepareRunId}-${a.prepareAttempt}` && artifact.expired===false && artifact.digest===`sha256:${a.artifactSha256}` && positive(artifact.size_in_bytes) && artifact.size_in_bytes<=2*1024**3 && artifact.workflow_run?.id===a.prepareRunId && artifact.workflow_run?.head_sha===a.sourceSha && artifact.workflow_run?.head_branch==='main','artifact provenance differs'); return true;
}
export function safeArchiveNames(text) {
  const names=text.trimEnd().split('\n');
  demand(names.length>1 && names.length<=64 && names.includes('candidate.json') && new Set(names).size===names.length && names.every(n=>/^[A-Za-z0-9][A-Za-z0-9_.-]{0,180}$/.test(n) && !n.includes('..')),'unsafe or duplicate ZIP entries'); return names;
}
function http(token) {
  demand(typeof token==='string' && token.length>0,'GitHub token required');
  async function call(url,{method='GET',body,octet=false,accept=octet?'application/octet-stream':'application/vnd.github+json',limit=20*1024**2}={}) {
    demand(url.startsWith(base+'/') || url.startsWith(`https://uploads.github.com/repos/${repo}/`),'unexpected GitHub request destination');
    const response=await fetch(url,{method,headers:{Authorization:`Bearer ${token}`,Accept:accept,'X-GitHub-Api-Version':'2022-11-28',...(body?{'Content-Type':Buffer.isBuffer(body)?'application/octet-stream':'application/json'}:{})},body:body?(Buffer.isBuffer(body)?body:JSON.stringify(body)):undefined,signal:AbortSignal.timeout(180000)});
    if(!response.ok)throw Object.assign(Error(`GitHub ${method} operation failed (HTTP ${response.status})`),{status:response.status});
    demand(Number(response.headers.get('content-length')??0)<=limit,'GitHub response exceeds size bound');
    const chunks=[];let size=0;
    for await(const chunk of response.body){size+=chunk.length;demand(size<=limit,'GitHub response exceeds size bound');chunks.push(Buffer.from(chunk));}
    const bytes=Buffer.concat(chunks);return octet?bytes:(bytes.length?JSON.parse(bytes):null);
  }
  return call;
}
export async function acquireCandidate(a,pubkey,token) {
  demand(positive(a.prepareRunId) && positive(a.prepareAttempt) && positive(a.artifactId) && /^[a-f0-9]{40}$/.test(a.sourceSha) && /^[a-f0-9]{64}$/.test(a.artifactSha256),'explicit acquisition pins required');
  const call=http(token),run=await call(`${base}/actions/runs/${a.prepareRunId}`);verifyPrepareRun(run,a);
  const asset=await call(`${base}/actions/artifacts/${a.artifactId}`);verifyArtifact(asset,a);
  // Actions negotiates its archive redirect through the JSON API media type;
  // the redirected response is still binary. Release assets use octet-stream.
  const archive=await call(`${base}/actions/artifacts/${a.artifactId}/zip`,{octet:true,accept:'application/vnd.github+json',limit:2*1024**3});
  demand(sha256(archive)===a.artifactSha256,'downloaded ZIP SHA differs');
  const temporary=mkdtempSync(join(tmpdir(),'tenebra-candidate-')),zip=join(temporary,'candidate.zip');
  const files=new Map();
  try {
    writeFileSync(zip,archive,{flag:'wx'});
    // Read entries to buffers, never unzip paths onto a filesystem. Exact flat
    // names, duplicates, output sizes and every resulting SHA are checked.
    const names=safeArchiveNames(execFileSync('unzip',['-Z1',zip],{encoding:'utf8',timeout:30000,maxBuffer:65536}));
    let total=0;
    for(const name of names){const bytes=execFileSync('unzip',['-p',zip,name],{timeout:120000,maxBuffer:1024**3});total+=bytes.length;demand(total<=2*1024**3,'expanded candidate exceeds bound');files.set(name,bytes);}
  } finally {unlinkSync(zip);rmdirSync(temporary);}
  const manifestBytes=files.get('candidate.json');demand(manifestBytes?.length<=1024**2,'candidate manifest size invalid');
  const candidate=JSON.parse(manifestBytes),manifestSha256=sha256(manifestBytes);files.delete('candidate.json');
  demand(manifestBytes.equals(jsonBytes(candidate)),'candidate manifest encoding is not canonical');
  verifyCandidate(candidate,files,pubkey);
  demand(candidate.sourceSha===a.sourceSha && candidate.runId===a.prepareRunId && candidate.runAttempt===a.prepareAttempt,'candidate disagrees with Actions provenance');
  const commit=await call(`${base}/git/commits/${a.sourceSha}`);demand(commit.sha===a.sourceSha && commit.tree.sha===candidate.sourceTree,'source tree differs from exact Git commit');
  return {candidate,files,artifact:{artifactId:a.artifactId,artifactSha256:a.artifactSha256,manifestSha256},run:{id:run.id,attempt:run.run_attempt,headSha:run.head_sha},context:{pubkey}};
}
export function publisher(token,candidate,notesBytes) {
  const call=http(token),tag=`v${candidate.version}`;
  const notes=releaseNotes(notesBytes,candidate.version);
  demand(sha256(notesBytes)===candidate.files.find(f=>f.name==='release-notes.md')?.sha256,'release notes are not candidate-bound');
  const body=promotionId=>`<!-- tenebra-promotion-sha256:${promotionId} -->\n${notes}`;
  function marker(release){const found=[...(release.body??'').matchAll(/<!-- tenebra-promotion-sha256:([a-f0-9]{64}) -->/g)];return found.length===1?found[0][1]:null;}
  async function readTag(){try{return (await call(`${base}/git/ref/tags/${tag}`)).object;}catch(e){if(e.status===404)return null;throw e;}}
  async function assertTag(allowMissing=false){const ref=await readTag();demand((!ref&&allowMissing)||(ref?.type==='commit'&&ref.sha===candidate.sourceSha),'stable tag changed or disappeared');}
  async function ownedRelease(id,promotionId){const r=await call(`${base}/releases/${id}`);demand(r.id===id&&r.tag_name===tag&&r.prerelease===false&&marker(r)===promotionId&&r.body===body(promotionId),'promotion ownership or release notes changed');return r;}
  async function assertPublicationReady(ownedReleaseId=null){
    const main=(await call(`${base}/git/ref/heads/main`)).object;
    let latest;try{latest=await call(`${base}/releases/latest`);}catch(e){if(e.status!==404)throw e;latest=null;}
    assertStablePromotionState(candidate,main,latest,ownedReleaseId);
  }
  async function checkAsset(asset,bytes){demand(positive(asset.id)&&asset.state==='uploaded'&&asset.size===bytes.length,'existing asset is incomplete or has another size');const actual=await call(`${base}/releases/assets/${asset.id}`,{octet:true,limit:bytes.length});demand(sha256(actual)===sha256(bytes),'existing bytes differ; never overwrite');}
  return {
    assertPublicationReady,
    async getState(name){
      let releases=[],complete=false;
      for(let page=1;page<=10;page++){const list=await call(`${base}/releases?per_page=100&page=${page}`);releases.push(...list.filter(r=>r.tag_name===name));if(list.length<100){complete=true;break;}}
      demand(complete&&releases.length<=1,'release listing is incomplete or ambiguous');
      const r=releases[0];return {tag:await readTag(),release:r?{id:r.id,isDraft:r.draft,promotionId:marker(r)}:null};
    },
    async createTag(name,source){await assertPublicationReady();await call(`${base}/git/refs`,{method:'POST',body:{ref:`refs/tags/${name}`,sha:source}});},
    async createDraft(name,source,promotionId){await assertPublicationReady();return call(`${base}/releases`,{method:'POST',body:{tag_name:name,target_commitish:source,name:`Tenebra ${name}`,draft:true,prerelease:false,body:body(promotionId)}});},
    async ensureAsset(id,name,bytes,promotionId){
      await assertTag();const release=await ownedRelease(id,promotionId);demand(release.draft===true,'cannot add assets to a public release');
      const existing=release.assets.filter(a=>a.name===name);demand(existing.length<=1,'duplicate release asset');
      if(existing.length){await checkAsset(existing[0],bytes);return;}
      await call(`https://uploads.github.com/repos/${repo}/releases/${id}/assets?name=${encodeURIComponent(name)}`,{method:'POST',body:bytes});
    },
    async verifyUploaded(id,files,{partial=false,promotionId}={}){
      await assertTag(partial);const release=await ownedRelease(id,promotionId);
      demand(!partial||release.draft===true,'public release changed during preparation');
      demand((partial||release.assets.length===files.size)&&new Set(release.assets.map(a=>a.name)).size===release.assets.length&&release.assets.every(a=>files.has(a.name)),'release asset set changed');
      for(const asset of release.assets)await checkAsset(asset,files.get(asset.name));
    },
    async publish(id,name,promotionId){await assertTag();const release=await ownedRelease(id,promotionId);await assertPublicationReady(id);if(release.draft)await call(`${base}/releases/${id}`,{method:'PATCH',body:{draft:false,prerelease:false,make_latest:'true'}});},
    async updateChannel(name){await assertTag();const state=await this.getState(name);await assertPublicationReady(state.release?.id??null);delete process.env.GH_TOKEN;await publishCompleteRelease({tag:name,repo,api:githubReleaseApi(repo,name)});},
  };
}
if(process.argv[1] && import.meta.url===pathToFileURL(process.argv[1]).href) {
  try {
    const [mode,input,output]=process.argv.slice(2);
    demand(mode==='inspect'||mode==='promote','usage: candidate-github.mjs inspect <request.json> <new-dir> | promote');
    const raw=mode==='promote'?process.env.ACCEPTANCE_JSON:readFileSync(input,'utf8');demand(raw?.length<=65536,'acceptance input size invalid');const acceptance=JSON.parse(raw);
    const config=JSON.parse(readFileSync('ui-desktop/src-tauri/tauri.conf.json'));
    demand(execFileSync('git',['rev-parse','HEAD'],{encoding:'utf8'}).trim()===acceptance.sourceSha,'verification checkout must equal candidate source SHA');
    if(mode==='promote') {
      demand(process.env.GITHUB_ACTIONS==='true','promotion is restricted to the reviewed GitHub workflow');
      assertPrepareIdentity({event:process.env.GITHUB_EVENT_NAME,ref:process.env.GITHUB_REF,sha:process.env.GITHUB_SHA,sourceSha:acceptance.sourceSha,repo:process.env.GITHUB_REPOSITORY});
      demand(process.env.SOURCE_SHA===acceptance.sourceSha,'dispatch source differs from acceptance');
    }
    const acquired=await acquireCandidate(acceptance,config.plugins.updater.pubkey,process.env.GITHUB_TOKEN||(mode==='inspect'?process.env.GH_TOKEN:undefined));
    demand(acquired.candidate.goVersion===readFileSync('.go-version','utf8').trim() && acquired.candidate.version===config.version,'candidate toolchain/version differs from pinned source');
    demand(acquired.files.get('release-notes.md').equals(execFileSync('git',['show',`HEAD:docs/releases/${config.version}.md`],{maxBuffer:65536})),'release notes differ from exact source commit');
    if(mode==='inspect') {
      mkdirSync(output);
      for(const [name,bytes] of acquired.files)writeFileSync(join(output,name),bytes,{flag:'wx'});
      writeFileSync(join(output,'candidate.json'),jsonBytes(acquired.candidate),{flag:'wx'});
      writeFileSync(join(output,'acquisition.json'),jsonBytes({schema:1,kind:'tenebra-signed-candidate-acquisition',state:'verified-only',...acquired.artifact,run:acquired.run,sourceSha:acquired.candidate.sourceSha,sourceTree:acquired.candidate.sourceTree,files:acquired.candidate.files,executed:false}),{flag:'wx'});
      console.log('Signed candidate verified and downloaded; no application was executed.');
    } else console.log(JSON.stringify(await promoteCandidate({...acquired,acceptance,api:publisher(process.env.GITHUB_TOKEN,acquired.candidate,acquired.files.get('release-notes.md'))})));
  } catch(error){console.error(error.message);process.exitCode=1;}
}
