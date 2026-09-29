import { readFileSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const versionPattern = '(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)'

function singleMatch(text, pattern, description) {
  const matches = [...text.matchAll(pattern)]
  if (matches.length !== 1) throw new Error(`Expected exactly one ${description}`)
  return matches[0]
}

function compareVersions(a, b) {
  const left = a.split('.').map(Number)
  const right = b.split('.').map(Number)
  for (let i = 0; i < 3; i++) {
    if (left[i] !== right[i]) return Math.sign(left[i] - right[i])
  }
  return 0
}

export function readRuntimeVersions(root = repositoryRoot) {
  const goMod = readFileSync(join(root, 'go.mod'), 'utf8')
  const dockerfile = readFileSync(join(root, 'Dockerfile'), 'utf8')
  const nodeFile = readFileSync(join(root, '.node-version'), 'utf8')
  const toolchain = singleMatch(goMod, /^toolchain go(\d+\.\d+\.\d+)$/gm, 'Go toolchain directive')[1]
  const minimum = singleMatch(goMod, /^go (\d+\.\d+(?:\.\d+)?)$/gm, 'minimum Go version')[1]
  const node = singleMatch(nodeFile, new RegExp(`^(${versionPattern})\\r?\\n?$`, 'g'), 'pinned Node version')[1]
  const imageVersion = image => singleMatch(dockerfile,
    new RegExp(`^FROM ${image}:(${versionPattern})-alpine@sha256:[a-f0-9]{64} AS [a-z-]+\\r?$`, 'gm'),
    `digest-pinned ${image} builder`)[1]
  const dockerGo = imageVersion('golang')
  const dockerNode = imageVersion('node')
  const minimumGo = minimum.split('.').length === 2 ? `${minimum}.0` : minimum
  if (compareVersions(dockerGo, minimumGo) < 0) {
    throw new Error(`Docker Go ${dockerGo} is older than go.mod minimum ${minimumGo}`)
  }
  return { goMod, toolchain, node, dockerGo, dockerNode }
}

export function checkRuntimeVersions(root = repositoryRoot) {
  const versions = readRuntimeVersions(root)
  const mismatches = []
  if (versions.toolchain !== versions.dockerGo) mismatches.push('Go toolchain / Docker')
  if (versions.node !== versions.dockerNode) mismatches.push('Node version file / Docker')
  if (mismatches.length) {
    throw new Error(`${mismatches.join(', ')} mismatch. Review the Docker update, then run node scripts/runtime-versions.mjs sync and commit the related files in the same PR.`)
  }
  return versions
}

// Docker PRs already carry the new tag and digest. Synchronize only the build
// pins, preserving the minimum supported Go version and package engine ranges.
export function syncRuntimeVersions(root = repositoryRoot) {
  const versions = readRuntimeVersions(root)
  if (versions.toolchain !== versions.dockerGo) {
    writeFileSync(join(root, 'go.mod'), versions.goMod.replace(
      /^toolchain go\d+\.\d+\.\d+$/m, `toolchain go${versions.dockerGo}`))
  }
  if (versions.node !== versions.dockerNode) {
    writeFileSync(join(root, '.node-version'), `${versions.dockerNode}\n`)
  }
  return checkRuntimeVersions(root)
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const command = process.argv[2] || 'check'
    if (!['check', 'sync'].includes(command)) throw new Error('Usage: runtime-versions.mjs [check|sync]')
    const versions = command === 'sync' ? syncRuntimeVersions() : checkRuntimeVersions()
    console.log(`Runtime pins agree: Go ${versions.toolchain}, Node ${versions.node}`)
  } catch (error) {
    console.error(error.message)
    process.exitCode = 1
  }
}
