// Pure release policy. No network, credentials, process execution or filesystem
// access: callers supply downloaded bytes and an explicit acceptance receipt.
import { createHash, createPublicKey, verify } from 'node:crypto';
import { verifyBuildInfo } from './verify-core-build.mjs';
import { validateManifest } from './release-lifecycle.mjs';

export const sha256 = bytes => createHash('sha256').update(bytes).digest('hex');
export const jsonBytes = value => Buffer.from(JSON.stringify(value, null, 2) + '\n');
const sha = value => typeof value === 'string' && /^[a-f0-9]{64}$/.test(value);
const commit = value => typeof value === 'string' && /^[a-f0-9]{40}$/.test(value);
const positive = value => Number.isSafeInteger(value) && value > 0;
const stable = value => /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/.test(value);
function requireValue(ok, message) { if (!ok) throw new Error(message); }
function base64(value) {
  requireValue(typeof value === 'string' && value.length > 0 && value.length <= 16384 && /^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/.test(value), 'invalid base64 record');
  const bytes = Buffer.from(value, 'base64');
  requireValue(bytes.toString('base64') === value, 'noncanonical base64 record'); return bytes;
}
function lines(encoded, count) {
  const text = new TextDecoder('utf-8', { fatal: true }).decode(base64(encoded.trim()));
  const result = text.replace(/\r\n/g, '\n').replace(/\n$/, '').split('\n');
  requireValue(result.length === count && result[0].startsWith('untrusted comment: '), 'invalid minisign framing'); return result;
}
// Minisign Ed signs the bytes; ED signs their BLAKE2b-512 digest. Both also
// authenticate signature || trusted-comment separately (upstream minisign).
export function verifyUpdaterSignature(bytes, encoded, encodedKey) {
  requireValue(Buffer.isBuffer(bytes), 'payload bytes required');
  const s = lines(encoded, 4), k = lines(encodedKey, 2), signature = base64(s[1]), key = base64(k[1]);
  requireValue(key.length === 42 && key.subarray(0,2).toString() === 'Ed' && signature.length === 74 && ['Ed','ED'].includes(signature.subarray(0,2).toString()), 'unsupported signature algorithm');
  requireValue(signature.subarray(2,10).equals(key.subarray(2,10)), 'signing key ID differs');
  requireValue(s[2].startsWith('trusted comment: ') && !/[\x00-\x08\x0a-\x1f\x7f]/.test(s[2]), 'invalid trusted comment');
  const publicKey = createPublicKey({ key:Buffer.concat([Buffer.from('302a300506032b6570032100','hex'),key.subarray(10)]), format:'der', type:'spki' });
  const message = signature.subarray(0,2).toString() === 'ED' ? createHash('blake2b512').update(bytes).digest() : bytes;
  requireValue(verify(null, message, publicKey, signature.subarray(10)), 'artifact signature invalid');
  const global = base64(s[3]);
  requireValue(global.length === 64 && verify(null,Buffer.concat([signature.subarray(10),Buffer.from(s[2].slice('trusted comment: '.length))]),publicKey,global), 'trusted comment signature invalid');
  return { valid:true, keyId:key.subarray(2,10).toString('hex') };
}
export function assertPrepareIdentity({ event, ref, sha:actual, sourceSha, repo, expectedRepo='Divaaaan/tenebra' }) {
  requireValue(event === 'workflow_dispatch' && ref === 'refs/heads/main' && repo === expectedRepo && expectedRepo === 'Divaaaan/tenebra' && commit(sourceSha) && actual === sourceSha, 'signing requires exact main workflow dispatch commit'); return true;
}
export const buildReports = {
  'core-buildinfo-windows.json':['windows','amd64'],
  'core-buildinfo-macos-arm64.json':['darwin','arm64'],
  'core-buildinfo-macos-amd64.json':['darwin','amd64'],
  'core-buildinfo-linux.json':['linux','amd64'],
  'core-buildinfo-arch.json':['linux','amd64'],
};
export function bundleNames(version) {
  requireValue(stable(version), 'stable semantic version required');
  return [`Tenebra_${version}_x64-setup.exe`, `Tenebra_${version}_universal.dmg`, 'Tenebra_universal.app.tar.gz', `Tenebra_${version}_amd64.deb`, `Tenebra_${version}_amd64.AppImage`, `tenebra-${version}-1-x86_64.pkg.tar.zst`];
}
export function releaseNotes(bytes,version) {
  requireValue(Buffer.isBuffer(bytes) && bytes.length>0 && bytes.length<=65536,'release notes size invalid');
  const notes=new TextDecoder('utf-8',{fatal:true}).decode(bytes);
  requireValue(notes.startsWith(`# Tenebra ${version}\n`) && !notes.includes('tenebra-promotion-sha256:') && !/[\x00-\x08\x0b-\x1f\x7f]/.test(notes),'release notes version or framing invalid');
  return notes;
}
export function makeCandidate(files, { repo, sourceSha, sourceTree, version, goVersion, runId, runAttempt, pubkey }) {
  requireValue(repo === 'Divaaaan/tenebra' && commit(sourceSha) && commit(sourceTree) && positive(runId) && positive(runAttempt) && /^\d+\.\d+\.\d+$/.test(goVersion), 'invalid candidate provenance');
  const bundles=bundleNames(version), names=[...bundles.flatMap(name=>[name,name+'.sig']),...Object.keys(buildReports),'release-notes.md'].sort();
  requireValue(files instanceof Map && files.size === names.length && [...files.keys()].every(name=>names.includes(name)), 'candidate contains missing, extra or unsafe paths');
  const rows=names.map(name=>{ const bytes=files.get(name); requireValue(Buffer.isBuffer(bytes) && bytes.length > 0 && bytes.length <= 1024**3, 'invalid candidate file size'); return {name,bytes:bytes.length,sha256:sha256(bytes)}; });
  for (const name of bundles) verifyUpdaterSignature(files.get(name),files.get(name+'.sig').toString().trim(),pubkey);
  for (const [name,[os,arch]] of Object.entries(buildReports)) {
    const report=JSON.parse(files.get(name));
    requireValue(report.goVersion === goVersion && report.revision === sourceSha && report.os === os && report.arch === arch && sha(report.sha256), 'core provenance differs');
    verifyBuildInfo(report.metadata,{goVersion,revision:sourceSha,os,arch});
  }
  const platforms={};
  for (const [keys,name] of [
    [['windows-x86_64','windows-x86_64-nsis'],bundles[0]],
    [['darwin-x86_64','darwin-aarch64','darwin-x86_64-app','darwin-aarch64-app'],bundles[2]],
    [['linux-x86_64','linux-x86_64-appimage'],bundles[4]],
    [['linux-x86_64-deb'],bundles[3]],
  ]) for(const key of keys) platforms[key]={url:`https://github.com/${repo}/releases/download/v${version}/${name}`,signature:files.get(name+'.sig').toString().trim()};
  const updater={version,notes:releaseNotes(files.get('release-notes.md'),version),platforms};
  validateManifest(updater,{tag:`v${version}`,repo,assets:names.map(name=>({name}))});
  return {schema:1,kind:'tenebra-signed-desktop-candidate',repo,sourceSha,sourceTree,version,goVersion,runId,runAttempt,workflowPath:'.github/workflows/desktop-candidate.yml',pubkeySha256:sha256(Buffer.from(pubkey)),files:rows,updater};
}
export function verifyCandidate(candidate, files, pubkey) {
  requireValue(JSON.stringify(candidate) === JSON.stringify(makeCandidate(files,{...candidate,pubkey})), 'candidate manifest/provenance differs from actual bytes'); return true;
}
export function verifyAcceptance(candidate, acceptance, artifact) {
  requireValue(acceptance.schema === 1 && acceptance.kind === 'tenebra-desktop-acceptance' && acceptance.state === 'pass' && acceptance.version === candidate.version && acceptance.sourceSha === candidate.sourceSha && acceptance.sourceTree === candidate.sourceTree && acceptance.prepareRunId === candidate.runId && acceptance.prepareAttempt === candidate.runAttempt, 'acceptance source/run differs');
  requireValue(positive(artifact.artifactId) && sha(artifact.artifactSha256) && sha(artifact.manifestSha256) && acceptance.artifactId === artifact.artifactId && acceptance.artifactSha256 === artifact.artifactSha256 && acceptance.manifestSha256 === artifact.manifestSha256, 'acceptance artifact identity differs');
  requireValue(JSON.stringify(acceptance.files) === JSON.stringify(candidate.files), 'accepted file hashes differ');
  requireValue(Array.isArray(acceptance.evidence) && acceptance.evidence.length > 0 && acceptance.evidence.length <= 32 && acceptance.evidence.every(e=>typeof e.kind === 'string' && /^[a-z][a-z0-9-]{2,80}$/.test(e.kind) && sha(e.sha256)), 'native acceptance evidence hashes required');
  requireValue(acceptance.evidence.some(e=>e.kind==='windows-install-service-ui-tunnel-protection'),'combined Windows native acceptance evidence required'); return true;
}
export async function promoteCandidate({candidate,files,acceptance,artifact,context,api}) {
  verifyCandidate(candidate,files,context.pubkey); verifyAcceptance(candidate,acceptance,artifact);
  const tag=`v${candidate.version}`;
  const intent={schema:1,kind:'tenebra-desktop-promotion',repo:candidate.repo,tag,sourceSha:candidate.sourceSha,prepareRunId:candidate.runId,prepareAttempt:candidate.runAttempt,...artifact,acceptanceSha256:sha256(jsonBytes(acceptance))};
  const promotionId=sha256(jsonBytes(intent));
  const uploaded=new Map(files);
  uploaded.set('latest.json',jsonBytes(candidate.updater)); uploaded.set('beta.json',jsonBytes(candidate.updater));
  uploaded.set('candidate.json',jsonBytes(candidate)); uploaded.set('acceptance.json',jsonBytes(acceptance));
  uploaded.set('promotion.json',jsonBytes(intent));
  function own(state) {
    requireValue(!state.tag || (state.tag.type==='commit' && state.tag.sha===candidate.sourceSha),'existing tag differs; never retag');
    requireValue(!state.release || (positive(state.release.id) && typeof state.release.isDraft==='boolean' && state.release.promotionId===promotionId),'existing release is not this accepted promotion');
    requireValue(!state.tag || state.release,'unknown existing tag without owned release');
    requireValue(!state.release || state.release.isDraft || state.tag,'published release lost its tag');
    return state;
  }
  let state=own(await api.getState(tag));
  // Persist ownership BEFORE separately creating the immutable tag. A timeout
  // after either write can then be reconciled on the next identical request.
  if(!state.release){await api.createDraft(tag,candidate.sourceSha,promotionId);state=own(await api.getState(tag));}
  requireValue(state.release,'created draft is not observable');
  const release=state.release;
  if(release.isDraft){
    await api.verifyUploaded(release.id,uploaded,{partial:true,promotionId});
    if(!state.tag)await api.createTag(tag,candidate.sourceSha);
    own(await api.getState(tag));
    for(const [name,bytes] of uploaded)await api.ensureAsset(release.id,name,bytes,promotionId);
    await api.verifyUploaded(release.id,uploaded,{promotionId});
    await api.publish(release.id,tag,promotionId);
  } else await api.verifyUploaded(release.id,uploaded,{promotionId});
  await api.updateChannel(tag,candidate.updater);
  return {tag,releaseId:release.id,sourceSha:candidate.sourceSha,promotionId,rebuilt:false};
}
