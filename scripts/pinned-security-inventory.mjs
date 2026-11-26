import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { existsSync, lstatSync, readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

export const root = fileURLToPath(new URL('../', import.meta.url))
export const groups = ['images', 'tools', 'native', 'modules']
export const inputFiles = ['Dockerfile', '.go-version', '.node-version', 'build/locks/docker.json', 'build/locks/linux-environments.json',
  'build/locks/msys2.json', 'build/locks/macos.json', 'build/locks/trivy.json', 'build/locks/trivy-source.json', 'build/locks/gh.json',
  'build/locks/webview2.json', 'build/locks/security-monitor.json',
  ...['windows', 'darwin'].map(os => `scripts/build-openssl-${os}.sh`),
  ...['linux', 'windows', 'darwin'].map(os => `scripts/build-sqlcipher-${os}.sh`)]
export const sha256 = value => createHash('sha256').update(value).digest('hex')
export const exactVersion = value => { assert.match(value, /^\d+(?:\.\d+){1,3}[a-z]?$/); return value }
export function compareVersion(a, b) {
  exactVersion(a); exactVersion(b)
  const parts = value => value.match(/\d+|[a-z]+/g).map(x => /^\d+$/.test(x) ? Number(x) : x)
  const left = parts(a), right = parts(b)
  for (let i = 0; i < Math.max(left.length, right.length); i++) {
    const x = left[i] ?? 0, y = right[i] ?? 0
    if (x !== y) return typeof x === typeof y ? (x < y ? -1 : 1) : typeof x === 'number' ? -1 : 1
  }
  return 0
}
export function inventory(directory = root) {
  const modulePaths = []
  const walk = folder => {
    for (const item of readdirSync(join(directory, folder), { withFileTypes: true })) {
      const path = folder ? folder + '/' + item.name : item.name
      if (item.isDirectory() && !['.git', '.oss-cache', '.codex', '.agents', '.aws', 'node_modules', 'bin', 'dist'].includes(item.name)) walk(path)
      else if (item.name.endsWith('.mod')) { assert.ok(item.isFile(), 'module manifests must be regular files'); modulePaths.push(path) }
    }
  }
  walk('')
  const modules = modulePaths.sort().map(path => ({ path, sum: path.replace(/\.mod$/, '.sum') }))
  assert.ok(modules.some(x => x.path === 'go.mod'), 'application module is missing')
  for (const item of modules) assert.ok(existsSync(join(directory, item.sum)), `${item.path}: checksum file missing`)
  const knownLocks = new Set([...inputFiles.filter(x => x.startsWith('build/locks/')).map(x => x.split('/').at(-1)), 'webview2-source.json'])
  for (const name of readdirSync(join(directory, 'build/locks'))) if (name.endsWith('.json')) assert.ok(knownLocks.has(name), `unregistered fixed input: ${name}`)
  const workflowFiles = readdirSync(join(directory, '.github/workflows')).filter(x => /\.ya?ml$/.test(x)).sort().map(x => '.github/workflows/' + x)
  const optionalInputs = ['scripts/security-patches/picomatch.json'].filter(path => existsSync(join(directory, path)))
  const paths = [...inputFiles, ...modules.flatMap(x => [x.path, x.sum]), 'scripts/security-reports.mjs', ...workflowFiles, ...optionalInputs]
  const content = new Map(paths.map(path => {
    assert.ok(lstatSync(join(directory, path)).isFile(), 'inventory inputs must be regular files')
    return [path, readFileSync(join(directory, path), 'utf8')]
  }))
  const identity = sha256(JSON.stringify([...content]))
  const json = (path, schema = 1) => { const v = JSON.parse(content.get(`build/locks/${path}.json`)); assert.equal(v.schema, schema); return v }
  const images = [], tools = [], sources = []
  const image = (name, ref) => {
    assert.match(ref, /^[a-z0-9/._:-]+@sha256:[a-f0-9]{64}$/)
    for (const arch of ['amd64', 'arm64']) images.push({ name: `${name}/${arch}`, ref, platform: `linux/${arch}` })
  }
  for (const [name, item] of Object.entries(json('linux-environments').environments)) image(name, item.image)
  const docker = json('docker')
  image('buildkit', docker.buildkitImage); image('sbom-generator', docker.sbomImage)
  const stages = new Set()
  for (const [index, match] of [...content.get('Dockerfile').matchAll(/^FROM(?:\s+--platform=\S+)?\s+(\S+)(?:\s+AS\s+(\S+))?/gim)].entries()) {
    const [, ref, alias] = match
    if (ref !== 'scratch' && !stages.has(ref) && !images.some(x => x.ref === ref)) image(alias || `dockerfile-stage-${index}`, ref)
    if (alias) stages.add(alias)
  }
  const archive = (name, item) => {
    assert.match(item.url, /^https:\/\/(?:github\.com|download\.docker\.com)\//)
    assert.match(item.sha256, /^[a-f0-9]{64}$/)
    tools.push({ name, url: item.url, sha256: item.sha256 })
  }
  for (const [arch, set] of Object.entries(docker.platforms)) {
    assert.deepEqual(Object.keys(set).sort(), ['buildx', 'compose', 'engine'], 'new Docker tools need vulnerability coverage')
    for (const name of ['engine', 'buildx', 'compose']) archive(`${name}/${arch}`, set[name])
  }
  for (const [arch, item] of Object.entries(json('gh').platforms)) archive(`gh/${arch}`, item)
  const trivy = json('trivy', 2), trivySource = json('trivy-source')
  assert.equal(trivy.version, trivySource.version)
  assert.equal(trivy.sourceRevision, trivySource.revision)
  assert.match(trivySource.sha256, /^[a-f0-9]{64}$/)
  assert.deepEqual(Object.keys(trivy.platforms).sort(), ['aarch64', 'x86_64'])
  for (const [arch, item] of Object.entries(trivy.platforms)) {
    assert.match(item.sha256, /^[a-f0-9]{64}$/)
    assert.equal(item.url, 'https://ghcr.io/v2/shiningwank0/omni_money-ci-trivy/blobs/sha256:' + item.sha256)
    assert.match(item.artifact, /^ghcr\.io\/shiningwank0\/omni_money-ci-trivy@sha256:[a-f0-9]{64}$/)
    tools.push({ name: `trivy/${arch}`, url: item.url, sha256: item.sha256 })
  }
  const monitor = json('security-monitor')
  archive('grype/amd64', monitor.grype); archive('oras/amd64', monitor.oras)
  const scriptVersion = (name, platforms) => {
    const versions = platforms.map(os => content.get(`scripts/build-${name}-${os}.sh`).match(new RegExp(`${name.toUpperCase()}_VERSION="([^"]+)"`))?.[1])
    assert.ok(versions.every(x => x && x === versions[0]), `${name} source versions disagree`)
    return exactVersion(versions[0])
  }
  const component = (name, version, cpe) => ({ type: 'library', name, version: exactVersion(version),
    'bom-ref': `native/${name}`, cpe: `cpe:2.3:a:${cpe}:${version}:*:*:*:*:*:*:*`,
    properties: [{ name: 'syft:location:0:path', value: `native/${name}` }] })
  const openssl = scriptVersion('openssl', ['darwin', 'windows'])
  const sqlcipher = scriptVersion('sqlcipher', ['linux', 'darwin', 'windows'])
  const mac = json('macos'), gh = json('gh')
  const native = [component('openssl', openssl, 'openssl:openssl'),
    component('node', content.get('.node-version').trim().replace(/^v/, ''), 'nodejs:node.js'),
    component('tcl', mac.tcl.version, 'tcl:tcl\\/tk'), component('xcode', mac.xcode.version, 'apple:xcode')]
  // SQLCipher and WebView2 are explicitly tracked by release/advisory review;
  // do not silently guess a CPE or treat absence from NVD as a clean scan.
  const advisories = [
    { name: 'sqlcipher', version: sqlcipher, keyword: 'SQLCipher', product: 'sqlcipher' },
    { name: 'webview2', version: exactVersion(json('webview2').version), keyword: 'WebView2', product: 'webview2' },
  ]
  const watch = (name, repo, version, pattern) => sources.push({ name, repo, version: exactVersion(version), pattern })
  if (content.has('scripts/security-patches/picomatch.json')) {
    const patch = JSON.parse(content.get('scripts/security-patches/picomatch.json'))
    assert.equal(patch.schema, 1); assert.equal(patch.name, 'picomatch')
    assert.ok(Number.isSafeInteger(patch.trackingIssue) && patch.trackingIssue > 0)
    watch('picomatch', 'micromatch/picomatch', patch.version, '^v?([0-9]+\\.[0-9]+\\.[0-9]+)$')
    sources.at(-1).trackingIssue = patch.trackingIssue
  }
  watch('openssl', 'openssl/openssl', openssl, `^openssl-(${openssl.split('.').slice(0, 2).join('\\.')}\\.[0-9]+)$`)
  watch('sqlcipher', 'sqlcipher/sqlcipher', sqlcipher, '^v([0-9]+\\.[0-9]+\\.[0-9]+)$')
  watch('trivy', 'aquasecurity/trivy', trivy.version, '^v([0-9]+\\.[0-9]+\\.[0-9]+)$')
  watch('gh', 'cli/cli', gh.version, '^v([0-9]+\\.[0-9]+\\.[0-9]+)$')
  for (const [name, repo, version] of [['buildx', 'docker/buildx', docker.buildxVersion], ['compose', 'docker/compose', docker.composeVersion],
    ['grype', 'anchore/grype', monitor.grype.version], ['oras', 'oras-project/oras', monitor.oras.version]]) watch(name, repo, version, '^v([0-9]+\\.[0-9]+\\.[0-9]+)$')
  watch('engine', 'moby/moby', docker.engineVersion, '^v([0-9]+\\.[0-9]+\\.[0-9]+)$')
  const buildkit = docker.buildkitImage.match(/:v([0-9]+\.[0-9]+\.[0-9]+)@/)
  assert.ok(buildkit); watch('buildkit', 'moby/buildkit', buildkit[1], '^v([0-9]+\\.[0-9]+\\.[0-9]+)$')
  const msys = json('msys2'); assert.match(msys.artifact, /^ghcr\.io\/shiningwank0\/omni_money-build-msys2@sha256:[a-f0-9]{64}$/)
  const remoteModules = []
  const legacyGovuln = content.get('scripts/security-reports.mjs').match(/golang\.org\/x\/vuln\/cmd\/govulncheck@(v[0-9]+\.[0-9]+\.[0-9]+)/)
  if (legacyGovuln) remoteModules.push({ module: 'golang.org/x/vuln', version: legacyGovuln[1] })
  for (const file of workflowFiles) {
    for (const [, pkg, version] of content.get(file).matchAll(/\bgo (?:run|install) ([a-zA-Z0-9./_-]+)@(v[0-9]+\.[0-9]+\.[0-9]+)/g)) {
      const module = pkg.split('/cmd/')[0]
      if (!remoteModules.some(x => x.module === module && x.version === version)) remoteModules.push({ module, version })
    }
  }
  const wails2 = content.get('go.mod')?.match(/github\.com\/wailsapp\/wails\/v2 (v\S+)/)
  if (wails2) remoteModules.push({ module: 'github.com/wailsapp/wails/v2', version: wails2[1] })
  const goVersion = exactVersion(content.get('.go-version').trim())
  return { identity, images, tools, native, sources, advisories, msys, monitor, trivy, modules, remoteModules, goVersion }
}
