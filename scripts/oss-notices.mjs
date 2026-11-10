import { createHash } from 'node:crypto'
import { spawnSync } from 'node:child_process'
import { mkdirSync, readFileSync, readdirSync, writeFileSync } from 'node:fs'
import { dirname, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

export const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
export const targetConfigs = JSON.parse(readFileSync(join(root, 'licenses/targets.json'), 'utf8'))
export const targets = targetConfigs.map(target => target.id)
const catalogPath = 'frontend/src/generated/thirdPartyNotices.json'
const approvalPath = 'licenses/approved.json'
const hash = value => createHash('sha256').update(value).digest('hex')
const readJSON = path => JSON.parse(readFileSync(join(root, path), 'utf8'))
const stable = value => `${JSON.stringify(value, null, 2)}\n`
const textFile = /^(licen[cs]e|copying|notice|copyright|patents)([._-].*)?$/i

export const normalizeLineEndings = text => text.replace(/\r\n?/g, '\n')

export function applicationModule() {
  const value = readFileSync(join(root, 'go.mod'), 'utf8').match(/^module\s+(\S+)\s*$/m)?.[1]
  if (!value) throw new Error('Missing application module identity')
  return value
}

export function parseObjects(input) {
  const objects = []
  let start = 0, depth = 0, quoted = false, escaped = false
  for (let index = 0; index < input.length; index++) {
    const char = input[index]
    if (quoted) {
      if (escaped) escaped = false
      else if (char === '\\') escaped = true
      else if (char === '"') quoted = false
    } else if (char === '"') quoted = true
    else if (char === '{') { if (depth++ === 0) start = index }
    else if (char === '}' && --depth === 0) objects.push(JSON.parse(input.slice(start, index + 1)))
  }
  if (depth || quoted) throw new Error('Incomplete Go dependency inventory')
  return objects
}

function command(program, args, env = {}) {
  const result = spawnSync(program, args, { cwd: root, env: { ...process.env, ...env }, encoding: 'utf8', maxBuffer: 64 * 1024 * 1024 })
  if (result.status !== 0) throw new Error(`${program} ${args.join(' ')} failed: ${result.stderr || result.error}`)
  return result.stdout
}

export function validateTargetCoverage() {
  if (targetConfigs.some(config => config.id !== `${config.mode}-${config.goos}-${config.goarch}` || !['desktop', 'server'].includes(config.mode))) throw new Error('Invalid OSS release target definition')
  const expected = []
  for (const [file, mode] of [['release-desktop.yml', 'desktop'], ['release-docker.yml', 'server']]) {
    const workflow = readFileSync(join(root, '.github/workflows', file), 'utf8')
    for (const [, os, arch] of workflow.matchAll(/^\s+platform: ([a-z0-9]+)\/([a-z0-9]+)\s*$/gm)) expected.push(`${mode}-${os}-${arch}`)
  }
  if (JSON.stringify([...new Set(expected)].sort()) !== JSON.stringify([...targets].sort())) throw new Error('OSS target definitions differ from the release build matrices')
}

// Read notices only from the package and its parents up to its module root.
// This includes nested license boundaries without pulling in unrelated tests,
// examples, generators, or the entire development dependency tree.
function notices(directory, boundary) {
  const paths = new Set()
  for (let current = directory; ; current = dirname(current)) {
    for (const entry of readdirSync(current, { withFileTypes: true })) {
      if (entry.isFile() && textFile.test(entry.name)) paths.add(join(current, entry.name))
    }
    if (current === boundary) break
    if (current === dirname(current) || !current.startsWith(`${boundary}/`)) throw new Error('Notice path escaped dependency')
  }
  return [...paths].sort().map(path => ({ path: relative(boundary, path).replace(/\\/g, '/'), text: readFileSync(path, 'utf8').replace(/\r\n/g, '\n') }))
}

function goComponents() {
  const components = new Map()
  for (const { id: target, goos: GOOS, goarch: GOARCH, tags, entry } of targetConfigs) {
    const packages = parseObjects(command('go', ['list', '-mod=readonly', '-deps', '-e', '-json', '-tags', tags.join(','), entry], { GOOS, GOARCH, CGO_ENABLED: '1' }))
    for (const pkg of packages) {
      if (pkg.Error || pkg.DepsErrors?.length) throw new Error(`Cannot resolve ${target}: ${JSON.stringify(pkg.Error || pkg.DepsErrors)}`)
      if (!pkg.Module || pkg.Module.Main) continue
      if (pkg.Module.Replace) throw new Error(`Review replacement module ${pkg.Module.Path}`)
      const id = `go:${pkg.Module.Path}`
      let component = components.get(id)
      if (!component) {
        component = { id, name: pkg.Module.Path, version: pkg.Module.Version, url: `https://pkg.go.dev/${pkg.Module.Path}@${pkg.Module.Version}`, targets: [], files: [] }
        components.set(id, component)
      }
      if (!component.targets.includes(target)) component.targets.push(target)
      const files = notices(pkg.Dir, pkg.Module.Dir)
      for (const file of files) if (!component.files.some(existing => existing.path === file.path)) component.files.push(file)
    }
  }
  return [...components.values()]
}

function npmComponents() {
  const lock = readJSON('frontend/package-lock.json')
  return readJSON('frontend/bundled-dependencies.json').map(pkg => {
    const locked = lock.packages[pkg.directory]
    if (!locked || locked.version !== pkg.version) throw new Error(`Unpinned bundled package ${pkg.name}@${pkg.version}`)
    const directory = join(root, 'frontend', pkg.directory)
    const files = notices(directory, directory)
    // Only Vite's emitted preload helpers are distributed. Its server-side
    // bundled tooling dependencies are not present in the browser output.
    if (pkg.name === 'vite') for (const file of files) file.text = file.text.split('\n# Licenses of bundled dependencies')[0].trimEnd() + '\n'
    return { id: `npm:${pkg.name}`, name: pkg.name, version: pkg.version, license: locked.license,
      url: `https://www.npmjs.com/package/${pkg.name}/v/${pkg.version}`, targets: [...targets], files }
  })
}

export function validateCatalog(catalog, approved) {
  if (catalog.schema !== 1 || approved.schema !== 1) throw new Error('Unsupported OSS catalog schema')
  if (!catalog.applicationLicense?.trim()) throw new Error('Missing application license')
  if (!catalog.applicationModule?.trim()) throw new Error('Missing application module identity')
  const ids = new Set()
  for (const component of catalog.components) {
    if (ids.has(component.id)) throw new Error(`Duplicate component ${component.id}`)
    ids.add(component.id)
    const approval = approved.components[component.id]
    if (!approval || approval.version !== component.version) throw new Error(`OSS review required: ${component.id}@${component.version}`)
    if (!component.files?.length || component.files.some(file => !file.text.trim())) throw new Error(`Missing license text: ${component.id}`)
    if (approval.sha256 !== hash(stable(component.files))) throw new Error(`License/NOTICE changed: ${component.id}`)
    if (!component.license || component.license !== approval.license) throw new Error(`Unreviewed license: ${component.id}`)
    if (!component.targets.length || component.targets.some(target => !targets.includes(target))) throw new Error(`Invalid targets: ${component.id}`)
  }
  const stale = Object.keys(approved.components).filter(id => !ids.has(id))
  if (stale.length) throw new Error(`Stale OSS approvals: ${stale.join(', ')}`)
}

// Carry an existing review forward only when the complete notice content and
// distribution boundary are identical. A version change is not a license review.
export function advanceUnchangedApprovals(previous, catalog, approved) {
  validateCatalog(previous, approved)
  if (stable({ ...catalog, components: [] }) !== stable({ ...previous, components: [] }) ||
      catalog.components.length !== previous.components.length) {
    throw new Error('OSS manual review required: application or distributed component set changed')
  }
  const old = new Map(previous.components.map(component => [component.id, component]))
  const updated = structuredClone(approved)
  for (const component of catalog.components) {
    const before = old.get(component.id)
    if (!before || stable({ ...component, version: before.version, url: before.url }) !== stable(before)) {
      throw new Error(`OSS manual review required: notices, license or distribution changed for ${component.id}`)
    }
    const expectedURL = component.id.startsWith('go:')
      ? `https://pkg.go.dev/${component.name}@${component.version}`
      : component.id.startsWith('npm:')
        ? `https://www.npmjs.com/package/${component.name}/v/${component.version}` : before.url
    if (component.url !== expectedURL || (component.id.startsWith('native:') && component.version !== before.version)) {
      throw new Error(`OSS manual review required: native version or source URL changed for ${component.id}`)
    }
    updated.components[component.id].version = component.version
  }
  validateCatalog(catalog, updated)
  return updated
}

export function collect() {
  validateTargetCoverage()
  const approved = readJSON(approvalPath)
  const native = readJSON('licenses/native/components.json').map(component => ({ ...component, targets: component.targets === 'all' ? [...targets] : component.targets,
    files: component.files.map(path => ({ path, text: readFileSync(join(root, 'licenses/native', path), 'utf8') })) }))
  const components = [...npmComponents(), ...goComponents(), ...native].map(component => ({ ...component,
    license: component.license || approved.components[component.id]?.license,
    targets: component.targets.sort(), files: component.files.sort((a, b) => a.path.localeCompare(b.path, 'en')),
  })).sort((a, b) => a.id.localeCompare(b.id, 'en'))
  const sqlcipher = components.find(component => component.id === 'native:sqlcipher')
  for (const file of ['linux', 'darwin', 'windows']) {
    const script = readFileSync(join(root, `scripts/build-sqlcipher-${file}.sh`), 'utf8')
    if (!script.includes(`SQLCIPHER_VERSION="${sqlcipher.version}"`)) throw new Error(`SQLCipher notice version differs from ${file} build`)
  }
  const goVersion = readFileSync(join(root, 'go.mod'), 'utf8').match(/^toolchain go(.+)$/m)?.[1]
  if (components.find(component => component.id === 'native:go').version !== goVersion) throw new Error('Go runtime license review required for new toolchain')
  const opensslVersion = readFileSync(join(root, 'scripts/build-openssl-darwin.sh'), 'utf8').match(/OPENSSL_VERSION="([^"]+)"/)?.[1]
  if (!components.find(component => component.id === 'native:openssl').version.includes(`macOS: ${opensslVersion})`)) throw new Error('OpenSSL notice version differs from macOS build')
  return { schema: 1, applicationModule: applicationModule(), applicationLicense: readFileSync(join(root, 'LICENSE'), 'utf8'), components }
}

export function usageFromInputs(catalog, lock, modules) {
  const included = new Map(catalog.components.map(component => [component.id, component]))
  const usage = []
  const add = (id, name, version, dependencyType, evidence) => {
    const component = included.get(id)
    usage.push({ id, name, version, dependencyType,
      distribution: component ? 'included' : 'excluded',
      targets: component?.targets || [],
      includedTargets: component?.targets.filter(target => !component.externalTargets?.includes(target)) || [],
      externalTargets: component?.externalTargets || [],
      license: component?.license || null,
      evidence: component ? evidence : `Not present in ${evidence}`,
    })
  }
  for (const [path, pkg] of Object.entries(lock.packages)) {
    if (!path) continue
    const name = path.slice(path.lastIndexOf('node_modules/') + 'node_modules/'.length)
    // Keep nested package instances separate, including build-only packages
    // whose names coincide with a different distributed version.
    const id = `npm:${name}`
    const component = included.get(id)
    if (component && component.version !== pkg.version) {
      usage.push({ id: `npm:${path}`, name, version: pkg.version, dependencyType: pkg.dev ? 'development' : 'runtime', distribution: 'excluded', targets: [], includedTargets: [], externalTargets: [], license: null, evidence: 'This package instance is not retained in the production bundle' })
    } else add(id, name, pkg.version, pkg.dev ? 'development' : 'runtime', 'production frontend bundle module inventory')
  }
  for (const module of modules) {
    if (!module.Main) add(`go:${module.Path}`, module.Path, module.Version, module.Indirect ? 'indirect' : 'direct', 'production Go dependency graphs for the configured release targets')
  }
  for (const component of catalog.components.filter(component => component.id.startsWith('native:'))) add(component.id, component.name, component.version, 'native', 'reviewed native build recipes; externalTargets identify system-provided runtime libraries')
  return { schema: 1, targets: targetConfigs, dependencies: usage.sort((a, b) => a.id.localeCompare(b.id, 'en')) }
}

export function collectUsage(catalog) {
  return usageFromInputs(catalog, readJSON('frontend/package-lock.json'),
    parseObjects(command('go', ['list', '-mod=readonly', '-m', '-json', 'all'])))
}

export function noticeText(catalog, target) {
  if (target && !targets.includes(target)) throw new Error(`Unknown OSS target: ${target}`)
  return 'Omni Money — Third-party software notices\n\n' + catalog.components.filter(component => !target || component.targets.includes(target)).map(component =>
    `${component.name} ${component.version}\n${component.license}\n${component.url}\n\n${component.files.map(file => `[${file.path}]\n${file.text}`).join('\n\n')}`
  ).join('\n\n' + '='.repeat(72) + '\n\n') + '\n'
}

export function verifyBundledPackages(catalog, inventory) {
  const expected = catalog.components.filter(component => component.id.startsWith('npm:')).map(component => `${component.name}@${component.version}`).sort()
  const actual = inventory.map(component => `${component.name}@${component.version}`).sort()
  if (!actual.length || JSON.stringify(expected) !== JSON.stringify(actual)) throw new Error('Built frontend dependencies differ from reviewed OSS inventory')
}

function main() {
  const [mode = 'check', target] = process.argv.slice(2)
  if (mode === 'review') {
    const catalog = collect()
    const review = { catalog, usage: collectUsage(catalog), candidates: Object.fromEntries(catalog.components.map(component => [component.id, {
      version: component.version, license: component.license || 'REVIEW REQUIRED', sha256: hash(stable(component.files)),
    }])) }
    mkdirSync(join(root, 'licenses/build'), { recursive: true })
    writeFileSync(join(root, 'licenses/build/review.json'), stable(review))
    console.log('Review licenses/build/review.json. This command does not change approvals or the displayed catalog.')
  } else if (mode === 'sync-unchanged') {
    const previous = readJSON(catalogPath)
    const catalog = collect()
    const approved = advanceUnchangedApprovals(previous, catalog, readJSON(approvalPath))
    const usage = collectUsage(catalog)
    writeFileSync(join(root, approvalPath), stable(approved))
    writeFileSync(join(root, catalogPath), stable(catalog))
    writeFileSync(join(root, 'licenses/dependency-usage.json'), stable(usage))
    console.log('Synchronized versions and inventories; existing notice text and distribution boundaries are unchanged')
  } else if (mode === 'refresh' || mode === 'check') {
    const catalog = collect()
    const usage = collectUsage(catalog)
    validateCatalog(catalog, readJSON(approvalPath))
    if (mode === 'refresh') {
      writeFileSync(join(root, catalogPath), stable(catalog))
      writeFileSync(join(root, 'licenses/dependency-usage.json'), stable(usage))
    } else if (stable(catalog) !== readFileSync(join(root, catalogPath), 'utf8') || stable(usage) !== readFileSync(join(root, 'licenses/dependency-usage.json'), 'utf8')) throw new Error('OSS inventory changed. Review approvals and run node scripts/oss-notices.mjs refresh.')
    console.log(`OSS check passed: ${catalog.components.length} components across ${targets.length} targets`)
  } else if (mode === 'package' || mode === 'package-all') {
    const catalog = readJSON(catalogPath)
    validateCatalog(catalog, readJSON(approvalPath))
    verifyBundledPackages(catalog, readJSON('frontend/bundled-dependencies.json'))
    if (mode === 'package' && !targets.includes(target)) throw new Error('package requires an explicit release target')
    const selectedTargets = mode === 'package-all' ? targets : [target]
    for (const target of selectedTargets) {
      const directory = join(root, 'licenses/build', target)
      mkdirSync(join(directory, 'native'), { recursive: true })
      writeFileSync(join(directory, 'THIRD_PARTY_NOTICES.txt'), noticeText(catalog, target))
      writeFileSync(join(directory, 'LICENSE'), readFileSync(join(root, 'LICENSE')))
      writeFileSync(join(directory, 'native/SQLCipher-LICENSE.txt'), catalog.components.find(component => component.id === 'native:sqlcipher').files[0].text)
      const targetConfig = targetConfigs.find(config => config.id === target)
      const manifest = { target, applicationModule: catalog.applicationModule, goos: targetConfig.goos, goarch: targetConfig.goarch,
        applicationVersion: process.env.VITE_APP_VERSION || process.env.VERSION || readFileSync(join(root, 'VERSION'), 'utf8').trim(),
        goVersion: `go${catalog.components.find(component => component.id === 'native:go').version}`,
        modules: catalog.components.filter(component => component.id.startsWith('go:') && component.targets.includes(target)).map(component => ({ path: component.name, version: component.version })),
        files: ['LICENSE', 'THIRD_PARTY_NOTICES.txt', 'native/SQLCipher-LICENSE.txt'].map(name => ({ name, sha256: hash(readFileSync(join(directory, name))) })) }
      writeFileSync(join(directory, 'manifest.json'), stable(manifest))
    }
  } else if (mode === 'verify') {
    const directory = process.argv[4]
    if (!directory || !targets.includes(target)) throw new Error('verify requires target and extracted license directory')
    const actual = readFileSync(resolve(root, directory, 'THIRD_PARTY_NOTICES.txt'), 'utf8')
    if (normalizeLineEndings(actual) !== normalizeLineEndings(noticeText(readJSON(catalogPath), target))) throw new Error('Packaged third-party notices are missing or changed')
    if (normalizeLineEndings(readFileSync(resolve(root, directory, 'LICENSE'), 'utf8')) !== normalizeLineEndings(readFileSync(join(root, 'LICENSE'), 'utf8'))) throw new Error('Packaged application license is missing or changed')
    if (target.startsWith('desktop-')) {
      if (!readFileSync(resolve(root, directory, 'BUILD_DEPENDENCIES.txt'), 'utf8').trim()) throw new Error('Packaged build dependency inventory is missing')
      if (normalizeLineEndings(readFileSync(resolve(root, directory, 'native/SQLCipher-LICENSE.txt'), 'utf8')).trim() !== normalizeLineEndings(readFileSync(join(root, 'licenses/native/sqlcipher.txt'), 'utf8')).trim()) throw new Error('Packaged native license is missing or changed')
    }
  } else throw new Error('Usage: oss-notices.mjs check|review|refresh|sync-unchanged|package-all|package TARGET|verify TARGET DIRECTORY')
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try { main() } catch (error) { console.error(error.message); process.exitCode = 1 }
}
