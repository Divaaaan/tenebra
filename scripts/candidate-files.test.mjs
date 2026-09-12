import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync, rmdirSync } from 'node:fs';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { collect, readFlatFiles } from './candidate-files.mjs';
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
