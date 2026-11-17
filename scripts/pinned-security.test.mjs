import assert from 'node:assert/strict'
import { existsSync, mkdtempSync, mkdirSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import test from 'node:test'
import { groups, inputFiles, inventory, root } from './pinned-security-inventory.mjs'
import { attempt, expectedNames, get, grypeReport, publishReports, releaseUpdates, renderPinned,
  reportEntries, scanEnvironment, scanImages, scanTools, trivyReport, validateReport } from './pinned-security.mjs'
import { publish } from './security-issues.mjs'

function fixture(t) {
  const folder = mkdtempSync(join(tmpdir(), 'pinned-security-test-'))
  t.after(() => rmSync(folder, { recursive: true, force: true }))
  const files = [...inputFiles, 'go.mod', 'go.sum', 'scripts/security-reports.mjs', '.github/workflows/pinned-input-security.yml']
  for (const path of files) {
    mkdirSync(dirname(join(folder, path)), { recursive: true })
    writeFileSync(join(folder, path), readFileSync(join(root, path)))
  }
  return folder
}
const runURL = 'https://github.com/ShiningWank0/Omni_Money/actions/runs/123/attempts/1'
const bot = { login: 'github-actions[bot]' }
const finding = { tool: 'pinned-trivy', id: 'CVE-2099-1234', component: 'ubuntu/amd64/openssl', installed: '1.0-1',
  fixed: '', severity: 'LOW', context: 'immutable digest', summary: 'fixture' }

test('inventory includes OS images, both architectures, Go tools and reviewed native versions', () => {
  const input = inventory()
  assert.ok(input.images.some(x => x.name === 'ubuntu/amd64') && input.images.some(x => x.name === 'ubuntu/arm64'))
  assert.ok(input.tools.some(x => x.name === 'engine/x86_64') && input.tools.some(x => x.name === 'engine/aarch64'))
  assert.ok(input.native.some(x => x.name === 'openssl'))
  assert.ok(input.advisories.some(x => x.name === 'webview2'))
  assert.ok(input.modules.some(x => /gosec/.test(x.path)))
  assert.ok(input.remoteModules.some(x => x.module === 'golang.org/x/vuln') || input.modules.some(x => /govulncheck/.test(x.path)))
  assert.equal(new Set(input.images.map(x => x.name)).size, input.images.length)
})

test('source version mismatches and newly unregistered locks fail inventory validation', t => {
  const folder = fixture(t)
  const initial = inventory(folder)
  const path = join(folder, 'scripts/build-openssl-windows.sh')
  const original = readFileSync(path, 'utf8')
  writeFileSync(path, original.replace(/OPENSSL_VERSION="[^"]+"/, 'OPENSSL_VERSION="0.0.0"'))
  assert.throws(() => inventory(folder), /source versions disagree/)
  writeFileSync(path, original)
  assert.equal(inventory(folder).identity, initial.identity)
  writeFileSync(join(folder, 'build/locks/new-tool.json'), '{"schema":1}')
  assert.throws(() => inventory(folder), /unregistered fixed input/)
})

test('a newly added tool module is discovered automatically and requires its checksum file', t => {
  const folder = fixture(t), path = 'scripts/securitytools/new/go.mod'
  mkdirSync(dirname(join(folder, path)), { recursive: true })
  writeFileSync(join(folder, path), 'module example.org/tool\n\ngo 1.27.4\n')
  assert.throws(() => inventory(folder), /checksum file missing/)
  writeFileSync(join(folder, path.replace('.mod', '.sum')), '')
  assert.ok(inventory(folder).modules.some(x => x.path === path))
})

test('new Dockerfile base images are automatically inventoried and moving tags are rejected', t => {
  const folder = fixture(t), path = join(folder, 'Dockerfile'), original = readFileSync(path, 'utf8')
  const initial = inventory(folder).images.length
  writeFileSync(path, original + `\nFROM example.com/tool@sha256:${'a'.repeat(64)} AS audit-tool\n`)
  assert.equal(inventory(folder).images.length, initial + 2)
  writeFileSync(path, original + '\nFROM example.com/tool:latest AS audit-tool\n')
  assert.throws(() => inventory(folder))
})

test('Trivy retains low severity and unfixed CVEs, and rejects absent binary/OS inventories', () => {
  const raw = { SchemaVersion: 2, Metadata: { OS: { Family: 'ubuntu' } }, Results: [{ Type: 'ubuntu', Packages: [{ Name: 'openssl' }],
    Vulnerabilities: [{ VulnerabilityID: finding.id, PkgName: 'openssl', InstalledVersion: '1.0-1', Severity: 'LOW' }] }] }
  const values = trivyReport(raw, { name: 'ubuntu/amd64', ref: 'digest' }, 'image')
  assert.equal(values[0].severity, 'LOW'); assert.equal(values[0].fixed, '')
  assert.throws(() => trivyReport({ ...raw, Results: [] }, { name: 'ubuntu/amd64' }, 'image'), /inventory/)
  assert.throws(() => trivyReport(raw, { name: 'tool' }, 'rootfs'), /Go binary/)
  assert.throws(() => trivyReport({ ...raw, Metadata: {} }, { name: 'ubuntu/amd64' }, 'image'), /OS inventory/)
})

test('static tool scans enable binary analysis and fail on missing inventories without executing payloads', async t => {
  const folder = fixture(t), calls = []
  const input = { tools: [{ name: 'complete' }, { name: 'empty' }, { name: 'not-go' }] }
  const result = await scanTools(input, (program, args) => {
    calls.push({ program, args })
    const name = args.at(-1).split('/').at(-1)
    const raw = { SchemaVersion: 2 }
    if (name !== 'empty') raw.Results = [{ Type: name === 'complete' ? 'gobinary' : 'gomod', Packages: [{}],
      Vulnerabilities: [{ VulnerabilityID: finding.id, PkgName: 'stdlib', InstalledVersion: '1.0', Severity: 'LOW' }] }]
    writeFileSync(args[args.indexOf('--output') + 1], JSON.stringify(raw))
  }, folder, folder, async item => {
    const path = join(folder, item.name.slice('audit-'.length)); mkdirSync(path); return path
  })
  assert.deepEqual(result.map(x => x.status), ['findings', 'error', 'error'])
  assert.equal(result[0].findings[0].id, finding.id)
  assert.ok(calls.every(x => x.program === 'trivy' && x.args[0] === 'rootfs'))
  assert.ok(calls.every(x => x.args.includes('--list-all-pkgs') && x.args.includes('/dev/null')))
  assert.ok(input.tools.every(x => !existsSync(join(folder, x.name))))
})

test('Grype matches retain installed package revisions and reject incomplete results', () => {
  const raw = { source: { type: 'cyclonedx-json' }, descriptor: { name: 'grype' }, matches: [{
    artifact: { name: 'openssl', version: '1.0', locations: [{ path: 'msys2/pkg' }] }, vulnerability: { id: finding.id, severity: 'High', fix: { versions: ['1.1'] } },
  }] }
  const result = grypeReport(raw, 'msys2', new Map([['msys2/pkg', { name: 'pkg', installed: '1.0-2' }]]))
  assert.equal(result[0].installed, '1.0-2'); assert.equal(result[0].component, 'msys2/pkg')
  assert.throws(() => grypeReport({ matches: [] }, 'msys2'), /incomplete/)
})

test('every target is attempted even when another image fails', async t => {
  t.mock.method(console, 'error', () => {})
  const folder = fixture(t), calls = []
  const result = await scanImages({ images: [{ name: 'one', ref: 'a' }, { name: 'two', ref: 'b' }] }, (_, args) => {
    calls.push(args.at(-1))
    if (args.at(-1) === 'a') throw new Error('scan failure')
    writeFileSync(args[args.indexOf('--output') + 1], JSON.stringify({ SchemaVersion: 2, Results: [{ Type: 'gobinary', Packages: [{}] }] }))
  }, folder)
  assert.deepEqual(calls, ['a', 'b']); assert.equal(result[0].status, 'error'); assert.equal(result[1].status, 'clean')
})

test('release notices preserve OpenSSL patch-series selection and distinguish newer versions from CVEs', () => {
  const source = { name: 'openssl', version: '3.5.9', pattern: '^openssl-(3\\.5\\.[0-9]+)$' }
  const releases = [{ tag_name: 'openssl-4.1.1' }, { tag_name: 'openssl-3.5.10' }, { tag_name: 'openssl-3.5.11', prerelease: true }]
  assert.equal(releaseUpdates(releases, source)[0].latest, '3.5.10')
  assert.deepEqual(releaseUpdates([{ tag_name: 'openssl-3.5.9' }], source), [])
  assert.throws(() => releaseUpdates([], source), /empty/)
})

test('a clean report cannot omit targets, findings, coverage gaps, reviews or input identity', () => {
  const input = inventory(), group = 'images'
  const report = { schema: 1, group, inputIdentity: input.identity, results: expectedNames(input, group).map(name => ({ name, status: 'clean', findings: [] })) }
  validateReport(report, input, group)
  for (const mutate of [r => r.results.pop(), r => r.inputIdentity = 'wrong', r => r.results[0].findings.push(finding),
    r => r.results[0].gaps = ['unknown'], r => r.results[0].reviews = ['needs review']]) {
    const changed = structuredClone(report); mutate(changed)
    assert.throws(() => validateReport(changed, input, group))
  }
})

test('vulnerabilities, updates, coverage gaps and scanner errors use distinct stable issue identities', async () => {
  const failed = await attempt('failed', () => { throw new Error('fixture') })
  const reports = [{ group: 'images', results: [failed, { name: 'image', status: 'partial', findings: [finding], gaps: ['unmapped'], reviews: ['unknown CNA range'],
    updates: [{ name: 'tool', installed: '1.0', latest: '1.1', summary: 'new version' }] }] }]
  const entries = reportEntries(reports)
  assert.deepEqual(entries.map(x => x.data.kind).sort(), ['coverage', 'review', 'scan-error', 'update', 'vulnerability'])
  assert.deepEqual(reportEntries(structuredClone(reports)), entries)
  assert.equal(new Set(entries.map(x => x.key)).size, 5)
})

test('rendered upstream data cannot inject mentions, links or workflow content into issues', () => {
  const entries = reportEntries([{ group: 'native', results: [{ name: 'msys2', status: 'partial', findings: [], gaps: ['@everyone [x](https://evil.invalid) <script>\nline'] }] }])
  const view = renderPinned(entries[0], runURL)
  assert.ok(!view.body.includes('@everyone')); assert.ok(!view.body.includes('<script>')); assert.ok(!view.body.includes('[x]('))
  assert.ok(view.body.includes(view.marker) && view.body.includes(view.revision))
})

test('publisher cannot write from PRs, a fork, a feature branch or an unapproved event', async () => {
  const base = { GITHUB_EVENT_NAME: 'schedule', GITHUB_REF: 'refs/heads/main', GITHUB_REPOSITORY: 'ShiningWank0/Omni_Money' }
  for (const patch of [{ GITHUB_EVENT_NAME: 'pull_request' }, { GITHUB_REF: 'refs/heads/feature' }, { GITHUB_REPOSITORY: 'attacker/fork' }]) {
    let calls = 0
    await assert.rejects(publishReports({ ...base, ...patch }, () => { calls++; return () => {} }), /requires/)
    assert.equal(calls, 0)
  }
})

test('failed artifact download generates separate error issues and fails after reporting', async () => {
  const writes = []
  const api = async (method, path, body) => { if (method === 'GET') return []; writes.push({ path, body }); return {} }
  await assert.rejects(publishReports({ GITHUB_EVENT_NAME: 'schedule', GITHUB_REF: 'refs/heads/main', GITHUB_REPOSITORY: 'ShiningWank0/Omni_Money',
    GITHUB_RUN_ID: '123', GITHUB_RUN_ATTEMPT: '1', GITHUB_TOKEN: 'fixture', PINNED_TEST_OUTCOME: 'success', PINNED_REPORT_DOWNLOAD_OUTCOME: 'failure' }, () => api), /could not be fully checked/)
  assert.equal(writes.length, groups.length)
  assert.ok(writes.every(x => x.path === 'issues' && x.body.title.includes('検査エラー')))
})

test('unchanged closed issues stay quiet; changed advisories create a new issue without reopening', async () => {
  const entry = reportEntries([{ group: 'images', results: [{ name: 'image', status: 'findings', findings: [finding] }] }])[0]
  const view = renderPinned(entry, runURL), writes = []
  const api = async (method, path, body) => {
    if (method === 'GET' && path.startsWith('issues?')) return [{ number: 7, state: 'closed', user: bot, body: view.body }]
    if (method === 'GET') return []
    writes.push({ method, path, body }); return {}
  }
  assert.equal((await publish(api, [entry], runURL, renderPinned, { respectClosed: true })).unchanged, 1)
  assert.equal(writes.length, 0)
  const changed = { ...entry, digest: 'changed' }
  await publish(api, [changed], runURL, renderPinned, { respectClosed: true })
  assert.equal(writes.length, 1); assert.equal(writes[0].path, 'issues'); assert.ok(writes[0].body.body.includes('#7'))
})

test('download authorization is restricted to the GitHub API and non-HTTPS is rejected', async () => {
  const calls = [], fetcher = async (url, options) => {
    calls.push(options.headers)
    return { ok: true, url, body: [new Uint8Array([123, 125])] }
  }
  await get('https://api.github.com/repos/owner/repo/releases', { GITHUB_TOKEN: 'token' }, fetcher)
  await get('https://example.org/advisory', { GITHUB_TOKEN: 'token' }, fetcher)
  assert.equal(calls[0].Authorization, 'Bearer token'); assert.equal(calls[1].Authorization, undefined)
  await assert.rejects(get('http://example.org', {}, fetcher))
})

test('GHCR credentials are private and never supplied globally to other registries', t => {
  const folder = fixture(t)
  const env = scanEnvironment(folder, { GITHUB_ACTOR: 'fixture', GITHUB_TOKEN: 'secret', TRIVY_USERNAME: 'wrong', TRIVY_PASSWORD: 'wrong' })
  assert.equal(env.TRIVY_USERNAME, undefined); assert.equal(env.TRIVY_PASSWORD, undefined)
  const path = join(env.DOCKER_CONFIG, 'config.json'), config = JSON.parse(readFileSync(path, 'utf8'))
  assert.deepEqual(Object.keys(config.auths), ['ghcr.io'])
  assert.equal(statSync(path).mode & 0o777, 0o600)
  assert.equal(Buffer.from(config.auths['ghcr.io'].auth, 'base64').toString(), 'fixture:secret')
})

test('inventory failures themselves create an issue instead of preventing notification', async () => {
  const writes = []
  const api = async (method, path, body) => { if (method === 'GET') return []; writes.push({ path, body }); return {} }
  await assert.rejects(publishReports({ GITHUB_EVENT_NAME: 'workflow_dispatch', GITHUB_REF: 'refs/heads/main', GITHUB_REPOSITORY: 'ShiningWank0/Omni_Money',
    GITHUB_RUN_ID: '123', GITHUB_RUN_ATTEMPT: '1', GITHUB_TOKEN: 'fixture', PINNED_TEST_OUTCOME: 'success' }, () => api, () => { throw new Error('unregistered input') }), /inventory cannot/)
  assert.equal(writes.length, 1); assert.ok(writes[0].body.title.includes('inventory/configuration'))
})

test('failed validation does not silence notification of an unexecuted security scan', async () => {
  const writes = []
  const api = async (method, path, body) => { if (method === 'GET') return []; writes.push({ path, body }); return {} }
  await assert.rejects(publishReports({ GITHUB_EVENT_NAME: 'schedule', GITHUB_REF: 'refs/heads/main', GITHUB_REPOSITORY: 'ShiningWank0/Omni_Money',
    GITHUB_RUN_ID: '123', GITHUB_RUN_ATTEMPT: '1', GITHUB_TOKEN: 'fixture', PINNED_TEST_OUTCOME: 'failure' }, () => api), /validation job did not succeed/)
  assert.equal(writes.length, 1); assert.ok(writes[0].body.title.includes('workflow/validation'))
})

test('notification job owns write permission; scanning never executes archived programs with that token', () => {
  const workflow = readFileSync(join(root, '.github/workflows/pinned-input-security.yml'), 'utf8')
  const scan = workflow.slice(workflow.indexOf('\n  scan:'), workflow.indexOf('\n  notify:'))
  const notify = workflow.slice(workflow.indexOf('\n  notify:'))
  assert.ok(scan.includes('packages: read')); assert.ok(!scan.includes(': write'))
  assert.ok(notify.includes('issues: write') && !notify.includes('packages: write'))
  assert.ok(notify.includes('needs.tests.result') && notify.includes("github.ref == 'refs/heads/main'"))
  assert.ok(!workflow.includes('pull_request_target'))
  assert.ok(notify.includes('node scripts/pinned-security.mjs publish'))
  assert.equal((workflow.match(/ref: \$\{\{ github.sha \}\}/g) || []).length, 3)
})
