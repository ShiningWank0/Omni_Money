import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { advanceUnchangedApprovals, usageFromInputs } from './oss-notices.mjs'
import { applyOSSSync, dependencyPaths, generatedPaths, planOSSSync, validateCandidate, validateDependencyInputs } from './dependabot-oss-sync.mjs'

const repository = 'ShiningWank0/Omni_Money'
const prefix = `/repos/${repository}`
const base = 'a'.repeat(40), head = 'b'.repeat(40), treeSHA = 'c'.repeat(40), commitSHA = 'd'.repeat(40)
const repo = { id: 1171756271, full_name: repository }
const branch = 'dependabot/go_modules/go-modules-minor-patch-example'
const stable = value => `${JSON.stringify(value, null, 2)}\n`
const read = path => readFileSync(new URL(`../${path}`, import.meta.url), 'utf8')

function fixture(kind = 'go') {
  const before = Object.fromEntries(dependencyPaths.map(path => [path, read(path)]))
  const baseline = Object.fromEntries(generatedPaths.map(path => [path, read(path)]))
  const inputs = structuredClone(before)
  const catalog = JSON.parse(baseline[generatedPaths[2]])
  const usage = JSON.parse(baseline[generatedPaths[3]])
  const modules = usage.dependencies.filter(entry => entry.id.startsWith('go:')).map(entry => ({
    Path: entry.name, Version: entry.version, Indirect: entry.dependencyType === 'indirect',
  }))
  if (kind === 'go') {
    const component = catalog.components.find(component => component.id === 'go:github.com/mattn/go-sqlite3')
    const previous = component.version
    const next = 'v1.14.53'
    inputs['go.mod'] = inputs['go.mod'].replace(`github.com/mattn/go-sqlite3 ${previous}`, `github.com/mattn/go-sqlite3 ${next}`)
    inputs['go.sum'] = inputs['go.sum'].replaceAll(`github.com/mattn/go-sqlite3 ${previous}`, `github.com/mattn/go-sqlite3 ${next}`)
    component.version = next
    component.url = `https://pkg.go.dev/${component.name}@${next}`
    modules.find(module => module.Path === component.name).Version = next
  } else {
    const pkg = JSON.parse(inputs['frontend/package.json'])
    const lock = JSON.parse(inputs['frontend/package-lock.json'])
    pkg.devDependencies.jsdom = '^31.0.0'
    lock.packages[''].devDependencies.jsdom = '^31.0.0'
    lock.packages['node_modules/jsdom'].version = '31.0.0'
    lock.packages['node_modules/jsdom'].resolved = 'https://registry.npmjs.org/jsdom/-/jsdom-31.0.0.tgz'
    inputs['frontend/package.json'] = stable(pkg)
    inputs['frontend/package-lock.json'] = stable(lock)
  }
  const approval = advanceUnchangedApprovals(JSON.parse(baseline[generatedPaths[2]]), catalog, JSON.parse(baseline[generatedPaths[1]]))
  const files = { ...baseline, [generatedPaths[1]]: stable(approval), [generatedPaths[2]]: stable(catalog),
    [generatedPaths[3]]: stable(usageFromInputs(catalog, JSON.parse(inputs['frontend/package-lock.json']), modules)) }
  const state = {
    eventName: 'workflow_run',
    event: { action: 'completed', repository: { ...repo }, workflow_run: {
      name: 'CI', event: 'pull_request', path: '.github/workflows/ci.yml', conclusion: 'failure',
      head_repository: { ...repo }, head_branch: branch, head_sha: head, pull_requests: [{ number: 302 }],
    } },
    pr: { state: 'open', draft: false, changed_files: kind === 'go' ? 2 : 2,
      user: { id: 49699333, login: 'dependabot[bot]', type: 'Bot' },
      base: { ref: 'main', sha: base, repo: { ...repo } }, head: { ref: branch, sha: head, repo: { ...repo } } },
    changed: (kind === 'go' ? ['go.mod', 'go.sum'] : ['frontend/package.json', 'frontend/package-lock.json']).map(filename => ({ filename, status: 'modified' })),
    before, inputs, baseline, current: structuredClone(baseline), treeMode: '100644', calls: [], race: false, finalRace: false,
    candidate: { schema: 1, repository, number: 302, branch, head, base, files },
  }
  state.plan = { eligible: true, number: 302, branch, head, base, inputs: state.inputs, baseline: state.baseline, current: state.current }
  state.request = async (method, path, body) => {
    state.calls.push({ method, path, body })
    if (method === 'GET') {
      if (path === `${prefix}/pulls/302`) {
        const result = structuredClone(state.pr)
        if (state.finalRace && state.calls.some(call => call.method === 'POST')) result.head.sha = 'f'.repeat(40)
        return result
      }
      if (path === `${prefix}/pulls/302/files?per_page=100`) return state.changed
      if (path === `${prefix}/git/trees/${base}?recursive=1` || path === `${prefix}/git/trees/${head}?recursive=1`) {
        return { truncated: false, tree: [...dependencyPaths, ...generatedPaths].map(path => ({ path, type: 'blob', mode: state.treeMode })) }
      }
      if (path === `${prefix}/git/commits/${head}`) return { tree: { sha: treeSHA } }
      const match = /^\/repos\/ShiningWank0\/Omni_Money\/contents\/(.+)\?ref=(.+)$/.exec(path)
      if (match) {
        const source = match[2] === base ? { ...state.before, ...state.baseline } : { ...state.inputs, ...state.current }
        const content = source[match[1]]
        assert.equal(typeof content, 'string', `unexpected fixture file ${path}`)
        return { type: 'file', encoding: 'base64', size: Buffer.byteLength(content), sha: treeSHA, content: Buffer.from(content).toString('base64') }
      }
    }
    if (method === 'POST' && path === `${prefix}/git/trees`) return { sha: treeSHA }
    if (method === 'POST' && path === `${prefix}/git/commits`) return { sha: commitSHA }
    if (method === 'PATCH' && path === `${prefix}/git/refs/heads/${branch}`) {
      if (state.race) throw new Error('non-fast-forward')
      return { object: { sha: commitSHA } }
    }
    assert.fail(`unexpected API request ${method} ${path}`)
  }
  return state
}

test('a go-sqlite3 update carries unchanged notices forward and changes only OSS metadata', async () => {
  const state = fixture()
  const plan = await planOSSSync(state)
  assert.equal(plan.eligible, true)
  assert.ok(state.calls.every(call => call.method === 'GET'))
  const changed = validateCandidate(plan, state.candidate)
  assert.deepEqual(Object.keys(changed).sort(), generatedPaths.slice(1).sort())
  assert.equal(JSON.parse(changed['licenses/approved.json']).components['go:github.com/mattn/go-sqlite3'].version, 'v1.14.53')
})

test('test-only jsdom update refreshes usage without approving or distributing test tools', async () => {
  const state = fixture('npm')
  state.event.workflow_run.head_branch = state.pr.head.ref = state.plan.branch = state.candidate.branch = 'dependabot/npm_and_yarn/frontend/jsdom-31.0.0'
  const plan = await planOSSSync(state)
  const changed = validateCandidate(plan, state.candidate)
  assert.deepEqual(Object.keys(changed), ['licenses/dependency-usage.json'])
  const jsdom = JSON.parse(changed['licenses/dependency-usage.json']).dependencies.find(entry => entry.id === 'npm:jsdom')
  assert.equal(jsdom.version, '31.0.0')
  assert.equal(jsdom.distribution, 'excluded')
  assert.equal(JSON.parse(state.candidate.files[generatedPaths[1]]).components['npm:jsdom'], undefined)
})

test('a Vue/Vite runtime update must synchronize bundle, approvals, display and usage together', () => {
  const state = fixture('npm')
  const catalog = JSON.parse(state.candidate.files[generatedPaths[2]])
  const bundle = JSON.parse(state.candidate.files[generatedPaths[0]])
  const lock = JSON.parse(state.inputs['frontend/package-lock.json'])
  const pkg = JSON.parse(state.inputs['frontend/package.json'])
  const component = catalog.components.find(component => component.id === 'npm:vite')
  component.version = '8.3.6'
  component.url = 'https://www.npmjs.com/package/vite/v/8.3.6'
  bundle.find(entry => entry.name === 'vite').version = '8.3.6'
  pkg.devDependencies.vite = lock.packages[''].devDependencies.vite = '^8.3.6'
  lock.packages['node_modules/vite'].version = '8.3.6'
  lock.packages['node_modules/vite'].resolved = 'https://registry.npmjs.org/vite/-/vite-8.3.6.tgz'
  state.inputs['frontend/package.json'] = stable(pkg)
  state.inputs['frontend/package-lock.json'] = stable(lock)
  state.candidate.files[generatedPaths[0]] = stable(bundle)
  state.candidate.files[generatedPaths[1]] = stable(advanceUnchangedApprovals(JSON.parse(state.baseline[generatedPaths[2]]), catalog, JSON.parse(state.baseline[generatedPaths[1]])))
  state.candidate.files[generatedPaths[2]] = stable(catalog)
  const modules = JSON.parse(state.baseline[generatedPaths[3]]).dependencies.filter(entry => entry.id.startsWith('go:')).map(entry => ({ Path: entry.name, Version: entry.version, Indirect: entry.dependencyType === 'indirect' }))
  state.candidate.files[generatedPaths[3]] = stable(usageFromInputs(catalog, lock, modules))
  assert.deepEqual(Object.keys(validateCandidate(state.plan, state.candidate)), generatedPaths)
})

test('the writer creates one allowlisted tree and a fast-forward commit parented by the validated head', async () => {
  const state = fixture()
  const result = await applyOSSSync(state, state.candidate)
  assert.equal(result.commit, commitSHA)
  const writes = state.calls.filter(call => call.method !== 'GET')
  assert.deepEqual(writes.map(call => call.method), ['POST', 'POST', 'PATCH'])
  assert.ok(writes[0].body.tree.every(entry => generatedPaths.includes(entry.path) && entry.mode === '100644' && entry.type === 'blob'))
  assert.deepEqual(writes[1].body.parents, [head])
  assert.deepEqual(writes[2].body, { sha: commitSHA, force: false })
})

test('a synchronized PR causes no writes or additional CI loop', async () => {
  const state = fixture()
  state.current = structuredClone(state.candidate.files)
  assert.equal((await applyOSSSync(state, state.candidate)).eligible, false)
  assert.ok(state.calls.every(call => call.method === 'GET'))
})

for (const [name, mutate] of Object.entries({
  'fork repository': s => { s.event.repository.id = 9 },
  'fork head': s => { s.event.workflow_run.head_repository.id = 9 },
  'wrong event': s => { s.eventName = 'pull_request_target' },
  'wrong workflow': s => { s.event.workflow_run.path = '.github/workflows/other.yml' },
  'cancelled run': s => { s.event.workflow_run.conclusion = 'cancelled' },
  'wrong action': s => { s.event.action = 'requested' },
  'ambiguous PR': s => { s.event.workflow_run.pull_requests.push({ number: 303 }) },
  'human-authored PR': s => { s.pr.user.id = 7 },
  'closed PR': s => { s.pr.state = 'closed' },
  'draft PR': s => { s.pr.draft = true },
  'wrong base': s => { s.pr.base.ref = 'release' },
  'stale head': s => { s.pr.head.sha = 'e'.repeat(40) },
  'unrelated source': s => { s.changed.push({ filename: 'server.go', status: 'modified' }); s.pr.changed_files++ },
  'workflow change': s => { s.changed.push({ filename: '.github/workflows/ci.yml', status: 'modified' }); s.pr.changed_files++ },
  'renamed input': s => { s.changed[0].status = 'renamed' },
  'truncated file list': s => { s.pr.changed_files++ },
})) test(`does not write for ${name}`, async () => {
  const state = fixture()
  mutate(state)
  assert.equal((await applyOSSSync(state, state.candidate)).eligible, false)
  assert.ok(state.calls.every(call => call.method === 'GET'))
})

for (const [name, mutate, pattern] of [
  ['changed notice text', value => { value.components[0].files[0].text += 'changed copyright' }, /manual review/],
  ['changed license', value => { value.components[0].license = 'different' }, /manual review/],
  ['new distributed component', value => { value.components.push({ ...value.components[0], id: 'npm:unknown' }) }, /manual review/],
  ['removed distributed component', value => { value.components.pop() }, /manual review/],
  ['changed target boundary', value => { value.components[0].targets.pop() }, /manual review/],
  ['changed native runtime', value => { value.components.find(entry => entry.id === 'native:go').version = '1.28.0' }, /manual review/],
  ['changed source URL', value => { value.components[0].url = 'https://untrusted.invalid/' }, /manual review/],
  ['changed application license', value => { value.applicationLicense += 'changed' }, /manual review/],
]) test(`requires review for ${name}`, () => {
  const state = fixture()
  const value = JSON.parse(state.candidate.files[generatedPaths[2]])
  mutate(value)
  state.candidate.files[generatedPaths[2]] = stable(value)
  assert.throws(() => validateCandidate(state.plan, state.candidate), pattern)
})

test('an artifact cannot authorize a new license by replacing both catalog and approvals', () => {
  const state = fixture()
  const approvals = JSON.parse(state.candidate.files[generatedPaths[1]])
  approvals.components['go:github.com/mattn/go-sqlite3'].sha256 = '0'.repeat(64)
  state.candidate.files[generatedPaths[1]] = stable(approvals)
  assert.throws(() => validateCandidate(state.plan, state.candidate), /approvals differ/)
})

for (const [name, mutate] of Object.entries({
  'another head': s => { s.candidate.head = base },
  'another base': s => { s.candidate.base = head },
  'extra file': s => { s.candidate.files['server.go'] = 'code' },
  'extra metadata': s => { s.candidate.execute = 'code' },
  'noncanonical data': s => { s.candidate.files[generatedPaths[0]] += '\n' },
  'oversized JSON': s => { s.candidate.files[generatedPaths[0]] = ' '.repeat(1_000_001) },
  'forged usage': s => { const usage = JSON.parse(s.candidate.files[generatedPaths[3]]); usage.dependencies[0].distribution = 'forged'; s.candidate.files[generatedPaths[3]] = stable(usage) },
  'missing required module': s => { const usage = JSON.parse(s.candidate.files[generatedPaths[3]]); usage.dependencies = usage.dependencies.filter(entry => entry.id !== 'go:github.com/mattn/go-sqlite3'); s.candidate.files[generatedPaths[3]] = stable(usage) },
})) test(`rejects candidate with ${name} before writes`, async () => {
  const state = fixture()
  mutate(state)
  await assert.rejects(applyOSSSync(state, state.candidate))
  assert.ok(state.calls.every(call => call.method === 'GET'))
})

test('symlink or executable dependency files are rejected', async () => {
  for (const mode of ['120000', '100755']) {
    const state = fixture()
    state.treeMode = mode
    await assert.rejects(planOSSSync(state), /regular, non-executable/)
  }
})

test('npm scripts and external dependency sources cannot be overlaid', () => {
  const state = fixture('npm')
  const pkg = JSON.parse(state.inputs['frontend/package.json'])
  pkg.scripts.build = 'unexpected command'
  assert.throws(() => validateDependencyInputs(state.before, { ...state.inputs, 'frontend/package.json': stable(pkg) }), /npm scripts/)
  const lock = JSON.parse(state.inputs['frontend/package-lock.json'])
  lock.packages['node_modules/jsdom'].resolved = 'https://untrusted.invalid/package.tgz'
  assert.throws(() => validateDependencyInputs(state.before, { ...state.inputs, 'frontend/package-lock.json': stable(lock) }), /npm registry/)
  assert.throws(() => validateDependencyInputs(state.before, { ...state.inputs, 'go.mod': state.inputs['go.mod'].replace('toolchain go', 'toolchain go0') }), /toolchain/)
})

test('existing Go checksums cannot be replaced', () => {
  const state = fixture()
  const modified = state.inputs['go.sum'].replace(/h1:[A-Za-z0-9+/]+=\n/, `h1:${'A'.repeat(43)}=\n`)
  assert.throws(() => validateDependencyInputs(state.before, { ...state.inputs, 'go.sum': modified }), /checksum changed/)
})

test('a concurrent head change never forces or retries the ref update', async () => {
  const state = fixture()
  state.race = true
  await assert.rejects(applyOSSSync(state, state.candidate), /non-fast-forward/)
  const updates = state.calls.filter(call => call.method === 'PATCH')
  assert.equal(updates.length, 1)
  assert.equal(updates[0].body.force, false)
})

test('a PR change detected immediately before publication prevents ref writes', async () => {
  const state = fixture()
  state.finalRace = true
  await assert.rejects(applyOSSSync(state, state.candidate), /PR changed/)
  assert.ok(state.calls.every(call => call.method !== 'PATCH'))
})

test('workflow isolates dependencies from App credentials and retains the mandatory OSS gate', () => {
  const workflow = read('.github/workflows/dependabot-oss-sync.yml')
  const [generate, publish] = workflow.split('\n  publish:')
  assert.match(generate, /types: \[completed\]/)
  assert.doesNotMatch(generate, /secrets\.|permission-contents: write|pull_request_target|cache: true/)
  assert.match(generate, /ref: \$\{\{ steps\.plan\.outputs\.base \}\}/)
  assert.match(generate, /npm ci --ignore-scripts/)
  assert.match(generate, /oss-notices\.mjs sync-unchanged/)
  assert.match(publish, /ref: \$\{\{ github\.workflow_sha \}\}/)
  assert.match(publish, /NODE_SYNC_APP_PRIVATE_KEY/)
  assert.doesNotMatch(publish, /npm (ci|install|run)|go (run|build)|head_sha|head\.sha/)
  assert.match(publish, /dependabot-oss-sync\.mjs inspect/)
  assert.match(publish, /dependabot-oss-sync\.mjs apply/)
  assert.match(read('.github/workflows/ci.yml'), /node scripts\/oss-notices\.mjs check/)
})
