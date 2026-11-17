import { spawnSync } from 'node:child_process'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

export function govulncheckCommand(...args) {
  return ['go', 'run', '-mod=readonly',
    `-modfile=${fileURLToPath(new URL('./securitytools/govulncheck/go.mod', import.meta.url))}`,
    'golang.org/x/vuln/cmd/govulncheck', ...args]
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const [program, ...args] = govulncheckCommand(...process.argv.slice(2))
  const result = spawnSync(program, args, { stdio: 'inherit', timeout: 20 * 60 * 1000, shell: false })
  if (result.error) console.error(result.error.message)
  process.exitCode = result.status ?? 1
}
