import { createHash } from 'node:crypto'
import { readFileSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'

export const displayModule = 'virtual:omni-oss-notices'
const moduleID = `\0${displayModule}`
const definitions = JSON.parse(readFileSync(new URL('../../licenses/targets.json', import.meta.url), 'utf8'))
const hash = value => createHash('sha256').update(value).digest('hex')
const stable = value => `${JSON.stringify(value, null, 2)}\n`
const archName = value => ({ x64: 'amd64', arm64: 'arm64', amd64: 'amd64' })[value]

export function resolveDisplayTarget(env = process.env, host = { platform: process.platform, arch: process.arch }) {
  const mode = env.OMNI_OSS_MODE || (env.BUILD_PLATFORM ? 'desktop' : 'server')
  if (!['desktop', 'server'].includes(mode)) throw new Error('Unknown OSS application mode')
  let target = env.VITE_OSS_TARGET
  if (!target) {
    const platform = env.BUILD_PLATFORM?.split('/')
    if (platform && platform.length !== 2) throw new Error('Invalid desktop build platform')
    const os = platform?.[0] || env.GOOS || (mode === 'server' ? 'linux' : host.platform === 'win32' ? 'windows' : host.platform)
    const arch = platform?.[1] || env.GOARCH || archName(host.arch)
    target = `${mode}-${os}-${arch}`
  }
  if (!definitions.some(config => config.id === target) ||
      ((env.OMNI_OSS_MODE || env.BUILD_PLATFORM) && !target.startsWith(`${mode}-`)) ||
      (env.BUILD_PLATFORM && target !== `desktop-${env.BUILD_PLATFORM.replace('/', '-')}`)) {
    throw new Error(`Unknown or inconsistent OSS distribution target: ${target}`)
  }
  return target
}

export function selectDisplayCatalog(catalog, target) {
  const config = definitions.find(config => config.id === target)
  if (!config || !Array.isArray(catalog.components)) throw new Error('Unknown OSS target or invalid catalog')
  const components = catalog.components.filter(component => component.targets.includes(target)).map(component => {
    const { externalTargets, ...selected } = component
    let version = component.version
    // The reviewed native entry records the pinned macOS build and the series
    // used by other platforms. Do not show another OS's version in this build.
    if (component.id === 'native:openssl') {
      const versions = /^3\.x \(macOS: (\d+\.\d+\.\d+)\)$/.exec(version)
      if (versions) version = config.goos === 'darwin' ? versions[1] : '3.x'
    }
    return { ...selected, version, targets: [target], ...(externalTargets?.includes(target) ? { externalTargets: [target] } : {}) }
  })
  if (!components.length || new Set(components.map(component => component.id)).size !== components.length) throw new Error('Empty or duplicate OSS display inventory')
  return { schema: catalog.schema, applicationModule: catalog.applicationModule, applicationLicense: catalog.applicationLicense,
    target: { id: config.id, mode: config.mode, goos: config.goos, goarch: config.goarch }, components }
}

export function verifyDisplayInventory(frontendRoot, catalog, target) {
  const expected = selectDisplayCatalog(catalog, target)
  const inventory = JSON.parse(readFileSync(join(frontendRoot, 'oss-display-inventory.json'), 'utf8'))
  if (inventory.schema !== 1 || inventory.target !== target || inventory.catalogSHA256 !== hash(stable(expected)) ||
      stable(inventory.components) !== stable(expected.components.map(({ id, version }) => ({ id, version }))) ||
      !Array.isArray(inventory.chunks) || !inventory.chunks.length || new Set(inventory.chunks.map(chunk => chunk.name)).size !== inventory.chunks.length) {
    throw new Error(`Built OSS display inventory differs from ${target}`)
  }
  for (const chunk of inventory.chunks) {
    if (!/^assets\/[A-Za-z0-9_-]+\.js$/.test(chunk.name) || hash(readFileSync(join(frontendRoot, 'dist', chunk.name))) !== chunk.sha256) {
      throw new Error('Built OSS display chunk is missing or changed')
    }
  }
}

export function ossCatalogPlugin() {
  let frontendRoot, target, selected
  return {
    name: 'target-specific-oss-notices',
    configResolved(config) {
      frontendRoot = config.root
      target = resolveDisplayTarget({ ...process.env, ...(config.env?.VITE_OSS_TARGET ? { VITE_OSS_TARGET: config.env.VITE_OSS_TARGET } : {}) })
    },
    resolveId(id) { if (id === displayModule) return moduleID },
    load(id) {
      if (id !== moduleID) return
      const catalog = JSON.parse(readFileSync(join(frontendRoot, 'src/generated/thirdPartyNotices.json'), 'utf8'))
      selected = selectDisplayCatalog(catalog, target)
      return `export default ${JSON.stringify(selected)};`
    },
    writeBundle(output, bundle) {
      const chunks = Object.values(bundle).filter(chunk => chunk.type === 'chunk' && Object.hasOwn(chunk.modules, moduleID))
        .map(chunk => ({ name: chunk.fileName, sha256: hash(chunk.code) }))
        .sort((left, right) => left.name.localeCompare(right.name, 'en'))
      if (!selected || !chunks.length || resolve(frontendRoot, output.dir) !== join(frontendRoot, 'dist')) throw new Error('OSS display module is missing from the production distribution')
      writeFileSync(join(frontendRoot, 'oss-display-inventory.json'), stable({ schema: 1, target,
        catalogSHA256: hash(stable(selected)), components: selected.components.map(({ id, version }) => ({ id, version })), chunks }))
    },
  }
}
