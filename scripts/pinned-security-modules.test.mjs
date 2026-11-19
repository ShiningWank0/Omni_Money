import assert from 'node:assert/strict'
import test from 'node:test'
import { auditModules, moduleQueries, osvBatch } from './pinned-security-modules.mjs'

const raw = JSON.stringify({ Path: 'private/app', Main: true }) + '\n' + JSON.stringify({ Path: 'example.org/lib', Version: 'v1.2.3' })
test('OSV queries use the selected MVS graph rather than historical go.sum entries', () => {
  assert.deepEqual(moduleQueries(raw), [{ package: { name: 'example.org/lib', ecosystem: 'Go' }, version: 'v1.2.3' }])
  for (const value of ['', '{', JSON.stringify({ Path: 'x', Error: 'failed' }), JSON.stringify({ Path: 'x', Version: 'v1', Replace: {} })]) assert.throws(() => moduleQueries(value))
})

test('OSV findings are retained even for unreachable modules and incomplete batches fail', () => {
  const queries = moduleQueries(raw)
  assert.equal(osvBatch({ results: [{ vulns: [{ id: 'GO-2099-1' }] }] }, queries)[0].version, 'v1.2.3')
  for (const value of [{ results: [] }, { results: [{ next_page_token: 'more' }] }, { results: [{ vulns: [{}] }] }]) assert.throws(() => osvBatch(value, queries))
})

test('every module graph and Go standard library is attempted after another graph fails', async () => {
  const input = { modules: [{ path: 'go.mod' }, { path: 'scripts/tool.mod' }], remoteModules: [{ module: 'example.org/tool', version: 'v1.0.0' }], goVersion: '1.27.4' }
  const calls = []
  const result = await auditModules(input, (args, cwd) => {
    calls.push({ args, cwd })
    if (args.includes('-modfile=/private/go.mod')) throw new Error('fixture failure')
    if (args[0] === 'mod') return JSON.stringify({ Dir: '/cache/example.org/tool@v1.0.0' })
    return raw
  }, async () => [], '/private')
  assert.ok(result[0].error)
  assert.equal(result[1].name, 'scripts/tool.mod')
  assert.equal(result[2].name, 'example.org/tool@v1.0.0')
  assert.equal(result[3].queries[0].package.name, 'stdlib')
  assert.ok(calls.some(x => x.cwd === '/cache/example.org/tool@v1.0.0'))
  assert.ok(calls.filter(x => x.args[0] === 'list').every(x => x.args.includes('-mod=readonly')))
})
