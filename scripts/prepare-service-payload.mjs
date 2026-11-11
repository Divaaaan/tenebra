import { execFile } from 'node:child_process';
import { createHash } from 'node:crypto';
import { constants as fsConstants, createReadStream, createWriteStream } from 'node:fs';
import { chmod, lstat, mkdir, open, readFile, writeFile } from 'node:fs/promises';
import { dirname, join, resolve } from 'node:path';
import { Readable } from 'node:stream';
import { pipeline } from 'node:stream/promises';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { promisify } from 'node:util';
import { constants as zipConstants, createGzip } from 'node:zlib';

const run = promisify(execFile);
const repository = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const mib = 1 << 20;
const payloadLimit = 2048 * mib;
const archiveLimit = 1024 * mib;
const roles = new Map([
  ['geoip-ru.srs', 0o644], ['geosite-ads.srs', 0o644], ['geosite-ru.srs', 0o644],
  ['sing-box', 0o755], ['tenebra-core', 0o755],
]);
const encode = value => Buffer.from(JSON.stringify(value, null, 2) + '\n');

function checkedFile(info, maximum) {
  if (!info.isFile() || info.nlink !== 1n || info.size < 1n || info.size > BigInt(maximum)) {
    throw new Error('expected a bounded regular file with one link');
  }
  return Number(info.size);
}

function sameFile(a, b) {
  return a.dev === b.dev && a.ino === b.ino && a.nlink === b.nlink &&
    a.size === b.size && a.mtimeNs === b.mtimeNs && a.ctimeNs === b.ctimeNs;
}

async function copyRole(source, target, mode, maximum) {
  const named = await lstat(source, { bigint: true });
  const size = checkedFile(named, maximum);
  const input = await open(source, fsConstants.O_RDONLY | (fsConstants.O_NOFOLLOW || 0));
  try {
    const opened = await input.stat({ bigint: true });
    if (!sameFile(named, opened)) throw new Error('source changed before copy');
    await pipeline(input.createReadStream({ autoClose: false, end: size - 1 }),
      createWriteStream(target, { flags: 'wx', mode }));
    if (!sameFile(opened, await input.stat({ bigint: true })) ||
        !sameFile(opened, await lstat(source, { bigint: true })) ||
        checkedFile(await lstat(target, { bigint: true }), maximum) !== size) {
      throw new Error('source changed during copy');
    }
  } finally {
    await input.close();
  }
}

async function hashedFile(path, maximum) {
  const size = checkedFile(await lstat(path, { bigint: true }), maximum);
  const hash = createHash('sha256');
  let seen = 0;
  for await (const bytes of createReadStream(path, { end: size - 1 })) {
    hash.update(bytes);
    seen += bytes.length;
  }
  if (seen !== size) throw new Error('staged file size changed');
  return { size, sha256: hash.digest('hex') };
}

function tarHeader({ name, size, mode }) {
  const header = Buffer.alloc(512);
  const octal = (value, offset, width) => header.write(value.toString(8).padStart(width - 1, '0'), offset, width - 1, 'ascii');
  header.write(name, 0, 100, 'ascii');
  octal(mode, 100, 8);
  octal(0, 108, 8);
  octal(0, 116, 8);
  octal(size, 124, 12);
  octal(0, 136, 12);
  header.fill(0x20, 148, 156);
  header[156] = 0x30;
  header.write('ustar\0' + '00', 257, 8, 'ascii');
  const sum = header.reduce((total, byte) => total + byte, 0);
  header.write(sum.toString(8).padStart(6, '0') + '\0 ', 148, 8, 'ascii');
  return header;
}

async function* tarBody(directory, files) {
  for (const file of files) {
    yield tarHeader(file);
    let seen = 0;
    for await (const bytes of createReadStream(join(directory, file.name), { end: file.size - 1 })) {
      seen += bytes.length;
      yield bytes;
    }
    if (seen !== file.size) throw new Error('staged file changed while building archive');
    const padding = (512 - seen % 512) % 512;
    if (padding) yield Buffer.alloc(padding);
  }
  yield Buffer.alloc(1024);
}

async function gzipTo(input, output) {
  await pipeline(input, createGzip({ level: zipConstants.Z_BEST_COMPRESSION }),
    createWriteStream(output, { flags: 'wx', mode: 0o644 }));
}

async function checkedELF(path) {
  const file = await open(path, 'r');
  try {
    const header = Buffer.alloc(20);
    const { bytesRead } = await file.read(header, 0, header.length, 0);
    if (bytesRead !== header.length || !header.subarray(0, 6).equals(Buffer.from([0x7f, 0x45, 0x4c, 0x46, 2, 1])) ||
        ![2, 3].includes(header.readUInt16LE(16)) || header.readUInt16LE(18) !== 62) {
      throw new Error('expected a Linux x86-64 executable');
    }
  } finally {
    await file.close();
  }
}

const nativePorts = {
  async seal(path, _role, target) {
    if (target === 'linux-amd64') return checkedELF(path);
    if (process.platform !== 'darwin') throw new Error('universal Apple payload sealing requires macOS');
    const { stdout } = await run('/usr/bin/lipo', ['-archs', path], { timeout: 30_000 });
    if (stdout.trim().split(/\s+/).sort().join(' ') !== 'arm64 x86_64') {
      throw new Error('expected exactly arm64 and x86_64 in the Apple executable');
    }
    await run('/usr/bin/codesign', ['--force', '--sign', '-', '--timestamp=none', path], { timeout: 60_000 });
    await run('/usr/bin/codesign', ['--verify', '--strict', path], { timeout: 60_000 });
  },
  async buildHelper(path, { target, payloadSha, archiveSha }) {
    const go = (await readFile(join(repository, '.go-version'), 'utf8')).trim();
    if (!/^1\.\d+\.\d+$/.test(go)) throw new Error('invalid pinned Go version');
    const mac = target === 'darwin-universal';
    if (mac && process.platform !== 'darwin') throw new Error('universal Apple helper build requires macOS');
    const ldflags = ['-X', `github.com/Divaaaan/tenebra/internal/installtx.nativeBoundPayloadSHA256=${payloadSha}`,
      '-X', `github.com/Divaaaan/tenebra/internal/installtx.nativeBoundArchiveSHA256=${archiveSha}`].join(' ');
    const env = { ...process.env, GOTOOLCHAIN: `go${go}`, GOOS: mac ? 'darwin' : 'linux', CGO_ENABLED: '0', GOFLAGS: '' };
    const { stdout } = await run('go', ['version'], { cwd: repository, env, timeout: 90_000 });
    if (stdout.trim().split(/\s+/)[2] !== `go${go}`) throw new Error('Go toolchain version mismatch');
    const build = (arch, output) => run('go', ['build', '-mod=readonly', '-p', '2', '-trimpath', '-buildvcs=false',
      '-ldflags', ldflags, '-o', output, './cmd/tenebra-helper'], {
      cwd: repository, env: { ...env, GOARCH: arch, GOMAXPROCS: '2' }, timeout: 360_000,
    });
    if (!mac) {
      await build('amd64', path);
      return;
    }
    const slices = [path + '.amd64', path + '.arm64'];
    await build('amd64', slices[0]);
    await build('arm64', slices[1]);
    await run('/usr/bin/lipo', ['-create', ...slices, '-output', path], { timeout: 30_000 });
  },
};

// Build inputs are a fixed list of roles. The helper is compiled only after
// hashing the sealed payload, and its own hashes are taken after its final seal.
export async function prepareServicePayload({ input, core, output, target, version, sourceSha }, ports = nativePorts) {
  if (!['linux-amd64', 'darwin-universal'].includes(target) ||
      typeof version !== 'string' || !/^[A-Za-z0-9._+-]{1,128}$/.test(version) ||
      typeof sourceSha !== 'string' || !/^[a-f0-9]{40}$/.test(sourceSha) ||
      typeof input !== 'string' || !input || typeof output !== 'string' || !output ||
      core !== undefined && (typeof core !== 'string' || !core)) {
    throw new Error('invalid service payload build input');
  }
  input = resolve(input);
  output = resolve(output);
  if (ports === nativePorts && target === 'darwin-universal' && process.platform !== 'darwin') {
    throw new Error('universal Apple payload requires a macOS build host');
  }
  const config = JSON.parse(await readFile(join(repository, 'ui-desktop/src-tauri/tauri.conf.json'), 'utf8'));
  const key = config.plugins.updater.pubkey;
  const helperSource = await readFile(join(repository, 'internal/installtx/native_helper_unix.go'), 'utf8');
  const helperKey = /^const nativeReleasePublicKey = "([A-Za-z0-9+/=]+)"$/m.exec(helperSource)?.[1];
  if (typeof key !== 'string' || !key || key !== helperKey) throw new Error('updater and helper public keys differ');
  await mkdir(output, { mode: 0o700 });
  const payload = join(output, 'payload');
  await mkdir(payload, { mode: 0o700 });
  const files = [];
  let total = 0;
  for (const [name, mode] of roles) {
    const path = join(payload, name);
    const source = name === 'tenebra-core' && core !== undefined ? resolve(core) : join(input, name);
    await copyRole(source, path, mode, payloadLimit - total);
    if (mode === 0o755) await ports.seal(path, name, target);
    await chmod(path, mode);
    const { size, sha256 } = await hashedFile(path, payloadLimit - total);
    total += size;
    files.push({ name, sha256, size, mode });
  }
  const body = encode({ schema: 1, kind: 'tenebra-install-payload', version, source_sha: sourceSha, target, files });
  const manifest = join(payload, 'payload.json');
  await writeFile(manifest, body, { flag: 'wx', mode: 0o644 });
  const payloadSha = createHash('sha256').update(body).digest('hex');
  const archive = join(output, 'payload.tar.gz');
  const members = [...files, { name: 'payload.json', size: body.length, mode: 0o644 }]
    .sort((a, b) => a.name < b.name ? -1 : a.name > b.name ? 1 : 0);
  const expanded = members.reduce((total, file) => total + 512 + Math.ceil(file.size / 512) * 512, 1024);
  if (expanded > payloadLimit) throw new Error('payload archive exceeds its expansion limit');
  await gzipTo(Readable.from(tarBody(payload, members)), archive);
  const { size: archiveSize, sha256: archiveSha } = await hashedFile(archive, archiveLimit);
  if (expanded + archiveSize > payloadLimit) throw new Error('payload exceeds the nested expansion limit');

  const helper = join(output, 'tenebra-helper');
  await ports.buildHelper(helper, { target, payloadSha, archiveSha });
  await ports.seal(helper, 'tenebra-helper', target);
  await chmod(helper, 0o755);
  const { sha256: helperSha } = await hashedFile(helper, 64 * mib);
  const blob = join(output, 'helper.gz');
  await gzipTo(createReadStream(helper), blob);
  const { sha256: helperBlobSha } = await hashedFile(blob, 32 * mib);
  const binding = { helper_sha256: helperSha, helper_blob_sha256: helperBlobSha,
    payload_sha256: payloadSha, payload_archive_sha256: archiveSha, project_pubkey: key, target, version };
  await writeFile(join(output, 'binding.json'), encode(binding), { flag: 'wx', mode: 0o644 });
  return binding;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const [input, output, target, version, sourceSha, ...extra] = process.argv.slice(2);
  try {
    if (extra.length) throw new Error('usage: prepare-service-payload.mjs <input> <output> <target> <version> <source-sha>');
    const binding = await prepareServicePayload({ input, output, target, version, sourceSha });
    console.log(`prepared ${binding.target} ${binding.version} ${binding.payload_sha256}`);
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
