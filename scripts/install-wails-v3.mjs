import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { mkdirSync, readFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = resolve(fileURLToPath(new URL('..', import.meta.url)))
const module = 'github.com/wailsapp/wails/v3'
export function verifyWailsCLI(info, version, toolchain) {
  assert.doesNotMatch(info, /^\s*=>/m, 'Replaced CLI dependencies are forbidden')
  assert.ok(info.split('\n')[0].endsWith('go' + toolchain), 'CLI Go toolchain differs from the project')
  const modules = [...info.matchAll(/^\s*(?:mod|dep)\s+(\S+)\s+(\S+)/gm)]
  for (const [path, expected] of [[module, version], ['golang.org/x/tools', 'v0.52.0'], ['golang.org/x/net', 'v0.62.0']]) {
    const values = modules.filter(match => match[1] === path)
    assert.equal(values.length, 1, 'Missing or duplicate CLI module: ' + path)
    assert.equal(values[0][2], expected, 'CLI module mismatch: ' + path)
  }
}

export function selectedWailsVersion() {
  const mod = readFileSync(join(root, 'go.mod'), 'utf8')
  const selected = mod.match(/^\s*github\.com\/wailsapp\/wails\/v3\s+(v3\.\d+\.\d+)\s*$/m)?.[1]
  assert.ok(selected, 'Pin an official stable Wails v3 module')
  const tool = readFileSync(join(root, 'scripts/buildtools/wails/go.mod'), 'utf8')
  assert.match(tool, new RegExp('github.com/wailsapp/wails/v3\\s+' + selected.replaceAll('.', '\\.') + '\\s'))
  return { version: selected, toolchain: mod.match(/^toolchain go(\S+)$/m)?.[1] }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const selected = selectedWailsVersion()
    const go = args => {
      const result = spawnSync('go', args, { cwd: root, encoding: 'utf8', timeout: 20 * 60 * 1000, maxBuffer: 8 * 1024 * 1024 })
      assert.equal(result.status, 0, result.stderr || String(result.error || 'Go command failed'))
      return result.stdout
    }
    const directory = join(go(['env', 'GOPATH']).trim(), 'bin')
    mkdirSync(directory, { recursive: true })
    const output = join(directory, 'wails3' + (process.platform === 'win32' ? '.exe' : ''))
    go(['-C', join(root, 'scripts/buildtools/wails'), 'build', '-mod=readonly', '-o', output, module + '/cmd/wails3'])
    const info = go(['version', '-m', output])
    verifyWailsCLI(info, selected.version, selected.toolchain)
    console.log(info)
  } catch (error) { console.error(error.message); process.exitCode = 1 }
}
