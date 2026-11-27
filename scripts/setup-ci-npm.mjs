import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { appendFileSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { join } from 'node:path'

// Use the same reviewed upstream archives as the Docker frontend builder.
const dockerfile = readFileSync(new URL('../Dockerfile', import.meta.url), 'utf8')
const archives = new Map()
const pattern = /^ADD --checksum=sha256:([a-f0-9]{64}) (https:\/\/registry\.npmjs\.org\/\S+) \/tmp\/omni-([\w-]+)\.tgz\r?$/gm
for (const [, digest, url, name] of dockerfile.matchAll(pattern)) {
  assert.ok(!archives.has(name), 'Duplicate npm archive ' + name)
  archives.set(name, { url, digest })
}

const directory = join(process.env.RUNNER_TEMP, 'omni-ci-npm')
mkdirSync(directory)
const npm = join(directory, 'npm')
for (const [name, packageName, target] of [
  ['npm', 'npm', npm],
  ['glob', 'glob', join(npm, 'node_modules/glob')],
  ['minimatch', 'minimatch', join(npm, 'node_modules/minimatch')],
  ['nonrecursive-glob-match', '@isaacs/nonrecursive-glob-match', join(npm, 'node_modules/@isaacs/nonrecursive-glob-match')],
]) {
  const { url, digest } = archives.get(name)
  assert.ok(url.startsWith('https://registry.npmjs.org/' + packageName + '/-/'))
  const file = join(directory, name + '.tgz')
  execFileSync('curl', ['--fail', '--silent', '--show-error', '--retry', '3', '--output', file, url])
  const contents = readFileSync(file)
  assert.equal(createHash('sha256').update(contents).digest('hex'), digest)
  rmSync(target, { recursive: true, force: true })
  mkdirSync(target, { recursive: true })
  // A Windows drive letter in an archive path is a remote host to GNU tar.
  execFileSync('tar', ['-xzf', '-', '--strip-components=1'], { cwd: target, input: contents })
  assert.equal(JSON.parse(readFileSync(join(target, 'package.json'))).name, packageName)
  rmSync(file)
}

const fromNpm = createRequire(join(npm, 'package.json'))
assert.equal(JSON.parse(readFileSync(join(npm, 'node_modules/brace-expansion/package.json'))).version, '5.0.12')
assert.equal(fromNpm('glob/package.json').version, '13.0.7')
assert.deepEqual(fromNpm('glob').globSync('package.json', { cwd: npm }), ['package.json'])
assert.equal(fromNpm('minimatch/package.json').version, '11.0.9')
assert.ok(fromNpm('minimatch').minimatch('src/index.js', 'src/{index,app}.js'))
assert.equal(execFileSync(process.execPath, [join(npm, 'bin/npm-cli.js'), '--version'], { encoding: 'utf8' }).trim(), '12.2.3')

const bin = join(directory, 'bin')
mkdirSync(bin)
for (const name of ['npm', 'npx']) {
  const cli = name + '-cli.js'
  writeFileSync(join(bin, name), '#!/bin/sh\nexec node "$(dirname "$0")/../npm/bin/' + cli + '" "$@"\n', { mode: 0o755 })
  if (process.platform === 'win32') {
    writeFileSync(join(bin, name + '.cmd'), '@echo off\r\nnode "%~dp0..\\npm\\bin\\' + cli + '" %*\r\n')
  }
}
appendFileSync(process.env.GITHUB_PATH, bin + '\n')
console.log('Using reviewed npm 12.2.3 with brace-expansion 5.0.12, glob 13.0.7 and minimatch 11.0.9')
