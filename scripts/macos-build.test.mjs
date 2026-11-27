import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { chmodSync, copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { spawnSync } from 'node:child_process'
import test from 'node:test'

const source = readFileSync(new URL('./setup-fixed-macos.sh', import.meta.url), 'utf8')

function fixture(t) {
  const root = mkdtempSync(join(tmpdir(), 'omni-macos-fixture-'))
  t.after(() => rmSync(root, { recursive: true, force: true }))
  for (const path of ['scripts', 'bin', 'build/locks', 'Developer/usr/bin', 'source/tcl9.1.0/unix', 'temp']) mkdirSync(join(root, path), { recursive: true })
  const executable = (name, body) => {
    const path = join(root, 'bin', name)
    writeFileSync(path, `#!${process.execPath}\n${body}\n`)
    chmodSync(path, 0o755)
  }
  writeFileSync(join(root, 'scripts/setup-fixed-macos.sh'), source)
  const configure = join(root, 'source/tcl9.1.0/unix/configure')
  writeFileSync(configure, '#!/bin/sh\nprintf configured > "$FIXTURE_ROOT/configured"\n')
  chmodSync(configure, 0o755)
  const archive = join(root, 'source.tar.gz')
  assert.equal(spawnSync('tar', ['-czf', archive, '-C', join(root, 'source'), 'tcl9.1.0']).status, 0)
  const digest = createHash('sha256').update(readFileSync(archive)).digest('hex')
  writeFileSync(join(root, 'build/locks/macos.json'), JSON.stringify({ schema: 2,
    xcode: {
      amd64: { developerDirectory: join(root, 'Developer'), version: '16.4', build: '16F6', macosSDK: '15.5' },
      arm64: { developerDirectory: join(root, 'Developer'), version: '27.2', build: '27B52', macosSDK: '27.2' },
    },
    tcl: { version: '9.1.0', url: 'https://fixture.invalid/tcl.tar.gz', sha256: digest, archiveRoot: 'tcl9.1.0', shell: 'tclsh9.1' } }))
  executable('uname', `console.log(process.argv[2] === '-m' ? (process.env.FIXTURE_ARCH || 'x86_64') : 'Darwin')`)
  executable('xcodebuild', `console.log(process.env.FIXTURE_XCODE || 'Xcode 16.4\\nBuild version 16F6')`)
  copyFileSync(join(root, 'bin/xcodebuild'), join(root, 'Developer/usr/bin/xcodebuild'))
  executable('clang', '')
  executable('codesign', `process.exit(process.argv.includes('-R=anchor apple') && !process.env.FIXTURE_INVALID_SIGNATURE ? 0 : 1)`)
  executable('xcrun', `console.log(process.argv.includes('--show-sdk-version') ? (process.env.FIXTURE_SDK || '15.5') :
    process.argv.includes('--find') ? process.env.FIXTURE_ROOT + '/bin/clang' : '/fixture/sdk')`)
  executable('curl', `const fs=require('node:fs'); fs.copyFileSync(process.env.FIXTURE_ROOT+'/source.tar.gz',process.argv[process.argv.indexOf('-o')+1]);`)
  executable('make', `if(process.argv.includes('install-binaries')) { const fs=require('node:fs'); const bin=process.env.RUNNER_TEMP+'/omni-tcl-9.1.0/bin'; fs.mkdirSync(bin,{recursive:true}); const p=bin+'/tclsh9.1'; fs.writeFileSync(p,'#!'+process.execPath+'\\nconsole.log("9.1.0")\\n'); fs.chmodSync(p,0o755); }`)
  executable('otool', `console.log('/usr/lib/libSystem.B.dylib')`)
  const prefix = join(root, 'temp/omni-tcl-9.1.0')
  const run = overrides => spawnSync('bash', [join(root, 'scripts/setup-fixed-macos.sh')], {
    env: { ...process.env, PATH: `${join(root, 'bin')}:${process.env.PATH}`, GITHUB_ACTIONS: 'true',
      FIXTURE_ROOT: root, RUNNER_TEMP: join(root, 'temp'), GITHUB_ENV: join(root, 'env'), GITHUB_PATH: join(root, 'path'), ...overrides },
    encoding: 'utf8', timeout: 10000,
  })
  return { root, prefix, archive, run }
}

test('macOS setup verifies Xcode/SDK and checksum before installing the explicit Tcl shell', t => {
  const f = fixture(t), r = f.run()
  assert.equal(r.status, 0, r.stderr + r.stdout)
  assert.match(readFileSync(join(f.root, 'env'), 'utf8'), /DEVELOPER_DIR=.*Developer\n/)
  assert.match(readFileSync(join(f.root, 'env'), 'utf8'), /TCLSH_CMD=.*omni-tcl-9\.1\.0\/bin\/tclsh\n/)
  assert.ok(existsSync(join(f.prefix, 'bin/tclsh')))
})

test('Apple Silicon requires its own native Xcode and SDK', t => {
  const f = fixture(t)
  assert.notEqual(f.run({ FIXTURE_ARCH: 'arm64' }).status, 0)
  const result = f.run({ FIXTURE_ARCH: 'arm64', FIXTURE_XCODE: 'Xcode 27.2\nBuild version 27B52', FIXTURE_SDK: '27.2' })
  assert.equal(result.status, 0, result.stderr + result.stdout)
})

test('unavailable Xcode build or SDK never falls back or builds Tcl', t => {
  const f = fixture(t)
  for (const env of [{ FIXTURE_XCODE: 'Xcode 16.4\nBuild version wrong' }, { FIXTURE_SDK: '26.2' }, { FIXTURE_INVALID_SIGNATURE: '1' }]) {
    assert.notEqual(f.run(env).status, 0)
    assert.equal(existsSync(join(f.root, 'configured')), false)
    assert.equal(existsSync(f.prefix), false)
  }
})

test('a changed Tcl archive is rejected before configure without leaving a prefix', t => {
  const f = fixture(t)
  writeFileSync(f.archive, 'changed archive')
  assert.notEqual(f.run().status, 0)
  assert.equal(existsSync(join(f.root, 'configured')), false)
  assert.equal(existsSync(f.prefix), false)
})
