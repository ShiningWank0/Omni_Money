import { posix } from 'node:path'

// package-lock v3 stores installed links separately from their source metadata.
// Only node_modules entries represent installed instances; workspace/source
// entries supply metadata and must never become additional package instances.
export function npmPackages(lock) {
  if (lock.lockfileVersion !== 3 || !lock.packages?.['']) throw new Error('Unsupported npm package inventory')
  const entries = lock.packages
  const safePath = path => {
    if (typeof path !== 'string' || !path || /[\\:?#\0]/.test(path) || path.startsWith('/') ||
        path.split('/').some(part => !part || part === '.' || part === '..') || posix.normalize(path) !== path) {
      throw new Error('Unsafe npm package source path')
    }
    return path
  }
  return Object.entries(entries).filter(([path]) => path.includes('node_modules/')).map(([directory, installed]) => {
    safePath(directory)
    const tail = directory.slice(directory.lastIndexOf('node_modules/') + 'node_modules/'.length)
    if (!/^(?:@[a-zA-Z0-9_.~-]+\/)?[a-zA-Z0-9_.~-]+$/.test(tail)) throw new Error('Invalid installed npm package identity')
    let sourceDirectory = directory, metadata = installed
    const seen = new Set([directory])
    while (metadata.link === true) {
      sourceDirectory = safePath(metadata.resolved)
      if (seen.has(sourceDirectory)) throw new Error('Cyclic npm package link')
      seen.add(sourceDirectory)
      metadata = entries[sourceDirectory]
      if (!metadata || typeof metadata !== 'object') throw new Error('Missing npm package link metadata')
    }
    if (typeof metadata.version !== 'string' || !metadata.version) throw new Error('Missing installed npm package version')
    if (installed.version && installed.version !== metadata.version) throw new Error('Contradictory npm package link version')
    return { directory, sourceDirectory, name: metadata.name || tail, version: metadata.version,
      dev: installed.dev ?? metadata.dev ?? false, license: metadata.license, linked: installed.link === true }
  })
}
