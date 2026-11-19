import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { isAbsolute, relative, resolve } from 'node:path'
assert.equal(process.env.GITHUB_ACTIONS, 'true', 'CI diagnostics only')
assert.equal(process.platform, 'linux')
const destination = process.env.OMNI_CI_BROWSER_BINARY
const base = process.env.RUNNER_TEMP
assert.ok(base && destination && isAbsolute(destination))
const path = relative(resolve(base), resolve(destination))
assert.ok(path && !path.startsWith('..') && !isAbsolute(path), 'Use a private RUNNER_TEMP output')
const result = spawnSync('go', ['build', '-mod=readonly', '-tags',
  'server,omni_wails_browser,dev,desktop,gtk3,libsqlite3,sqlite_omit_load_extension', '-o', destination, '.'], { stdio: 'inherit' })
assert.equal(result.status, 0, String(result.error || 'Headless binding host build failed'))
