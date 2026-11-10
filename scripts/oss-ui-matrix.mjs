import { spawnSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

// Build and inspect the real public license dialog for every supported release
// configuration. Browsers use a local static preview, with no backend or vault.
if (process.env.GITHUB_ACTIONS !== 'true' || process.platform !== 'linux') throw new Error('The OSS browser matrix is CI Linux only')
const frontend = fileURLToPath(new URL('../frontend/', import.meta.url))
const targets = JSON.parse(readFileSync(new URL('../licenses/targets.json', import.meta.url), 'utf8'))
for (const target of targets) {
  console.log(`::group::OSS display ${target.id}`)
  const env = { ...process.env, VITE_OSS_TARGET: target.id, OSS_TEST_TARGET: target.id }
  for (const [command, args] of [['npm', ['run', 'build']], ['npx', ['--no-install', 'playwright', 'test', '--config', 'playwright.licenses.config.js']]]) {
    const result = spawnSync(command, args, { cwd: frontend, env, stdio: 'inherit', timeout: 180_000 })
    if (result.status !== 0) throw new Error(`OSS display ${target.id}: ${command} failed`)
  }
  console.log('::endgroup::')
}
