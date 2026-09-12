// Hosted build assembly only; never runs any produced application binary.
import { readdirSync, lstatSync, readFileSync, writeFileSync, mkdirSync, copyFileSync } from 'node:fs';
import { join, resolve, basename } from 'node:path';
import { execFileSync } from 'node:child_process';
import { pathToFileURL } from 'node:url';
import { assertPrepareIdentity, makeCandidate, jsonBytes, bundleNames } from './signed-candidate.mjs';

export function readFlatFiles(dir) {
  const result=new Map();
  for(const name of readdirSync(dir)) {
    if(!/^[A-Za-z0-9_.-]+$/.test(name) || name.startsWith('.')) throw Error('Unsafe candidate filename');
    const path=join(dir,name), stat=lstatSync(path);
    if(!stat.isFile() || stat.isSymbolicLink() || stat.size <= 0 || stat.size > 1024**3) throw Error('Unsafe candidate file');
    result.set(name,readFileSync(path));
  }
  return result;
}
function find(dir, name) {
  const hits=[];
  function visit(path,depth) {
    if(depth>8) throw Error('Bundle directory depth exceeded');
    for(const entry of readdirSync(path,{withFileTypes:true})) {
      if(entry.isSymbolicLink()) continue; // app symlinks are not release assets.
      const next=join(path,entry.name);
      if(entry.isDirectory()) { if(!entry.name.endsWith('.app')) visit(next,depth+1); }
      else if(entry.isFile() && entry.name===name) hits.push(next);
    }
  }
  visit(dir,0); if(hits.length!==1) throw Error(`Expected one bundle ${name}, found ${hits.length}`); return hits[0];
}
export function signFile(path, cliPath=resolve('ui-desktop/node_modules/@tauri-apps/cli/tauri.js')) {
  try { execFileSync(process.execPath,[cliPath,'signer','sign',resolve(path)],{stdio:'pipe',timeout:120000,maxBuffer:1024*1024}); }
  catch { throw Error(`Signing failed for ${basename(path)}`); }
}
export function collect(platform, version, output='candidate-part', operations={signFile}) {
  const names=bundleNames(version), root='ui-desktop/src-tauri/target';
  const sources={
    windows:[[`${root}/release/bundle`,names[0],names[0]]],
    macos:[[`${root}/universal-apple-darwin/release/bundle`,names[1],names[1]],[`${root}/universal-apple-darwin/release/bundle`,'Tenebra.app.tar.gz',names[2]]],
    linux:[[`${root}/release/bundle`,names[3],names[3]],[`${root}/release/bundle`,names[4],names[4]]],
    arch:[['packaging/arch',names[5],names[5]]],
  };
  if(!Object.hasOwn(sources,platform)) throw Error('Unsupported platform');
  mkdirSync(output); // refuse reuse: same-name uploads must never clobber.
  for(const [dir,raw,name] of sources[platform]) {
    const source=platform==='arch'?join(dir,raw):find(dir,raw);
    const stat=lstatSync(source);if(!stat.isFile()||stat.isSymbolicLink())throw Error('Expected a regular bundle');
    copyFileSync(source,join(output,name));
    // Sign the staged filenames, including deb/DMG/Arch. No key in argv/logs.
    operations.signFile(join(output,name));
  }
  const reports={windows:['windows'],macos:['macos-arm64','macos-amd64'],linux:['linux'],arch:['arch']}[platform];
  for(const id of reports) {
    const path=id==='arch'?'packaging/arch/src/tenebra/build/core-buildinfo.json':`core-buildinfo-${id}.json`;
    copyFileSync(path,join(output,`core-buildinfo-${id}.json`));
  }
}
function identity(sourceSha) {
  assertPrepareIdentity({event:process.env.GITHUB_EVENT_NAME,ref:process.env.GITHUB_REF,sha:process.env.GITHUB_SHA,sourceSha,repo:process.env.GITHUB_REPOSITORY});
  if(execFileSync('git',['rev-parse','HEAD'],{encoding:'utf8'}).trim()!==sourceSha) throw Error('Checkout differs from requested source');
}
if(process.argv[1] && import.meta.url===pathToFileURL(process.argv[1]).href) {
  try {
    const [mode,arg,output]=process.argv.slice(2);
    identity(process.env.SOURCE_SHA);
    const config=JSON.parse(readFileSync('ui-desktop/src-tauri/tauri.conf.json'));
    bundleNames(config.version); // refuse a prerelease version before signing.
    if(mode==='identity') { /* validation already completed */ }
    else if(mode==='collect') collect(arg,config.version,output);
    else if(mode==='manifest') {
      const files=readFlatFiles(arg);
      if(files.has('release-notes.md'))throw Error('Release notes must come from the exact source commit');
      files.set('release-notes.md',execFileSync('git',['show',`HEAD:docs/releases/${config.version}.md`],{maxBuffer:65536}));
      const candidate=makeCandidate(files,{repo:process.env.GITHUB_REPOSITORY,sourceSha:process.env.SOURCE_SHA,sourceTree:execFileSync('git',['rev-parse','HEAD^{tree}'],{encoding:'utf8'}).trim(),version:config.version,goVersion:readFileSync('.go-version','utf8').trim(),runId:Number(process.env.GITHUB_RUN_ID),runAttempt:Number(process.env.GITHUB_RUN_ATTEMPT),pubkey:config.plugins.updater.pubkey});
      mkdirSync(output);
      for(const [name,bytes] of files) writeFileSync(join(output,basename(name)),bytes,{flag:'wx'});
      writeFileSync(join(output,'candidate.json'),jsonBytes(candidate),{flag:'wx'});
      console.log(`Verified signed desktop candidate ${candidate.sourceSha}; no tag, release or channel created.`);
    } else throw Error('Usage: candidate-files.mjs identity | collect <platform> [dir] | manifest <input> <output>');
  } catch(error) { console.error(error.message);process.exitCode=1; }
}
