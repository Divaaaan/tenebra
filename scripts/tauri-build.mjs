import { execFile, spawn } from 'node:child_process';
import { mkdir, mkdtemp, readFile, writeFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { dirname, join, relative, resolve, sep } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { promisify } from 'node:util';
import { prepareServicePayload } from './prepare-service-payload.mjs';
import { verifyBuildInfo } from './verify-core-build.mjs';

const run = promisify(execFile);
const repository = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const desktop = join(repository, 'ui-desktop');
const tauriDirectory = join(desktop, 'src-tauri');
const sourcePaths = ['go.mod', 'go.sum', 'adapters', 'cmd', 'core', 'core-bridge', 'internal'];

export async function linuxPackageConfig(prefix) {
  if (typeof prefix !== 'string' || !/^resources\/service-payload\/[A-Za-z0-9/_-]+$/.test(prefix)) {
    throw new Error('service payload must use a build-owned resource directory');
  }
  const config = JSON.parse(await readFile(join(tauriDirectory, 'tauri.linux.conf.json'), 'utf8'));
  config.bundle.linux.deb.files['/usr/lib/tenebra/tenebra-helper'] = prefix + '/tenebra-helper';
  config.bundle.linux.deb.files['/usr/lib/tenebra/package-payload'] = prefix + '/payload';
  return config;
}

export function withBuildConfig(args, config) {
  const separator = args.indexOf('--');
  const beforeCargo = separator < 0 ? args.length : separator;
  return [...args.slice(0, beforeCargo), '--config', JSON.stringify(config), ...args.slice(beforeCargo)];
}

async function unchangedGoSource() {
  const { stdout } = await run('git', ['status', '--porcelain=v1', '--untracked-files=all', '--', ...sourcePaths], {
    cwd: repository, timeout: 30_000,
  });
  if (stdout.trim()) throw new Error('service payload requires the committed Go source used to build the core');
}

async function prepareLinuxBuild(args) {
  const separator = args.indexOf('--');
  const options = separator < 0 ? args : args.slice(0, separator);
  const targetOption = options.findIndex(value => value === '--target' || value.startsWith('--target='));
  const target = targetOption < 0 ? 'x86_64-unknown-linux-gnu' :
    options[targetOption].split('=')[1] || options[targetOption + 1];
  if (process.arch !== 'x64' || target !== 'x86_64-unknown-linux-gnu') {
    throw new Error('the protected Linux service payload currently supports x86_64-unknown-linux-gnu');
  }
  await unchangedGoSource();
  const goVersion = (await readFile(join(repository, '.go-version'), 'utf8')).trim();
  const env = { ...process.env, GOTOOLCHAIN: 'go' + goVersion, GOMAXPROCS: '2' };
  const { stdout: head } = await run('git', ['rev-parse', 'HEAD'], { cwd: repository, timeout: 30_000 });
  const sourceSha = head.trim();
  const core = join(tauriDirectory, 'binaries', 'tenebra-core-' + target);
  const { stdout: metadata } = await run('go', ['version', '-m', core], { cwd: repository, env, timeout: 90_000 });
  verifyBuildInfo(metadata, { goVersion, revision: sourceSha, os: 'linux', arch: 'amd64' });
  const { version } = JSON.parse(await readFile(join(tauriDirectory, 'tauri.conf.json'), 'utf8'));
  const parent = join(tauriDirectory, 'resources', 'service-payload');
  await mkdir(parent, { recursive: true });
  const build = await mkdtemp(join(parent, 'build-'));
  const output = join(build, 'package');
  const binding = await prepareServicePayload({ input: join(tauriDirectory, 'resources'), core, output,
    target: 'linux-amd64', version, sourceSha });
  await unchangedGoSource();
  const { stdout: after } = await run('git', ['rev-parse', 'HEAD'], { cwd: repository, timeout: 30_000 });
  if (after.trim() !== sourceSha) throw new Error('source revision changed while preparing the service payload');
  const prefix = relative(tauriDirectory, output).split(sep).join('/');
  const config = await linuxPackageConfig(prefix);
  await writeFile(join(build, 'tauri.config.json'), JSON.stringify(config, null, 2) + '\n', { flag: 'wx' });
  await writeFile(join(build, 'source.json'), JSON.stringify({ source_sha: sourceSha, core, binding }, null, 2) + '\n', { flag: 'wx' });
  return withBuildConfig(args, config);
}

async function main(args) {
  if (process.platform === 'linux' && args[0] === 'build' && !args.some(value => value === '--help' || value === '-h')) {
    args = await prepareLinuxBuild(args);
  }
  const require = createRequire(join(desktop, 'package.json'));
  const cli = join(dirname(require.resolve('@tauri-apps/cli/package.json')), 'tauri.js');
  const child = spawn(process.execPath, [cli, ...args], { cwd: desktop, stdio: 'inherit' });
  process.exitCode = await new Promise((resolveExit, reject) => {
    child.once('error', reject);
    child.once('exit', code => resolveExit(code ?? 1));
  });
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main(process.argv.slice(2)).catch(error => {
    console.error(error.message);
    process.exitCode = 1;
  });
}
