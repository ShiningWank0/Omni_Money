import { readFileSync, writeFileSync } from 'node:fs'
import { dirname, join, relative } from 'node:path'

// Inventory the code retained by the production bundler. A package's dev flag
// is deliberately not used: build-time dependencies can contribute runtime code.
export function bundledPackages(bundle, root) {
  const packages = new Map()
  for (const chunk of Object.values(bundle)) {
    if (chunk.type !== 'chunk') continue
    for (const id of Object.keys(chunk.modules || {})) {
      const clean = id.replace(/\\/g, '/').replace(/\?.*$/, '')
      if (clean.includes('/node_modules/')) {
        let directory = dirname(clean)
        while (directory.includes('/node_modules/')) {
          try {
            const info = JSON.parse(readFileSync(join(directory, 'package.json'), 'utf8'))
            if (info.name && info.version) {
              packages.set(info.name, { name: info.name, version: info.version, directory: relative(root, directory).replace(/\\/g, '/') })
              break
            }
          } catch (error) { if (error.code !== 'ENOENT') throw error }
          directory = dirname(directory)
        }
      } else if (clean.includes('vite/') || clean.includes('vite:')) {
        const directory = join(root, 'node_modules/vite')
        const info = JSON.parse(readFileSync(join(directory, 'package.json'), 'utf8'))
        packages.set(info.name, { name: info.name, version: info.version, directory: 'node_modules/vite' })
      }
    }
  }
  return [...packages.values()].sort((a, b) => a.name.localeCompare(b.name, 'en'))
}

export function ossInventoryPlugin() {
  let root
  return {
    name: 'oss-runtime-inventory',
    apply: 'build',
    configResolved(config) { root = config.root },
    generateBundle(_options, bundle) {
      writeFileSync(join(root, 'bundled-dependencies.json'), `${JSON.stringify(bundledPackages(bundle, root), null, 2)}\n`)
    },
  }
}
