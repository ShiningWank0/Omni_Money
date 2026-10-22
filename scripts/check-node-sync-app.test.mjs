import assert from 'node:assert/strict'
import { generateKeyPairSync } from 'node:crypto'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { assertManualMainContext, checkNodeSyncAppAccess, readOnlyGitHubRequest, validateAppCredentials } from './check-node-sync-app.mjs'

const repository = 'ShiningWank0/Omni_Money'
const prefix = `/repos/${repository}`
const ref = 'a'.repeat(40)
const privateKey = generateKeyPairSync('rsa', {
  modulusLength: 2048,
  privateKeyEncoding: { type: 'pkcs1', format: 'pem' },
  publicKeyEncoding: { type: 'spki', format: 'pem' },
}).privateKey

test('manual check rejects automatic events, other repositories and non-main refs', () => {
  const env = { GITHUB_EVENT_NAME: 'workflow_dispatch', GITHUB_REPOSITORY: repository, GITHUB_REF: 'refs/heads/main' }
  assert.doesNotThrow(() => assertManualMainContext(env))
  for (const change of [{ GITHUB_EVENT_NAME: 'push' }, { GITHUB_EVENT_NAME: 'workflow_run' },
    { GITHUB_REPOSITORY: 'other/fork' }, { GITHUB_REF: 'refs/heads/feature' }]) {
    assert.throws(() => assertManualMainContext({ ...env, ...change }), /手動実行専用/)
  }
})

test('credentials accept PEM and escaped newlines without returning private material', () => {
  assert.equal(validateAppCredentials({ clientID: 'test-client-id', privateKey }), undefined)
  assert.equal(validateAppCredentials({ clientID: 'test-client-id', privateKey: privateKey.replace(/\n/g, '\\n') }), undefined)
})

test('missing settings and a clipboard command are rejected without echoing values', () => {
  assert.throws(() => validateAppCredentials({ privateKey }), /NODE_SYNC_APP_CLIENT_ID/)
  assert.throws(() => validateAppCredentials({ clientID: 'test-client-id', privateKey: ' ' }), /NODE_SYNC_APP_PRIVATE_KEY/)
  const clipboardCommand = '(\n  umask 077\n  pbpaste > "$HOME/Downloads/bitwarden-restored.pem"\n)'
  assert.equal(Buffer.byteLength(clipboardCommand), 68)
  for (const invalid of [clipboardCommand, privateKey.replace(/\n/g, ''), 'not-a-secret-key']) {
    assert.throws(() => validateAppCredentials({ clientID: 'test-client-id', privateKey: invalid }), error => {
      assert.match(error.message, /RSA秘密鍵を読み込めません/)
      assert.ok(!error.message.includes(invalid))
      return true
    })
  }
})

test('a non-RSA private key is rejected', () => {
  const ec = generateKeyPairSync('ec', { namedCurve: 'prime256v1', privateKeyEncoding: { type: 'pkcs8', format: 'pem' }, publicKeyEncoding: { type: 'spki', format: 'pem' } })
  assert.throws(() => validateAppCredentials({ clientID: 'test-client-id', privateKey: ec.privateKey }), /RSA秘密鍵を登録/)
})

function fixture() {
  const replies = {
    '/installation/repositories?per_page=100': { total_count: 1, repositories: [{ id: 1171756271, full_name: repository }] },
    [`${prefix}/contents/.node-version?ref=${ref}`]: { type: 'file', encoding: 'base64', size: 8, content: Buffer.from('26.12.1\n').toString('base64') },
    [`${prefix}/pulls?state=open&per_page=1`]: [],
  }
  const calls = []
  return { replies, calls, request: async path => { calls.push(path); return structuredClone(replies[path]) } }
}

test('read-only access succeeds with no open PR and checks one immutable revision', async () => {
  const state = fixture()
  assert.deepEqual(await checkNodeSyncAppAccess({ ref, request: state.request }), { nodeVersion: '26.12.1' })
  assert.deepEqual(state.calls, Object.keys(state.replies))
})

test('wrong or overbroad token scope is rejected before accessing repository data', async () => {
  for (const installation of [null, { total_count: 0, repositories: [] },
    { total_count: 2, repositories: [{ id: 1171756271, full_name: repository }] },
    { total_count: 1, repositories: [{ id: 999, full_name: repository }] },
    { total_count: 1, repositories: [{ id: 1171756271, full_name: 'other/repo' }] }]) {
    const state = fixture()
    state.replies['/installation/repositories?per_page=100'] = installation
    await assert.rejects(checkNodeSyncAppAccess({ ref, request: state.request }), /Omni_Moneyだけ/)
    assert.equal(state.calls.length, 1)
  }
})

test('invalid revision, malformed contents and a failed PR read do not pass', async () => {
  const state = fixture()
  await assert.rejects(checkNodeSyncAppAccess({ ref: 'main', request: state.request }), /コミットが不正/)
  assert.equal(state.calls.length, 0)
  const filePath = `${prefix}/contents/.node-version?ref=${ref}`
  for (const change of [{ type: 'symlink' }, { encoding: 'none' }, { size: 65 }, { content: Buffer.from('not-a-version').toString('base64') }]) {
    const changed = fixture()
    Object.assign(changed.replies[filePath], change)
    await assert.rejects(checkNodeSyncAppAccess({ ref, request: changed.request }))
  }
  state.replies[`${prefix}/pulls?state=open&per_page=1`] = { message: 'failure' }
  await assert.rejects(checkNodeSyncAppAccess({ ref, request: state.request }), /Pull requests/)
})

test('transport makes only allowlisted GET requests and forbids redirects', async () => {
  const calls = []
  const request = readOnlyGitHubRequest('test-token', async (url, options) => {
    calls.push({ url, options })
    return { ok: true, json: async () => ({}) }
  })
  await request('/installation/repositories?per_page=100')
  await request(`${prefix}/contents/.node-version?ref=${ref}`)
  for (const path of ['https://other.example/', '/repos/other/repo/pulls', `${prefix}/git/refs/heads/main`, `${prefix}/contents/.node-version?ref=main`]) {
    await assert.rejects(request(path), /許可されていない/)
  }
  assert.equal(calls.length, 2)
  assert.ok(calls.every(({ url, options }) => url.startsWith('https://api.github.com/') && options.method === 'GET' && options.redirect === 'error' && !options.body))
})

test('HTTP, network and JSON failures never echo token, response or exception contents', async () => {
  for (const fetchImpl of [
    async () => ({ ok: false, status: 403, json: async () => ({ message: 'test-token' }) }),
    async () => { throw new Error('test-token') },
    async () => ({ ok: true, json: async () => { throw new Error('test-token') } }),
  ]) {
    await assert.rejects(readOnlyGitHubRequest('test-token', fetchImpl)('/installation/repositories?per_page=100'), error => !error.message.includes('test-token'))
  }
  assert.throws(() => readOnlyGitHubRequest(''), /Appトークン/)
})

test('workflow is dispatch-only, main-only and uses a pinned scoped token action', () => {
  const workflow = readFileSync(new URL('../.github/workflows/check-node-sync-app.yml', import.meta.url), 'utf8')
  assert.match(workflow, /on:\n  workflow_dispatch:\n\npermissions:/)
  assert.match(workflow, /github\.ref == 'refs\/heads\/main'/)
  assert.match(workflow, /ref: \$\{\{ github.workflow_sha \}\}\n          persist-credentials: false/)
  assert.match(workflow, /permission-contents: write\n          permission-pull-requests: read/)
  assert.match(workflow, /repositories: \$\{\{ github.event.repository.name \}\}/)
  assert.ok(workflow.indexOf('check-node-sync-app.mjs credentials') < workflow.indexOf('uses: actions/create-github-app-token@'))
  assert.doesNotMatch(workflow, /pull_request:|workflow_run:|schedule:|^\s+contents: write|npm (ci|install)|dependabot-node-sync\.mjs apply|git (push|commit)/m)
  assert.match(workflow, /if: failure\(\)/)
})
