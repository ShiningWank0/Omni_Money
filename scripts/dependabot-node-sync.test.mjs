import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { applyNodeSync, githubRequest, planNodeSync } from './dependabot-node-sync.mjs'

const repository = 'ShiningWank0/Omni_Money'
const prefix = `/repos/${repository}`
const base = 'a'.repeat(40)
const head = 'b'.repeat(40)
const treeSHA = 'c'.repeat(40)
const blobSHA = 'd'.repeat(40)
const updatedTreeSHA = 'e'.repeat(40)
const updatedCommitSHA = 'f'.repeat(40)
const repo = { id: 1171756271, full_name: repository }
const branch = 'dependabot/docker/node-26.12.0-alpine'
const image = (version, digest) => `FROM node:${version}-alpine@sha256:${digest.repeat(64)} AS frontend-builder`

function fixture() {
  const state = {
    eventName: 'workflow_run',
    event: {
      action: 'requested', repository: { ...repo },
      workflow_run: {
        name: 'CI', event: 'pull_request', path: '.github/workflows/ci.yml',
        head_repository: { ...repo }, head_branch: branch, head_sha: head,
        pull_requests: [{ number: 264 }],
      },
    },
    pr: {
      state: 'open', draft: false, changed_files: 1,
      user: { id: 49699333, login: 'dependabot[bot]', type: 'Bot' },
      base: { ref: 'main', sha: base, repo: { ...repo } },
      head: { ref: branch, sha: head, repo: { ...repo } },
    },
    files: [{ filename: 'Dockerfile', status: 'modified' }],
    contents: {
      [`Dockerfile@${base}`]: `${image('26.11.1', '1')}\nRUN npm ci\n`,
      [`Dockerfile@${head}`]: `${image('26.12.0', '2')}\nRUN npm ci\n`,
      [`.node-version@${base}`]: '26.11.1\n',
      [`.node-version@${head}`]: '26.11.1\n',
      [`frontend/package.json@${base}`]: JSON.stringify({ engines: { node: '^24.19.0 || ^26.0.0' } }),
    },
    pinMode: '100644',
    calls: [],
    race: false,
  }
  state.request = async (method, path, body) => {
    state.calls.push({ method, path, body })
    if (method === 'GET') {
      if (path === `${prefix}/pulls/264`) return structuredClone(state.pr)
      if (path === `${prefix}/pulls/264/files?per_page=100`) return structuredClone(state.files)
      if (path === `${prefix}/git/commits/${head}`) return { tree: { sha: treeSHA } }
      if (path === `${prefix}/git/trees/${treeSHA}`) return { tree: [{ path: '.node-version', mode: state.pinMode, type: 'blob', sha: blobSHA }] }
      const contentPath = path.match(/^\/repos\/ShiningWank0\/Omni_Money\/contents\/(.+)\?ref=(.+)$/)
      if (contentPath) {
        const text = state.contents[`${contentPath[1]}@${contentPath[2]}`]
        assert.equal(typeof text, 'string', `unexpected fixture file ${path}`)
        return { type: 'file', encoding: 'base64', size: Buffer.byteLength(text), content: Buffer.from(text).toString('base64'), sha: blobSHA }
      }
    }
    if (method === 'POST' && path === `${prefix}/git/blobs`) return { sha: blobSHA }
    if (method === 'POST' && path === `${prefix}/git/trees`) return { sha: updatedTreeSHA }
    if (method === 'POST' && path === `${prefix}/git/commits`) return { sha: updatedCommitSHA }
    if (method === 'PATCH' && path === `${prefix}/git/refs/heads/${branch}`) {
      if (state.race) throw new Error('GitHub API PATCH failed (422); no forced update will be attempted')
      return { object: { sha: updatedCommitSHA } }
    }
    assert.fail(`unexpected API request ${method} ${path}`)
  }
  return state
}

test('a same-major Node Docker update plans a same-PR pin change without writes', async () => {
  const state = fixture()
  const result = await planNodeSync(state)
  assert.equal(result.eligible, true)
  assert.equal(result.version, '26.12.0')
  assert.equal(result.head, head)
  assert.ok(state.calls.every(call => call.method === 'GET'))
})

test('apply creates only the pin blob and tree entry, preserving the validated parent', async () => {
  const state = fixture()
  const result = await applyNodeSync(state)
  assert.equal(result.commit, updatedCommitSHA)
  const writes = state.calls.filter(call => call.method !== 'GET')
  assert.deepEqual(writes.map(call => call.body), [
    { content: '26.12.0\n', encoding: 'utf-8' },
    { base_tree: treeSHA, tree: [{ path: '.node-version', mode: '100644', type: 'blob', sha: blobSHA }] },
    { message: '[dependabot skip] chore: DockerのNode 26.12.0に固定版を同期', tree: updatedTreeSHA, parents: [head] },
    { sha: updatedCommitSHA, force: false },
  ])
})

test('patch updates are eligible without widening the supported Node ranges', async () => {
  const state = fixture()
  state.contents[`Dockerfile@${head}`] = state.contents[`Dockerfile@${head}`].replace('26.12.0', '26.11.2')
  assert.equal((await planNodeSync(state)).version, '26.11.2')
})

const rejected = {
  'fork repository': s => { s.event.repository.id = 99 },
  'fork head': s => { s.event.workflow_run.head_repository.full_name = 'other/fork' },
  'wrong event': s => { s.eventName = 'pull_request_target' },
  'unrelated workflow': s => { s.event.workflow_run.path = '.github/workflows/other.yml' },
  'non-PR run': s => { s.event.workflow_run.event = 'push' },
  'ambiguous PR association': s => { s.event.workflow_run.pull_requests.push({ number: 265 }) },
  'untrusted author': s => { s.pr.user.id = 1 },
  'untrusted branch': s => { s.event.workflow_run.head_branch = 'feature/change' },
  'wrong base branch': s => { s.pr.base.ref = 'release' },
  'closed PR': s => { s.pr.state = 'closed' },
  'draft PR': s => { s.pr.draft = true },
  'stale head': s => { s.pr.head.sha = '0'.repeat(40) },
  'extra changed files': s => { s.pr.changed_files = 3 },
  'unrelated changed file': s => { s.files.push({ filename: 'server.go', status: 'modified' }); s.pr.changed_files = 2 },
  'renamed Dockerfile': s => { s.files[0].status = 'renamed' },
  'extra Docker instruction': s => { s.contents[`Dockerfile@${head}`] += 'RUN unexpected-command\n' },
  'unpinned image': s => { s.contents[`Dockerfile@${head}`] = 'FROM node:26.12.0-alpine AS frontend-builder\nRUN npm ci\n' },
  'major update': s => { s.contents[`Dockerfile@${head}`] = `${image('27.0.0', '2')}\nRUN npm ci\n` },
  'downgrade': s => { s.contents[`Dockerfile@${head}`] = `${image('26.10.0', '2')}\nRUN npm ci\n` },
  'unchanged digest with new version': s => { s.contents[`Dockerfile@${head}`] = `${image('26.12.0', '1')}\nRUN npm ci\n` },
  'digest-only update': s => { s.contents[`Dockerfile@${head}`] = `${image('26.11.1', '2')}\nRUN npm ci\n` },
  'already synchronized pin': s => { s.contents[`.node-version@${head}`] = '26.12.0\n'; s.files.push({ filename: '.node-version', status: 'modified' }); s.pr.changed_files = 2 },
  'manually changed pin': s => { s.contents[`.node-version@${head}`] = '26.11.2\n' },
  'inconsistent base pin': s => { s.contents[`.node-version@${base}`] = '24.19.0\n' },
  'unsupported Node range': s => { s.contents[`frontend/package.json@${base}`] = '{"engines":{"node":"^24.19.0"}}' },
  'unrecognized Node range syntax': s => { s.contents[`frontend/package.json@${base}`] = '{"engines":{"node":">=26"}}' },
}

for (const [name, change] of Object.entries(rejected)) {
  test(`${name} never writes to a branch`, async () => {
    const state = fixture()
    change(state)
    assert.equal((await applyNodeSync(state)).eligible, false)
    assert.ok(state.calls.every(call => call.method === 'GET'))
  })
}

test('a head changed after inspection is revalidated before creating any blob', async () => {
  const state = fixture()
  assert.equal((await planNodeSync(state)).eligible, true)
  state.pr.head.sha = '0'.repeat(40)
  assert.equal((await applyNodeSync(state)).eligible, false)
  assert.ok(state.calls.every(call => call.method === 'GET'))
})

test('a symlink or executable pin is rejected before any write', async () => {
  for (const mode of ['120000', '100755']) {
    const state = fixture()
    state.pinMode = mode
    await assert.rejects(applyNodeSync(state), /regular non-executable/)
    assert.ok(state.calls.every(call => call.method === 'GET'))
  }
})

test('a concurrent update rejects the final non-forced ref update without a retry', async () => {
  const state = fixture()
  state.race = true
  await assert.rejects(applyNodeSync(state), /422.*no forced update/)
  const updates = state.calls.filter(call => call.method === 'PATCH')
  assert.equal(updates.length, 1)
  assert.equal(updates[0].body.force, false)
})

test('GitHub transport fixes the API origin, forbids redirects and reports HTTP failures', async () => {
  const calls = []
  const request = githubRequest('test-token', async (url, options) => {
    calls.push({ url, options })
    return { ok: false, status: 403 }
  })
  await assert.rejects(request('GET', `${prefix}/pulls/264`), /403/)
  assert.equal(calls[0].url, `https://api.github.com${prefix}/pulls/264`)
  assert.equal(calls[0].options.redirect, 'error')
  await assert.rejects(request('GET', 'https://other.example/'), /Unexpected repository/)
  assert.equal(calls.length, 1)
})

test('workflow executes trusted code with an explicitly scoped App token only after inspection', () => {
  const workflow = readFileSync(new URL('../.github/workflows/dependabot-node-sync.yml', import.meta.url), 'utf8')
  assert.match(workflow, /workflow_run:\n    workflows: \[CI\]\n    types: \[requested\]/)
  assert.match(workflow, /ref: \$\{\{ github.workflow_sha \}\}\n          persist-credentials: false/)
  assert.match(workflow, /repositories: \$\{\{ github.event.repository.name \}\}/)
  assert.match(workflow, /permission-contents: write\n          permission-pull-requests: read/)
  assert.match(workflow, /NODE_SYNC_APP_CLIENT_ID/)
  assert.match(workflow, /NODE_SYNC_APP_PRIVATE_KEY/)
  assert.match(workflow, /steps.config.outputs.configured == 'true'/)
  assert.ok(workflow.indexOf('node scripts/dependabot-node-sync.mjs inspect') < workflow.indexOf('uses: actions/create-github-app-token@'))
  assert.doesNotMatch(workflow, /pull_request_target:|head\.sha|download-artifact|npm (ci|install)|^\s+contents: write|permission-workflows/m)
})
