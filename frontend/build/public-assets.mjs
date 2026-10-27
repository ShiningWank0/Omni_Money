import { createHash } from 'node:crypto'
import { lstatSync, readdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join, posix, resolve } from 'node:path'

const contentHash = value => createHash('sha256').update(value).digest('hex')
const extensions = /\.(js|css|svg|png|jpe?g|webp|gif|ico|woff2?)$/

function allowedName(name) {
  return typeof name === 'string' && !/[\\:\x00]/.test(name) &&
    name.split('/').every(part => part && !part.startsWith('.')) &&
    (name === 'index.html' || (name.startsWith('assets/') && extensions.test(name)))
}

// Derive the inventory from bundler outputs, never by trusting every file that
// happens to be in dist (Vite's publicDir copies are not automatically public).
export function bundleManifest(bundle) {
  const files = Object.values(bundle).map(output => {
    if (!allowedName(output.fileName)) throw new Error('Forbidden public bundle filename')
    const bytes = output.type === 'chunk' ? output.code : output.source
    return { name: output.fileName, sha256: contentHash(bytes) }
  }).sort((a, b) => a.name < b.name ? -1 : a.name > b.name ? 1 : 0)
  if (!files.some(file => file.name === 'index.html') || new Set(files.map(file => file.name)).size !== files.length) {
    throw new Error('Missing index or duplicate public bundle filename')
  }
  return { version: 1, files }
}

export function verifyBuildTree(root, manifest) {
  const expected = new Map(manifest.files.map(file => [file.name, file.sha256]))
  const directories = new Set([''])
  for (const name of expected.keys()) {
    for (let parent = posix.dirname(name); parent !== '.'; parent = posix.dirname(parent)) directories.add(parent)
  }
  const visit = name => {
    const filename = join(root, name)
    const info = lstatSync(filename)
    if (info.isSymbolicLink()) throw new Error(`Linked public output: ${JSON.stringify(name)}`)
    if (info.isDirectory() && directories.has(name)) {
      for (const child of readdirSync(filename)) visit(name ? `${name}/${child}` : child)
      return
    }
    if (!info.isFile() || !expected.has(name)) throw new Error(`Unexpected public output: ${JSON.stringify(name)}`)
    const bytes = readFileSync(filename)
    if (/-----BEGIN (RSA |EC |DSA |OPENSSH |ENCRYPTED )?PRIVATE KEY-----/.test(bytes.toString('latin1')) ||
        bytes.subarray(0, 16).equals(Buffer.from('SQLite format 3\0'))) {
      throw new Error(`Private material in public output: ${JSON.stringify(name)}`)
    }
    if (contentHash(bytes) !== expected.get(name)) throw new Error(`Public output hash mismatch: ${JSON.stringify(name)}`)
    expected.delete(name)
  }
  visit('')
  if (expected.size) throw new Error('Missing public bundle outputs')
}

export function publicAssetsPlugin() {
  let frontendRoot
  return {
    name: 'verify-public-assets',
    apply: 'build',
    enforce: 'post',
    configResolved(config) { frontendRoot = config.root },
    writeBundle(output, bundle) {
      const manifest = bundleManifest(bundle)
      verifyBuildTree(resolve(frontendRoot, output.dir), manifest)
      writeFileSync(resolve(frontendRoot, 'public-assets-manifest.json'), `${JSON.stringify(manifest, null, 2)}\n`)
    },
  }
}
