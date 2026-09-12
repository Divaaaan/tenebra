import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync, rmdirSync, symlinkSync, existsSync } from 'node:fs';
import { join, dirname, resolve } from 'node:path';
import { tmpdir } from 'node:os';
import { collect, readFlatFiles } from './candidate-files.mjs';
const release='ui-desktop/src-tauri/target/release/bundle';
const universal='ui-desktop/src-tauri/target/universal-apple-darwin/release/bundle';
const layouts={
  windows:[[`${release}/nsis/Tenebra_0.6.0_x64-setup.exe`,'Tenebra_0.6.0_x64-setup.exe']],
  linux:[[`${release}/deb/Tenebra_0.6.0_amd64.deb`,'Tenebra_0.6.0_amd64.deb'],[`${release}/appimage/Tenebra_0.6.0_amd64.AppImage`,'Tenebra_0.6.0_amd64.AppImage']],
  macos:[[`${universal}/dmg/Tenebra_0.6.0_universal.dmg`,'Tenebra_0.6.0_universal.dmg'],[`${universal}/macos/Tenebra.app.tar.gz`,'Tenebra_universal.app.tar.gz']],
};
function fixture(platform) {
  for(const [path,name] of layouts[platform]) {mkdirSync(dirname(path),{recursive:true});writeFileSync(path,`bundle ${name}`);}
  for(const id of platform==='macos'?['macos-arm64','macos-amd64']:[platform])writeFileSync(`core-buildinfo-${id}.json`,`report ${id}`);
}
const testSigner={signFile:path=>writeFileSync(path+'.sig','test signature')};
test('Linux collects finished kind outputs despite the actual deep AppDir and deb staging layout',()=>{
  const dir=mkdtempSync(join(tmpdir(),'candidate-test-')),old=process.cwd();
  try {
    process.chdir(dir);fixture('linux');
    for(const staging of ['appimage/Tenebra.AppDir','deb/Tenebra_0.6.0_amd64']) {
      const path=`${release}/${staging}/usr/lib/a/b/c/d/e/f/g/h/i/j`;
      mkdirSync(path,{recursive:true});writeFileSync(join(path,'cache'),'unrelated staging bytes');
    }
    collect('linux','0.6.0','output',testSigner);
    for(const [,name] of layouts.linux)assert.equal(readFileSync(join('output',name),'utf8'),`bundle ${name}`);
    assert.equal(readFlatFiles('output').size,5);
  } finally {process.chdir(old);rmSync(dir,{recursive:true});}
});
for(const platform of ['windows','linux','macos']) {
  test(`${platform} uses only exact final bundle-kind paths`,()=>{
    const dir=mkdtempSync(join(tmpdir(),'candidate-test-')),old=process.cwd();
    try {
      process.chdir(dir);fixture(platform);collect(platform,'0.6.0','output',testSigner);
      for(const [,name] of layouts[platform])assert.equal(readFileSync(join('output',name),'utf8'),`bundle ${name}`);
      rmSync(layouts[platform][0][0]);
      const fallback=join(dirname(dirname(layouts[platform][0][0])),'unexpected',layouts[platform][0][1]);
      mkdirSync(dirname(fallback),{recursive:true});writeFileSync(fallback,'wrong-location bytes');
      assert.throws(()=>collect(platform,'0.6.0','must-fail',testSigner));
    } finally {process.chdir(old);rmSync(dir,{recursive:true});}
  });
}
test('collection rejects a symlinked bundle-kind directory before reading or signing its target',()=>{
  const dir=mkdtempSync(join(tmpdir(),'candidate-test-')),old=process.cwd();let signed=false;
  try {
    process.chdir(dir);fixture('windows');rmSync(`${release}/nsis`,{recursive:true});
    mkdirSync('outside');writeFileSync('outside/Tenebra_0.6.0_x64-setup.exe','outside bytes');
    symlinkSync(resolve('outside'),`${release}/nsis`,process.platform==='win32'?'junction':'dir');
    assert.throws(()=>collect('windows','0.6.0','output',{signFile:()=>{signed=true;}}));assert.equal(signed,false);
  } finally {process.chdir(old);rmSync(dir,{recursive:true});}
});
test('collection rejects a symbolic-link leaf instead of signing an alternate target',()=>{
  const dir=mkdtempSync(join(tmpdir(),'candidate-test-')),old=process.cwd();let signed=false;
  try {
    process.chdir(dir);fixture('windows');rmSync(layouts.windows[0][0]);
    // Junctions need no Windows symlink privilege and exercise lstat's link guard.
    if(process.platform==='win32'){mkdirSync('outside');symlinkSync(resolve('outside'),layouts.windows[0][0],'junction');}
    else{writeFileSync('outside','outside bytes');symlinkSync(resolve('outside'),layouts.windows[0][0]);}
    assert.throws(()=>collect('windows','0.6.0','output',{signFile:()=>{signed=true;}}));assert.equal(signed,false);
  } finally {process.chdir(old);rmSync(dir,{recursive:true});}
});
test('case-alias duplicate bundle names are refused on case-sensitive filesystems',t=>{
  const dir=mkdtempSync(join(tmpdir(),'candidate-test-')),old=process.cwd();
  try {
    process.chdir(dir);writeFileSync('case-probe-A','probe');
    if(existsSync('case-probe-a')){t.skip('Filesystem cannot contain distinct case-alias entries');return;}
    fixture('linux');writeFileSync(`${release}/deb/tenebra_0.6.0_amd64.deb`,'ambiguous bytes');
    assert.throws(()=>collect('linux','0.6.0','output',testSigner));
  } finally {process.chdir(old);rmSync(dir,{recursive:true});}
});
test('Arch collection reads its one package without traversing dependency caches or rebuilt source',()=>{
  const dir=mkdtempSync(join(tmpdir(),'candidate-test-')),old=process.cwd();
  try {
    process.chdir(dir);mkdirSync('packaging/arch/src/tenebra/build',{recursive:true});
    mkdirSync('packaging/arch/src/cache/a/b/c/d/e/f/g/h/i',{recursive:true});
    writeFileSync('packaging/arch/tenebra-0.6.0-1-x86_64.pkg.tar.zst','package bytes');
    writeFileSync('packaging/arch/src/tenebra/build/core-buildinfo.json','report bytes');
    collect('arch','0.6.0','output',{signFile:path=>writeFileSync(path+'.sig','test signature')});
    assert.equal(readFileSync('output/tenebra-0.6.0-1-x86_64.pkg.tar.zst','utf8'),'package bytes');
    assert.equal(readFileSync('output/core-buildinfo-arch.json','utf8'),'report bytes');
    assert.equal(readFlatFiles('output').size,3);
    assert.throws(()=>collect('arch','0.6.0','output',{signFile:()=>{}}));
  } finally {process.chdir(old);rmSync(dir,{recursive:true});}
});
test('flat candidate collection rejects a directory or dotfile instead of uploading extra content',()=>{
  const dir=mkdtempSync(join(tmpdir(),'candidate-test-'));
  try {writeFileSync(join(dir,'safe'),'bytes');assert.equal(readFlatFiles(dir).get('safe').toString(),'bytes');mkdirSync(join(dir,'nested'));assert.throws(()=>readFlatFiles(dir));rmdirSync(join(dir,'nested'));writeFileSync(join(dir,'.secret'),'not allowed');assert.throws(()=>readFlatFiles(dir));} finally {rmSync(dir,{recursive:true});}
});
