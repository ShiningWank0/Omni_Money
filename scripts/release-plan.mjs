import { appendFileSync } from 'node:fs'
import { spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { resolve } from 'node:path'

export function releasePlan(event, paths, rehearsal = false) {
  if (event === 'workflow_dispatch') return { desktop: false, docker: rehearsal }
  if (event === 'schedule') return { desktop: false, docker: false }
  if (event === 'push') return {
    desktop: paths.some(path => path === 'VERSION' || path === '.github/workflows/release-desktop.yml'),
    docker: paths.includes('VERSION'),
  }
  if (event !== 'pull_request') throw new Error(`Unexpected pipeline event ${event}`)
  const shared = path => /^(licenses\/|scripts\/oss-|frontend\/|\.github\/workflows\/)/.test(path) || ['go.mod', 'go.sum', '.node-version', 'LICENSE'].includes(path)
  return {
    desktop: paths.some(path => shared(path) || path.endsWith('.go') || path.startsWith('build/') || /^scripts\/(build-(sqlcipher|openssl)-|wails-(native|dev)-)/.test(path) || path === 'wails.json'),
    docker: paths.some(path => shared(path) || path.startsWith('backend/') || ['Dockerfile', '.dockerignore', 'server.go'].includes(path) || path.startsWith('scripts/build-sqlcipher-')),
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const event = process.env.GITHUB_EVENT_NAME
    let paths = []
    if (event === 'push' || event === 'pull_request') {
      const base = event === 'push' ? process.env.BEFORE_SHA : process.env.BASE_SHA
      const head = process.env.HEAD_SHA
      if (!/^[a-f0-9]{40}$/.test(base || '') || !/^[a-f0-9]{40}$/.test(head || '')) throw new Error('Invalid immutable release-plan refs')
      const result = spawnSync('git', ['diff', '--name-only', '--no-renames', base, head], { encoding: 'utf8' })
      if (result.status !== 0) throw new Error('Cannot determine changed release inputs')
      paths = result.stdout.trim().split('\n').filter(Boolean)
    }
    const plan = releasePlan(event, paths, process.env.RELEASE_REHEARSAL === 'true')
    if (process.env.GITHUB_OUTPUT) appendFileSync(process.env.GITHUB_OUTPUT, Object.entries(plan).map(([key, value]) => `${key}=${value}\n`).join(''))
    console.log(JSON.stringify(plan))
  } catch (error) { console.error(error.message); process.exitCode = 1 }
}
