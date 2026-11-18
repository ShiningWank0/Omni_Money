import { createHash } from 'node:crypto'
import { existsSync, readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'

const walk = path => !existsSync(path) ? [] : readdirSync(path, { withFileTypes: true }).flatMap(entry => entry.isDirectory()
  ? walk(join(path, entry.name)) : [join(path, entry.name)])
const paths = ['go.mod', 'go.sum', 'scripts/buildtools/wails/go.mod', 'scripts/buildtools/wails/go.sum',
  'licenses/build/review.json', ...walk('frontend/bindings')]
for (const path of paths.filter(existsSync)) {
  const bytes = readFileSync(path), encoded = bytes.toString('base64')
  console.log('OMNI_FILE ' + JSON.stringify({ path, sha256: createHash('sha256').update(bytes).digest('hex'), bytes: bytes.length }))
  for (let index = 0; index < encoded.length; index += 8192) console.log('OMNI_CHUNK ' + JSON.stringify({ path, offset: index, data: encoded.slice(index, index + 8192) }))
}
