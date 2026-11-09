import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, cpSync, chmodSync, symlinkSync, existsSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync, execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const node = process.execPath;
function fixture(t, { version = '0.6.2', failBuild = false } = {}) {
  const home = mkdtempSync(join(tmpdir(), 'tenebra-distribution-test-'));
  t.after(() => rmSync(home, { recursive: true, force: true }));
  const repo = join(home, 'repo'), bin = join(home, 'tools'), trace = join(home, 'trace');
  mkdirSync(repo); mkdirSync(bin); symlinkSync(node, join(bin, 'node'));
  const put = (path, value, executable = false) => {
    const target = join(repo, path); mkdirSync(dirname(target), { recursive: true });
    writeFileSync(target, value); if (executable) chmodSync(target, 0o755);
  };
  for (const file of ['scripts/macos/build-distribution.sh', 'scripts/macos/distribution.mjs', 'scripts/set-version.mjs', 'scripts/verify-core-build.mjs']) {
    mkdirSync(dirname(join(repo, file)), { recursive: true }); cpSync(join(root, file), join(repo, file));
  }
  put('.go-version', '1.26.8\n');
  put('.gitignore', 'ui-desktop/node_modules/\nui-desktop/src-tauri/target/\nui-desktop/src-tauri/binaries/\nui-desktop/src-tauri/resources/\n');
  put('ui-desktop/package.json', JSON.stringify({ name: 'tenebra-desktop', version }));
  put('ui-desktop/package-lock.json', JSON.stringify({ name: 'tenebra-desktop', version, packages: { '': { name: 'tenebra-desktop', version } } }));
  put('ui-desktop/src-tauri/tauri.conf.json', JSON.stringify({ productName: 'Tenebra', version }));
  put('ui-desktop/src-tauri/tauri.ci.conf.json', '{"bundle":{"createUpdaterArtifacts":false}}');
  put('ui-desktop/src-tauri/Cargo.toml', `version = "${version}"\n`);
  put('ui-desktop/src-tauri/Cargo.lock', `name = "tenebra-desktop"\nversion = "${version}"\n`);
  put('core/buildinfo/buildinfo.go', `const Version = "${version}"\n`);
  put('packaging/arch/PKGBUILD', `pkgver=${version}\n`);
  put('scripts/fetch-resources.sh', `#!/bin/bash\nset -eu\nprintf '%s\\n' '{"tool":"fetch"}' >> "$TRACE"\nmkdir -p ui-desktop/src-tauri/resources\nprintf '%s' '{"archs":["arm64","x86_64"]}' > ui-desktop/src-tauri/resources/sing-box\nfor name in geoip-ru geosite-ru geosite-ads; do printf rules > "ui-desktop/src-tauri/resources/$name.srs"; done\n`);
  put('scripts/macos/build-pkg.sh', `#!/bin/bash\nset -eu\nwhile [[ $# -gt 0 ]]; do case "$1" in --output) output="$2"; shift 2;; --app|--go|--arch) shift 2;; *) exit 2;; esac; done\nprintf '%s\\n' '{"tool":"pkg"}' >> "$TRACE"\nprintf fixture-package > "$output"\n`);
  const fake = join(bin, 'fake.mjs');
  writeFileSync(fake, `#!${node}\n` + String.raw`
import { basename, join, dirname } from 'node:path';
import { readFileSync, writeFileSync, mkdirSync, appendFileSync, cpSync, existsSync, realpathSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
const tool = basename(process.argv[1]), args = process.argv.slice(2);
const rustToolchain = process.env.RUSTUP_TOOLCHAIN || (process.env.RUST_OVERRIDE_DIR && realpathSync(process.cwd()) === realpathSync(process.env.RUST_OVERRIDE_DIR) ? 'fixture-override-aarch64-apple-darwin' : 'fixture-default-aarch64-apple-darwin');
appendFileSync(process.env.TRACE, JSON.stringify({ tool, args, cwd: process.cwd(), goWork: process.env.GOWORK, rustToolchain }) + '\n');
if (process.env.EXPECT_GOWORK_OFF && ['go', 'npm'].includes(tool) && process.env.GOWORK !== 'off') throw Error('inherited Go workspace reached the build boundary');
const read = f => JSON.parse(readFileSync(f, 'utf8'));
const out = f => { mkdirSync(dirname(f), {recursive: true}); return f; };
if (tool === 'uname') console.log(args[0] === '-s' ? 'Darwin' : 'arm64');
else if (tool === 'go') {
  if (args[0] === 'version' && args[1] !== '-m') console.log('go version go' + (process.env.WRONG_GO || '1.26.8') + ' darwin/arm64');
  else if (args[0] === 'version') {
    const b=read(args[2]);
    console.log(args[2] + ': go1.26.8\n\tbuild\tGOOS=darwin\n\tbuild\tGOARCH=' + b.arch + '\n\tbuild\tvcs.revision=' + b.revision + '\n\tbuild\tvcs.modified=false');
  } else if (args[0] === 'build') {
    if (!existsSync('.git/HEAD')) throw Error('build snapshot must have a normal .git directory');
    const target=args[args.indexOf('-o')+1];
    writeFileSync(out(target), JSON.stringify({arch:process.env.GOARCH, archs:[process.env.GOARCH==='amd64'?'x86_64':'arm64'], revision:execFileSync('git',['rev-parse','HEAD'],{encoding:'utf8'}).trim()}));
  } else throw Error('unexpected go args');
} else if (tool === 'npm') {
  if (args[0] === '--version') console.log('11.0.0');
  else if (args[0] === 'ci') mkdirSync('node_modules',{recursive:true});
  else {
    if(process.env.FAIL_BUILD) process.exit(7);
    execFileSync('cargo',['--version']);
    execFileSync('rustc',['-vV']);
    if(!args.includes('--locked') || !args.includes('--config')) throw Error('locked CI Tauri build required');
    const config=read(args[args.indexOf('--config')+1]);
    if(config.bundle.createUpdaterArtifacts!==false) throw Error('updater artifacts must be disabled');
    const target=args[args.indexOf('--target')+1];
    const app='src-tauri/target/'+target+'/release/bundle/macos/Tenebra.app';
    mkdirSync(app+'/Contents/MacOS',{recursive:true});
    mkdirSync(app+'/Contents/Resources/resources',{recursive:true});
    writeFileSync(app+'/Contents/MacOS/Tenebra','fixture');
    cpSync('src-tauri/binaries/tenebra-core-'+target,app+'/Contents/MacOS/tenebra-core');
    cpSync('src-tauri/resources',app+'/Contents/Resources/resources',{recursive:true});
  }
} else if (tool==='lipo') {
  if(args[0]==='-archs') console.log(read(args[1]).archs.join(' '));
  else if(args[0]==='-create') writeFileSync(out(args[args.indexOf('-output')+1]),JSON.stringify({archs:['arm64','x86_64'],slices:args.slice(1,args.indexOf('-output')).map(read)}));
  else { const b=read(args[0]); const arch=args[args.indexOf('-thin')+1]; writeFileSync(out(args[args.indexOf('-output')+1]),JSON.stringify(b.slices.find(s=>s.archs.includes(arch)))); }
} else if(tool==='rustup') console.log(args[0]==='show' ? rustToolchain+' (fixture selection)' : 'aarch64-apple-darwin\nx86_64-apple-darwin');
else if(tool==='rustc') console.log('rustc 1.97.0\nhost: aarch64-apple-darwin\ntoolchain: '+rustToolchain);
else if(tool==='cargo') console.log('cargo 1.97.0 (toolchain '+rustToolchain+')');
else if(tool==='xcrun') console.log('Xcode 99.0');
else if(tool==='sw_vers') console.log('15.0');
else if(tool!=='codesign') throw Error('unknown fake tool '+tool);
`);
  chmodSync(fake, 0o755);
  for(const tool of ['go','npm','uname','rustc','cargo','rustup','lipo','codesign','xcrun','sw_vers']) symlinkSync(fake, join(bin, tool));
  const git = (...args) => execFileSync('git', args, { cwd: repo, encoding:'utf8', stdio:['ignore','pipe','pipe'] }).trim();
  git('init','-b','main'); git('config','user.name','Fixture'); git('config','user.email','fixture@example.invalid'); git('add','.'); git('commit','-m','fixture');
  const sha=git('rev-parse','HEAD'), output=join(home,'output');
  const run=(args=[], env={}) => spawnSync('/bin/bash',[join(repo,'scripts/macos/build-distribution.sh'),'--output-dir',output,'--go',join(bin,'go'),'--node',join(bin,'node'),...args],{cwd:repo, encoding:'utf8',env:{...process.env,PATH:bin+':'+dirname(node)+':'+process.env.PATH,TRACE:trace,...(failBuild?{FAIL_BUILD:'1'}:{}),...env}});
  return {home,repo,output,sha,git,put,run,trace:()=>readFileSync(trace,'utf8').trim().split('\n').map(line=>JSON.parse(line))};
}

test('plan selects coherent future version and exact clean source without generating output', t=>{
  const f=fixture(t); const result=f.run(['--plan-json']);
  assert.equal(result.status,0,result.stderr); const plan=JSON.parse(result.stdout);
  assert.equal(plan.version,'0.6.2'); assert.equal(plan.source.commit,f.sha); assert.equal(plan.arch,'arm64');
  assert.equal(plan.package,'Tenebra-0.6.2-macos-arm64.pkg'); assert.equal(plan.goVersion,'1.26.8');
  assert.equal(existsSync(f.output),false);
});

test('dirty source and inconsistent version fail before fetching or producing final output', t=>{
  const f=fixture(t); f.put('unexpected.txt','dirty');
  let result=f.run(); assert.notEqual(result.status,0); assert.match(result.stderr,/clean|dirty/i); assert.equal(existsSync(f.output),false);
  rmSync(join(f.repo,'unexpected.txt')); f.put('core/buildinfo/buildinfo.go','const Version = "0.6.1"\n');
  f.git('add','.'); f.git('commit','-m','mismatched version');
  result=f.run(); assert.notEqual(result.status,0); assert.match(result.stderr,/version|disagree/i); assert.equal(existsSync(f.output),false);
});

test('wrong pinned Go fails before fetch and leaves no output', t=>{
  const f=fixture(t); const result=f.run([], {WRONG_GO:'1.26.7'});
  assert.notEqual(result.status,0); assert.match(result.stderr,/Go|go/i); assert.equal(existsSync(f.output),false);
  assert.equal(f.trace().some(event=>event.tool==='fetch'),false);
});

for(const arch of ['arm64','universal']) test(`${arch} pipeline snapshots worktree, verifies cores, signs nested engine before app, and hashes final artifacts`, t=>{
  const f=fixture(t); const wt=join(f.home,'linked'); f.git('worktree','add','--detach',wt,f.sha);
  const result=spawnSync('/bin/bash',[join(wt,'scripts/macos/build-distribution.sh'),'--output-dir',f.output,'--arch',arch,'--go',join(f.home,'tools/go'),'--node',join(f.home,'tools/node')],{encoding:'utf8',env:{...process.env,PATH:join(f.home,'tools')+':'+dirname(node)+':'+process.env.PATH,TRACE:join(f.home,'trace')}});
  assert.equal(result.status,0,result.stderr+'\n'+result.stdout);
  const manifest=JSON.parse(readFileSync(join(f.output,'manifest.json'),'utf8'));
  assert.equal(manifest.version,'0.6.2'); assert.equal(manifest.source.commit,f.sha); assert.equal(manifest.arch,arch);
  assert.equal(manifest.nativeAcceptance,'not_run'); assert.equal(manifest.signing.executables,'ad-hoc');
  for(const file of manifest.artifacts) assert.equal(createHash('sha256').update(readFileSync(join(f.output,file.name))).digest('hex'),file.sha256);
  assert.match(readFileSync(join(f.output,'SHA256SUMS'),'utf8'),/manifest.json/);
  const events=f.trace(), tauri=events.findIndex(e=>e.tool==='npm'&&e.args.includes('--target'));
  assert.ok(events.slice(0,tauri).some(e=>e.tool==='codesign'&&e.args.at(-1).endsWith('/resources/sing-box')&&e.args.includes('--sign')));
  const outer=events.findIndex(e=>e.tool==='codesign'&&e.args.includes('--sign')&&e.args.at(-1).endsWith('.app'));
  assert.ok(events.slice(tauri,outer).some(e=>e.tool==='codesign'&&e.args.includes('--sign')&&e.args.at(-1).endsWith('/resources/sing-box')));
  const builds=events.filter(e=>e.tool==='go'&&e.args[0]==='build'); assert.equal(builds.length,arch==='universal'?2:1);
  assert.equal(f.git('status','--porcelain'),''); assert.equal(f.git('rev-parse','HEAD'),f.sha);
  assert.equal(existsSync(builds[0].cwd),false,'own scratch clone is removed');
});

test('failed Tauri build never publishes output or leaves scratch clone', t=>{
  const f=fixture(t,{failBuild:true}); const result=f.run();
  assert.notEqual(result.status,0); assert.match(result.stderr,/failed \(7\)/); assert.equal(existsSync(f.output),false);
  const build=f.trace().find(e=>e.tool==='go'&&e.args[0]==='build'); assert.equal(existsSync(build.cwd),false);
});

test('source version is read dynamically rather than relabeling an older commit', t=>{
  const f=fixture(t,{version:'0.6.1'}); const result=f.run(['--plan-json','--arch','universal']);
  assert.equal(result.status,0,result.stderr); const plan=JSON.parse(result.stdout);
  assert.equal(plan.version,'0.6.1'); assert.equal(plan.package,'Tenebra-0.6.1-macos-universal.pkg');
  assert.deepEqual(plan.slices.map(slice=>slice.goArch),['arm64','amd64']);
});

test('an existing output is preserved and relative output paths are rejected', t=>{
  const f=fixture(t); mkdirSync(f.output); writeFileSync(join(f.output,'keep'),'existing');
  let result=f.run(); assert.notEqual(result.status,0); assert.match(result.stderr,/already exists/);
  assert.equal(readFileSync(join(f.output,'keep'),'utf8'),'existing');
  result=f.run(['--output-dir','relative']); assert.notEqual(result.status,0); assert.match(result.stderr,/absolute/);
});

test('CI only builds explicitly selected source and uploads artifacts with pinned official actions', ()=>{
  const workflow=readFileSync(join(root,'.github/workflows/macos-package.yml'),'utf8');
  assert.match(workflow,/workflow_dispatch:/); assert.match(workflow,/workflow_call:/);
  assert.match(workflow,/permissions:\n  contents: read/);
  assert.doesNotMatch(workflow,/pull_request_target|secrets\.|contents: write|gh release|git push/);
  const actions=[...workflow.matchAll(/uses: ([^\s]+)/g)].map(match=>match[1]);
  assert.equal(actions.length,4); for(const action of actions) assert.match(action,/^actions\/[\w-]+@[0-9a-f]{40}$/);
  assert.match(workflow,/ref: \$\{\{ inputs.source_sha \}\}/);
  assert.match(workflow,/git rev-parse HEAD\).*==.*SOURCE_SHA/);
  assert.match(workflow,/scripts\/macos\/build-distribution.sh/);
  assert.match(workflow,/if-no-files-found: error/);
});

test('caller Go workspace is disabled throughout the owned build context', t=>{
  const f=fixture(t);
  const workspace=join(f.home,'caller.go.work');
  writeFileSync(workspace, 'go 1.26.8\nuse ./repo\n');
  const result=f.run([], {GOWORK:workspace, EXPECT_GOWORK_OFF:'1'});
  assert.equal(result.status,0,result.stderr);
  const boundaries=f.trace().filter(event=>['go','npm'].includes(event.tool));
  assert.ok(boundaries.some(event=>event.tool==='go'&&event.args[0]==='build'));
  assert.ok(boundaries.some(event=>event.tool==='npm'&&event.args.includes('--target')));
  for(const event of boundaries) assert.equal(event.goWork,'off',`${event.tool} ${event.args.join(' ')}`);
});

test('directory-selected Rust toolchain remains pinned in snapshot builds and provenance', t=>{
  const f=fixture(t);
  const result=f.run([], {RUST_OVERRIDE_DIR:f.repo, RUSTUP_TOOLCHAIN:''});
  assert.equal(result.status,0,result.stderr);
  const selected='fixture-override-aarch64-apple-darwin';
  const manifest=JSON.parse(readFileSync(join(f.output,'manifest.json'),'utf8'));
  assert.equal(manifest.tools.rustToolchain,selected);
  assert.match(manifest.tools.rustc,new RegExp(selected));
  assert.match(manifest.tools.cargo,new RegExp(selected));
  const events=f.trace();
  const targetCheck=events.find(event=>event.tool==='rustup'&&event.args[0]==='target');
  assert.equal(targetCheck.rustToolchain,selected);
  const clonedCompilers=events.filter(event=>['cargo','rustc'].includes(event.tool)&&event.cwd.includes('tenebra-macos-build-'));
  assert.ok(clonedCompilers.length>=2,'Tauri invokes compilers inside the snapshot');
  for(const event of clonedCompilers) assert.equal(event.rustToolchain,selected);
});
