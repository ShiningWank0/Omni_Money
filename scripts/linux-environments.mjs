import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = fileURLToPath(new URL('..', import.meta.url))
export const workflowPaths = ['ci.yml', 'release-desktop.yml', 'release-docker.yml', 'wails-dev-integration.yml']
  .map(name => `.github/workflows/${name}`)
export const capturePaths = ['.github/workflows/capture-build-environments.yml', 'docs/environment-snapshots.md',
  ...['ubuntu', 'alpine-builder', 'alpine-runtime'].map(name => `build/environments/${name}.Dockerfile`)]

function withoutVerifiedOfflineApkInstall(dockerfile, image) {
  const mount = `RUN --network=none --mount=type=bind,from=${image},source=/opt/omni-environment/apks,target=/run/omni-apks,ro`
  const verify = ' && apk verify "$@" \\'
  const install = ' && apk add --no-network --no-cache --repositories-file /dev/null "$@" \\'
  return dockerfile.split(/\n(?=[A-Z]+\s)/).map(instruction =>
    instruction.split('\n', 1)[0] === `${mount} \\`
      ? instruction.replace(`${verify}\n${install}`, verify)
      : instruction
  ).join('\n')
}

export function checkLinuxEnvironments(directory = root) {
  for (const path of capturePaths) assert.ok(readFileSync(join(directory, path), 'utf8').trim(), `${path}: environment capture input is empty`)
  const lock = JSON.parse(readFileSync(join(directory, 'build/locks/linux-environments.json'), 'utf8'))
  assert.equal(lock.schema, 1)
  const references = new Map()
  for (const name of ['ubuntu', 'alpine-builder', 'alpine-runtime']) {
    const item = lock.environments[name]
    assert.equal(item.environment, name)
    assert.deepEqual(item.platforms, ['linux/amd64', 'linux/arm64'])
    assert.match(item.image, new RegExp(`^ghcr.io/shiningwank0/omni_money-build-${name}@sha256:[a-f0-9]{64}$`))
    assert.match(item.sourceCommit, /^[a-f0-9]{40}$/)
    assert.match(item.runId, /^[1-9][0-9]*$/)
    references.set(item.image.split('@')[0], item.image)
  }
  const used = new Set()
  for (const path of ['Dockerfile', ...workflowPaths]) {
    const text = readFileSync(join(directory, path), 'utf8')
    const found = [...text.matchAll(/ghcr\.io\/shiningwank0\/omni_money-build-(?:ubuntu|alpine-builder|alpine-runtime)[a-z0-9@:.]*/g)]
    assert.ok(found.length, `${path}: frozen environment reference missing`)
    for (const [reference] of found) {
      assert.equal(reference, references.get(reference.split('@')[0]), `${path}: environment differs from reviewed lock`)
      used.add(reference.split('@')[0])
    }
    if (path.endsWith('.yml')) {
      for (const [, options] of text.matchAll(/^\s+options: (.+)$/gm)) {
        assert.match(options, /(?:^|\s)--init(?:\s|$)/, `${path}: job containers must reap orphaned child processes`)
      }
    }
    const packageResolution = path === 'Dockerfile'
      ? withoutVerifiedOfflineApkInstall(text, lock.environments['alpine-runtime'].image)
      : text
    assert.doesNotMatch(packageResolution, /\bapt-get\s+(?:update|install)|\bapk\s+(?:add|upgrade|update)|playwright install --with-deps/,
      `${path}: ordinary builds must not resolve OS packages again`)
  }
  assert.equal(used.size, 3)
  return lock
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try { checkLinuxEnvironments(); console.log('Linux build inputs agree with the reviewed immutable environment lock') }
  catch (error) { console.error(error.message); process.exitCode = 1 }
}
