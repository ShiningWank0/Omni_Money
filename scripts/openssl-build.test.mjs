import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { spawnSync } from 'node:child_process'
import test from 'node:test'

const source = readFileSync(new URL('./build-openssl-windows.sh', import.meta.url), 'utf8')
const version = source.match(/OPENSSL_VERSION="([^"]+)"/)[1]

function fixture(t) {
  const directory = mkdtempSync(join(tmpdir(), 'omni-openssl-fixture-'))
  t.after(() => rmSync(directory, { recursive: true, force: true }))
  const bin = join(directory, 'bin'), prefix = join(directory, 'installed')
  mkdirSync(bin)
  const executable = (name, body) => {
    const path = join(bin, name)
    writeFileSync(path, `#!${process.execPath}\n${body}\n`)
    chmodSync(path, 0o755)
  }
  const root = join(directory, 'source', `openssl-${version}`)
  mkdirSync(join(root, 'include/openssl'), { recursive: true })
  writeFileSync(join(root, 'Configure'), '# fixture configure input\n')
  writeFileSync(join(root, 'include/openssl/crypto.h'), '# fixture public header\n')
  writeFileSync(join(root, 'LICENSE.txt'), 'fixture license\n')
  const archive = join(directory, 'source.tar.gz')
  const packed = spawnSync('tar', ['-czf', archive, '-C', join(directory, 'source'), `openssl-${version}`])
  assert.equal(packed.status, 0, packed.stderr?.toString())
  const digest = createHash('sha256').update(readFileSync(archive)).digest('hex')
  const script = join(directory, 'build.sh')
  // Substitute only the fixture archive's digest in a temporary script copy.
  // Every production integrity check and build operation remains in place.
  writeFileSync(script, source.replace(/OPENSSL_SHA256="[a-f0-9]{64}"/, `OPENSSL_SHA256="${digest}"`))
  executable('uname', `console.log('x86_64')`)
  executable('cygpath', `console.log(process.argv[3])`)
  executable('curl', `const fs = require('node:fs'); fs.copyFileSync(process.env.FIXTURE_ARCHIVE, process.argv[process.argv.indexOf('--output') + 1]);`)
  executable('sha256sum', `const fs = require('node:fs'), crypto = require('node:crypto'); const match = /^([a-f0-9]{64})  (.+)\\n?$/.exec(fs.readFileSync(0, 'utf8').trimEnd()); if (!match || crypto.createHash('sha256').update(fs.readFileSync(match[2])).digest('hex') !== match[1]) { console.error('checksum mismatch'); process.exit(1); }`)
  executable('perl', `const fs = require('node:fs'); fs.writeFileSync(process.env.FIXTURE_CONFIGURED, 'configured'); fs.writeFileSync('libcrypto.a', 'fixture archive');`)
  executable('make', '')
  executable('ar', `console.log('provider.o')`)
  executable('ranlib', '')
  executable('nm', `console.log('00000000 T EVP_aes_256_cbc')`)
  executable('objdump', `console.log('file format pe-x86-64')`)
  executable('gcc', `const fs = require('node:fs'); if (process.argv.includes('-dumpmachine')) { console.log(process.env.FIXTURE_MACHINE || 'x86_64-w64-mingw32'); } else { if (!process.argv.includes('-lbcrypt')) process.exit(2); const output = process.argv[process.argv.indexOf('-o') + 1]; fs.writeFileSync(output, '#!/bin/sh\\nprintf \\'OpenSSL ${version} fixture\\\\n\\'\\n'); fs.chmodSync(output, 0o755); }`)
  const marker = join(directory, 'configured')
  const env = { ...process.env, PATH: `${bin}:${process.env.PATH}`, MSYSTEM: 'UCRT64', OPENSSL_PREFIX: prefix,
    FIXTURE_ARCHIVE: archive, FIXTURE_CONFIGURED: marker, TMPDIR: directory }
  return { directory, prefix, marker, archive, env, run: overrides => spawnSync('bash', [script], { env: { ...env, ...overrides }, encoding: 'utf8' }) }
}

test('Windows builder verifies and stages only a static archive, headers, license and linked version', t => {
  const f = fixture(t), result = f.run()
  assert.equal(result.status, 0, result.stdout + result.stderr)
  assert.equal(readFileSync(join(f.prefix, 'lib/libcrypto.a'), 'utf8'), 'fixture archive')
  assert.equal(readFileSync(join(f.prefix, 'share/licenses/openssl/LICENSE.txt'), 'utf8'), 'fixture license\n')
  assert.match(readFileSync(join(f.prefix, 'BUILD_VERSION.txt'), 'utf8'), new RegExp(`^OpenSSL ${version.replaceAll('.', '\\.')} `))
  assert.ok(existsSync(f.marker))
  assert.equal(existsSync(join(f.prefix, 'bin/openssl.exe')), false)
})

test('a changed source archive is rejected before configure and leaves no installed prefix', t => {
  const f = fixture(t)
  writeFileSync(f.archive, 'tampered source')
  const result = f.run()
  assert.notEqual(result.status, 0)
  assert.match(result.stderr, /checksum mismatch/)
  assert.equal(existsSync(f.marker), false)
  assert.equal(existsSync(f.prefix), false)
})

test('Windows builder rejects the wrong environment, compiler and existing destination', t => {
  const f = fixture(t)
  for (const env of [{ MSYSTEM: 'MINGW64' }, { FIXTURE_MACHINE: 'aarch64-w64-mingw32' }, { OPENSSL_BUILD_JOBS: '0' }]) {
    assert.notEqual(f.run(env).status, 0)
    assert.equal(existsSync(f.marker), false)
  }
  mkdirSync(f.prefix)
  writeFileSync(join(f.prefix, 'keep'), 'existing data')
  assert.notEqual(f.run().status, 0)
  assert.equal(readFileSync(join(f.prefix, 'keep'), 'utf8'), 'existing data')
})
