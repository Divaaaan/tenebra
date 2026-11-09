// A single execution plan for local and CI artifact builds. Native tools stay
// process boundaries, so tests can exercise the pipeline without installing it.
import { spawnSync } from 'node:child_process';
import { readFileSync, writeFileSync, existsSync, lstatSync, mkdirSync, mkdtempSync, rmSync, renameSync, readdirSync, copyFileSync, chmodSync } from 'node:fs';
import { createHash } from 'node:crypto';
import { dirname, join, resolve, isAbsolute, relative, delimiter } from 'node:path';
import { tmpdir } from 'node:os';
import { fileURLToPath } from 'node:url';

const sourceRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const fail = message => { throw new Error(message); };
const json = file => JSON.parse(readFileSync(file, 'utf8'));
const hash = file => createHash('sha256').update(readFileSync(file)).digest('hex');
const usage = 'Usage: build-distribution.sh --output-dir /absolute/new-directory [--arch arm64|universal] [--go /absolute/go] [--node /absolute/node] [--plan-json]';
let scratch, stage;

function invoke(command, args, { cwd = sourceRoot, env = process.env, capture = false } = {}) {
  const result = spawnSync(command, args, { cwd, env, encoding: 'utf8', stdio: capture ? ['ignore', 'pipe', 'pipe'] : ['ignore', 'inherit', 'inherit'] });
  if (result.error) fail(`${command}: ${result.error.message}`);
  if (result.status !== 0) fail(`${command} ${args.join(' ')} failed (${result.status ?? result.signal})${capture ? `\n${result.stdout ?? ''}${result.stderr ?? ''}` : ''}`);
  return result.stdout?.trim();
}
function clean(root) {
  const dirty = invoke('git', ['status', '--porcelain=v1', '--untracked-files=all', '--ignore-submodules=none'], { cwd: root, capture: true });
  if (dirty) fail(`source must be clean, including untracked files:\n${dirty}`);
}
function executable(name) {
  const found = isAbsolute(name) ? name : (process.env.PATH ?? '').split(delimiter).map(dir => join(dir, name)).find(file => existsSync(file));
  if (!found || !isAbsolute(found) || !existsSync(found)) fail(`missing executable ${name}`);
  return found;
}
function parse(args) {
  const options = { arch: 'arm64', go: undefined, node: process.execPath, planOnly: false };
  while (args.length) {
    const arg = args.shift();
    if (arg === '--plan-json') options.planOnly = true;
    else if (['--output-dir', '--arch', '--go', '--node'].includes(arg)) {
      const value = args.shift();
      if (!value || value.startsWith('--')) fail(`${arg} requires a value`);
      options[{ '--output-dir': 'output', '--arch': 'arch', '--go': 'go', '--node': 'node' }[arg]] = value;
    } else fail(`unknown argument: ${arg}\n${usage}`);
  }
  if (!['arm64', 'universal'].includes(options.arch)) fail('--arch must be arm64 or universal');
  if (!options.output || !isAbsolute(options.output)) fail('--output-dir must be absolute');
  options.output = resolve(options.output);
  if (existsSync(options.output) || (() => { try { lstatSync(options.output); return true; } catch { return false; } })()) fail('output directory already exists; select a new path');
  if (!existsSync(dirname(options.output)) || !lstatSync(dirname(options.output)).isDirectory()) fail('output parent directory must exist');
  options.go ??= executable('go');
  if (!isAbsolute(options.go) || !isAbsolute(options.node)) fail('--go and --node must name absolute executables');
  executable(options.go); executable(options.node);
  return options;
}
function plan(options, env) {
  if (!/^v24\./.test(process.version)) fail(`Node 24 is required; running ${process.version}`);
  clean(sourceRoot);
  const commit = invoke('git', ['rev-parse', 'HEAD'], { capture: true, env });
  const tree = invoke('git', ['rev-parse', `${commit}^{tree}`], { capture: true, env });
  invoke(options.node, ['scripts/set-version.mjs', '--check'], { capture: true, env });
  const conf = JSON.parse(invoke('git', ['show', `${commit}:ui-desktop/src-tauri/tauri.conf.json`], { capture: true, env }));
  if (!/^\d+\.\d+\.\d+$/.test(conf.version)) fail('PKG builds require a coherent stable source version');
  if (conf.productName !== 'Tenebra') fail('unexpected product name');
  const goVersion = invoke('git', ['show', `${commit}:.go-version`], { capture: true, env });
  if (!/^\d+\.\d+\.\d+$/.test(goVersion)) fail('invalid .go-version pin');
  const actualGo = invoke(options.go, ['version'], { env, capture: true });
  if (!new RegExp(`^go version go${goVersion.replaceAll('.', '\\.')} darwin/(arm64|amd64)$`).test(actualGo)) fail(`Go ${goVersion} for macOS is required; got ${actualGo}`);
  clean(sourceRoot);
  if (invoke('git', ['rev-parse', 'HEAD'], { capture: true, env }) !== commit) fail('source HEAD changed during preflight');
  const slices = [{ goArch: 'arm64', machoArch: 'arm64', target: 'aarch64-apple-darwin' }];
  if (options.arch === 'universal') slices.push({ goArch: 'amd64', machoArch: 'x86_64', target: 'x86_64-apple-darwin' });
  return { schemaVersion: 1, source: { commit, tree, clean: true }, version: conf.version, arch: options.arch,
    goVersion, slices, target: options.arch === 'universal' ? 'universal-apple-darwin' : slices[0].target,
    package: `Tenebra-${conf.version}-macos-${options.arch}.pkg`, output: options.output };
}
function requireSlices(file, slices, cwd, env) {
  const actual = invoke('lipo', ['-archs', file], { cwd, env, capture: true }).split(/\s+/);
  for (const slice of slices) if (!actual.includes(slice.machoArch)) fail(`${file} is missing ${slice.machoArch}`);
  return actual;
}
function inventory(directory) {
  const files = [];
  function walk(path) {
    for (const item of readdirSync(path, { withFileTypes: true }).sort((a,b) => a.name.localeCompare(b.name))) {
      const file = join(path, item.name);
      if (item.isDirectory()) walk(file);
      else if (item.isFile()) files.push({ name: relative(directory, file), sha256: hash(file), bytes: lstatSync(file).size });
      else fail(`unexpected symlink or special file in artifact: ${file}`);
    }
  }
  walk(directory);
  return files;
}
function build(options, selected, env) {
  if (invoke('uname', ['-s'], { capture: true, env }) !== 'Darwin') fail('macOS is required');
  // Preserve the caller/source checkout's rustup selection in the new clone.
  const rustToolchain = invoke('rustup', ['show', 'active-toolchain'], { capture: true, env }).split(/\s+/)[0];
  if (!rustToolchain) fail('cannot resolve active Rust toolchain');
  env = { ...env, RUSTUP_TOOLCHAIN: rustToolchain };
  const installed = invoke('rustup', ['target', 'list', '--installed'], { capture: true, env }).split(/\s+/);
  for (const slice of selected.slices) if (!installed.includes(slice.target)) fail(`missing Rust target ${slice.target}; prepare it before building`);
  const tools = {
    rustToolchain,
    node: process.version, npm: invoke('npm', ['--version'], { capture: true, env }),
    go: invoke(options.go, ['version'], { capture: true, env }),
    rustc: invoke('rustc', ['-vV'], { capture: true, env }), cargo: invoke('cargo', ['--version'], { capture: true, env }),
    appleTools: invoke('xcrun', ['--version'], { capture: true, env }),
    clang: invoke('xcrun', ['clang', '--version'], { capture: true, env }),
    sdk: invoke('xcrun', ['--sdk', 'macosx', '--show-sdk-version'], { capture: true, env }),
    macos: invoke('sw_vers', ['-productVersion'], { capture: true, env }),
  };
  scratch = mkdtempSync(join(tmpdir(), 'tenebra-macos-build-'));
  const snapshot = join(scratch, 'source');
  // A linked worktree can lose Go VCS stamping with the pinned Go toolchain.
  // A normal Git clone retains .git/ and never copies ignored local payloads.
  invoke('git', ['clone', '--no-local', '--no-hardlinks', '--no-checkout', sourceRoot, snapshot], { env });
  invoke('git', ['checkout', '--detach', selected.source.commit], { cwd: snapshot, env });
  clean(snapshot);
  if (invoke('git', ['rev-parse', 'HEAD'], { cwd: snapshot, env, capture: true }) !== selected.source.commit) fail('snapshot commit differs from selected source');
  invoke(options.node, ['scripts/set-version.mjs', '--check'], { cwd: snapshot, env });
  stage = mkdtempSync(join(dirname(options.output), '.tenebra-output-'));
  const run = (cmd, args, extra = {}) => invoke(cmd, args, { cwd: snapshot, env, ...extra });
  const sign = file => { run('codesign', ['--force', '--sign', '-', '--timestamp=none', file]); run('codesign', ['--verify', '--strict', file]); };
  run('/bin/bash', ['scripts/fetch-resources.sh']);
  const resources = join(snapshot, 'ui-desktop/src-tauri/resources');
  requireSlices(join(resources, 'sing-box'), selected.slices, snapshot, env);
  sign(join(resources, 'sing-box'));
  const resourceFiles = inventory(resources);
  const bins = join(snapshot, 'ui-desktop/src-tauri/binaries'); mkdirSync(bins, { recursive: true });
  for (const slice of selected.slices) {
    const binary = join(bins, `tenebra-core-${slice.target}`);
    run(options.go, ['build', '-trimpath', '-buildvcs=true', '-o', binary, './cmd/tenebra-core'], { env: { ...env, GOOS: 'darwin', GOARCH: slice.goArch, CGO_ENABLED: '0' } });
    sign(binary);
    run(options.node, ['scripts/verify-core-build.mjs', binary, 'darwin', slice.goArch, join(stage, `core-build-${slice.goArch}.json`)]);
  }
  if (selected.arch === 'universal') {
    const combined = join(bins, `tenebra-core-${selected.target}`);
    run('lipo', ['-create', ...selected.slices.map(slice => join(bins, `tenebra-core-${slice.target}`)), '-output', combined]); sign(combined);
  }
  const desktop = join(snapshot, 'ui-desktop');
  const ciConfig = json(join(desktop, 'src-tauri/tauri.ci.conf.json'));
  if (ciConfig.bundle?.createUpdaterArtifacts !== false) fail('CI config must disable updater artifacts');
  run('npm', ['ci', '--no-audit', '--no-fund'], { cwd: desktop });
  run('npm', ['run', 'tauri', '--', 'build', '--target', selected.target, '--bundles', 'app', '--config', 'src-tauri/tauri.ci.conf.json', '--', '--locked'], { cwd: desktop });
  const app = join(desktop, `src-tauri/target/${selected.target}/release/bundle/macos/Tenebra.app`);
  const core = join(app, 'Contents/MacOS/tenebra-core');
  // Explicitly sign nested executables before sealing the outer bundle.
  sign(core); sign(join(app, 'Contents/Resources/resources/sing-box')); sign(app);
  run('codesign', ['--verify', '--deep', '--strict', app]);
  const archs = requireSlices(core, selected.slices, snapshot, env);
  for (const slice of selected.slices) {
    const thin = join(scratch, `packaged-core-${slice.goArch}`);
    if (archs.length > 1) run('lipo', [core, '-thin', slice.machoArch, '-output', thin]); else copyFileSync(core, thin);
    run(options.node, ['scripts/verify-core-build.mjs', thin, 'darwin', slice.goArch, join(stage, `core-packaged-${slice.goArch}.json`)]);
  }
  run('/bin/bash', ['scripts/macos/build-pkg.sh', '--app', app, '--output', join(stage, selected.package), '--go', options.go, '--arch', selected.arch]);
  const manifest = { ...selected, output: undefined, builtAt: new Date().toISOString(), tools,
    signing: { executables: 'ad-hoc', installer: 'unsigned', notarized: false }, nativeAcceptance: 'not_run',
    resourceFetcher: { name: 'scripts/fetch-resources.sh', sha256: hash(join(snapshot, 'scripts/fetch-resources.sh')) },
    resources: resourceFiles, app: { name: 'Tenebra.app', files: inventory(app) }, artifacts: inventory(stage) };
  // The lockfiles are source inputs; record their byte hashes without copying them.
  manifest.sourceInputs = ['.go-version', 'go.mod', 'go.sum', 'ui-desktop/package-lock.json', 'ui-desktop/src-tauri/Cargo.lock']
    .filter(name => existsSync(join(snapshot, name))).map(name => ({ name, sha256: hash(join(snapshot, name)) }));
  writeFileSync(join(stage, 'manifest.json'), JSON.stringify(manifest, null, 2) + '\n');
  writeFileSync(join(stage, 'SHA256SUMS'), inventory(stage).map(file => `${file.sha256}  ${file.name}\n`).join(''));
  for (const file of readdirSync(stage)) chmodSync(join(stage, file), 0o644);
  // Same-filesystem rename publishes the complete directory, never a partial PKG.
  if (existsSync(options.output)) fail('output appeared during build; refusing to overwrite');
  renameSync(stage, options.output); stage = undefined;
  console.log(`Built ${join(options.output, selected.package)} from ${selected.source.commit}; native acceptance not run.`);
}

try {
  if (process.argv.slice(2).some(arg => arg === '--help' || arg === '-h')) { console.log(usage); process.exit(0); }
  const options = parse(process.argv.slice(2));
  const env = { ...process.env, PATH: [dirname(options.node), dirname(options.go), process.env.PATH].join(delimiter),
    GOTOOLCHAIN: 'local', GOENV: 'off', GOWORK: 'off', GOFLAGS: '-p=4', GOMAXPROCS: '4',
    CARGO_BUILD_JOBS: '4', CARGO_INCREMENTAL: '0', MACOSX_DEPLOYMENT_TARGET: '15.0' };
  for (const key of ['GOOS', 'GOARCH', 'CGO_ENABLED', 'RUSTFLAGS', 'CARGO_ENCODED_RUSTFLAGS', 'RUSTC_WRAPPER', 'RUSTC_WORKSPACE_WRAPPER', 'CARGO_TARGET_DIR', 'CARGO_BUILD_TARGET', 'TAURI_SIGNING_PRIVATE_KEY', 'TAURI_SIGNING_PRIVATE_KEY_PASSWORD', 'APPLE_SIGNING_IDENTITY', 'APPLE_CERTIFICATE', 'APPLE_CERTIFICATE_PASSWORD', 'APPLE_ID', 'APPLE_PASSWORD', 'APPLE_API_KEY', 'APPLE_API_ISSUER']) delete env[key];
  const selected = plan(options, env);
  if (options.planOnly) console.log(JSON.stringify(selected, null, 2)); else build(options, selected, env);
} catch (error) {
  console.error(`build-distribution: ${error.message}`); process.exitCode = 1;
} finally {
  if (stage) rmSync(stage, { recursive: true, force: true });
  if (scratch) rmSync(scratch, { recursive: true, force: true });
}
