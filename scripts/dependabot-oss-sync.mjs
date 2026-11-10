import { appendFileSync, lstatSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { githubRequest } from './dependabot-node-sync.mjs'
import { advanceUnchangedApprovals, usageFromInputs, verifyBundledPackages } from './oss-notices.mjs'

const repository = 'ShiningWank0/Omni_Money'
const repositoryID = 1171756271
const prefix = `/repos/${repository}`
const shaPattern = /^[a-f0-9]{40}$/
const versionPattern = /^v?\d+\.\d+\.\d+(?:-[A-Za-z0-9.-]+)?(?:\+[A-Za-z0-9.-]+)?$/
const stable = value => `${JSON.stringify(value, null, 2)}\n`
const skip = reason => ({ eligible: false, reason })
export const dependencyPaths = ['go.mod', 'go.sum', 'frontend/package.json', 'frontend/package-lock.json']
export const generatedPaths = ['frontend/bundled-dependencies.json', 'licenses/approved.json',
  'frontend/src/generated/thirdPartyNotices.json', 'licenses/dependency-usage.json']
const maxFileSize = 1_000_000

async function readFile(request, path, ref) {
  const file = await request('GET', `${prefix}/contents/${path}?ref=${ref}`)
  if (file?.type !== 'file' || file.encoding !== 'base64' || !shaPattern.test(file.sha || '') ||
      !Number.isSafeInteger(file.size) || file.size < 1 || file.size > maxFileSize || typeof file.content !== 'string') {
    throw new Error(`Expected a bounded regular repository file: ${path}`)
  }
  const bytes = Buffer.from(file.content, 'base64')
  if (bytes.length !== file.size) throw new Error(`Invalid file size: ${path}`)
  return bytes.toString('utf8')
}

export function goRequirements(text) {
  const requirements = new Map()
  let block = false
  const skeleton = []
  for (const line of text.split('\n')) {
    if (/^require \($/.test(line)) { block = true; skeleton.push(line); continue }
    if (block && line === ')') { block = false; skeleton.push(line); continue }
    if (!block && !/^require /.test(line)) { skeleton.push(line); continue }
    const match = /^\s*(?:require )?([A-Za-z0-9][A-Za-z0-9._~!+/-]*)\s+(v\d+\.\d+\.\d+(?:-[A-Za-z0-9.-]+)?(?:\+[A-Za-z0-9.-]+)?)\s*(\/\/ indirect)?\s*$/.exec(line)
    if (!match || requirements.has(match[1])) throw new Error('Unsupported or duplicate Go requirement')
    requirements.set(match[1], { version: match[2], indirect: Boolean(match[3]) })
  }
  if (block || !requirements.size || /^\s*(replace|exclude)\b/m.test(text)) throw new Error('Go replacements or unsupported directives require review')
  return { requirements, skeleton: skeleton.join('\n') }
}

function goSums(text) {
  const result = new Map()
  for (const line of text.trim().split('\n')) {
    const match = /^(\S+) (v\S+) (h1:[A-Za-z0-9+/]+=)$/.exec(line)
    if (!match || result.has(`${match[1]} ${match[2]}`)) throw new Error('Invalid or duplicate Go checksum')
    result.set(`${match[1]} ${match[2]}`, match[3])
  }
  return result
}

export function validateDependencyInputs(before, after) {
  if (goRequirements(before['go.mod']).skeleton !== goRequirements(after['go.mod']).skeleton) {
    throw new Error('Go module identity, toolchain or other settings changed; manual review is required')
  }
  const oldSums = goSums(before['go.sum']), newSums = goSums(after['go.sum'])
  for (const [key, value] of newSums) {
    if (oldSums.has(key) && oldSums.get(key) !== value) throw new Error('An existing Go module checksum changed')
  }
  const oldPackage = JSON.parse(before['frontend/package.json'])
  const pkg = JSON.parse(after['frontend/package.json'])
  const settings = value => Object.fromEntries(Object.entries(value).filter(([key]) => !['dependencies', 'devDependencies'].includes(key)))
  if (stable(settings(pkg)) !== stable(settings(oldPackage))) throw new Error('npm scripts, engines, overrides or other settings changed')
  for (const field of ['dependencies', 'devDependencies']) {
    if (JSON.stringify(Object.keys(pkg[field] || {}).sort()) !== JSON.stringify(Object.keys(oldPackage[field] || {}).sort())) {
      throw new Error('Adding or removing direct npm dependencies requires manual review')
    }
    for (const spec of Object.values(pkg[field] || {})) {
      if (typeof spec !== 'string' || !versionPattern.test(spec.replace(/^[~^]/, ''))) throw new Error('Only registry version ranges are supported')
    }
  }
  const lock = JSON.parse(after['frontend/package-lock.json'])
  if (lock.lockfileVersion !== 3 || !lock.packages?.[''] || Object.keys(lock.packages).length > 10_000) throw new Error('Unsupported npm lockfile')
  for (const field of ['dependencies', 'devDependencies']) {
    if (stable(lock.packages[''][field]) !== stable(pkg[field])) throw new Error('npm manifest and lockfile disagree')
  }
  for (const [path, entry] of Object.entries(lock.packages)) {
    if (!path) continue
    if (!/^node_modules\/(?:[A-Za-z0-9@_.+-]+\/)*[A-Za-z0-9@_.+-]+$/.test(path) ||
        path.split('/').some(part => part === '.' || part === '..') || entry.link ||
        !versionPattern.test(entry.version || '') ||
        typeof entry.resolved !== 'string' || !entry.resolved.startsWith('https://registry.npmjs.org/') ||
        !/^sha(?:256|384|512)-[A-Za-z0-9+/]+=*$/.test(entry.integrity || '')) {
      throw new Error('Only integrity-pinned npm registry packages are supported')
    }
  }
}

// This runs exclusively from the default-branch checkout. PRs supply manifest
// data; no PR source, workflow, script or generated approval is executed.
export async function planOSSSync({ event, eventName, request }) {
  const run = event.workflow_run
  if (eventName !== 'workflow_run' || event.action !== 'completed' ||
      event.repository?.id !== repositoryID || event.repository?.full_name !== repository ||
      run?.event !== 'pull_request' || run.name !== 'CI' || run.path !== '.github/workflows/ci.yml' ||
      !['success', 'failure'].includes(run.conclusion) || run.head_repository?.id !== repositoryID ||
      run.head_repository?.full_name !== repository || !shaPattern.test(run.head_sha || '') ||
      !/^dependabot\/(go_modules\/|npm_and_yarn\/frontend\/)[A-Za-z0-9_./-]+$/.test(run.head_branch || '') || run.head_branch.includes('..') ||
      run.pull_requests?.length !== 1 || !Number.isSafeInteger(run.pull_requests[0].number) || run.pull_requests[0].number < 1) {
    return skip('対象のDependabot Go/npm更新PRではありません。')
  }
  const number = run.pull_requests[0].number
  const pr = await request('GET', `${prefix}/pulls/${number}`)
  if (pr.state !== 'open' || pr.draft || pr.user?.id !== 49699333 || pr.user?.login !== 'dependabot[bot]' || pr.user?.type !== 'Bot' ||
      pr.base?.ref !== 'main' || pr.base?.repo?.id !== repositoryID || pr.head?.repo?.id !== repositoryID ||
      pr.head?.ref !== run.head_branch || pr.head?.sha !== run.head_sha || !shaPattern.test(pr.base?.sha || '') ||
      !Number.isSafeInteger(pr.changed_files) || pr.changed_files < 1 || pr.changed_files > 8) {
    return skip('PRの作成者・変更範囲・現在のコミットが対象条件と一致しません。')
  }
  const files = await request('GET', `${prefix}/pulls/${number}/files?per_page=100`)
  const allowed = [...dependencyPaths, ...generatedPaths]
  if (!Array.isArray(files) || files.length !== pr.changed_files || new Set(files.map(file => file.filename)).size !== files.length ||
      !files.some(file => dependencyPaths.includes(file.filename)) ||
      files.some(file => !allowed.includes(file.filename) || file.status !== 'modified')) {
    return skip('依存ファイルとOSS生成物だけの更新に限定しています。')
  }
  const [baseTree, headTree] = await Promise.all([pr.base.sha, pr.head.sha].map(ref => request('GET', `${prefix}/git/trees/${ref}?recursive=1`)))
  for (const tree of [baseTree, headTree]) {
    if (tree.truncated || !Array.isArray(tree.tree)) throw new Error('Cannot validate the repository tree')
    for (const path of allowed) {
      const entry = tree.tree.find(entry => entry.path === path)
      if (entry?.mode !== '100644' || entry.type !== 'blob') throw new Error(`Expected a regular, non-executable file: ${path}`)
    }
  }
  const readSet = async (paths, ref) => Object.fromEntries(await Promise.all(paths.map(async path => [path, await readFile(request, path, ref)])))
  const [before, inputs, baseline, current] = await Promise.all([
    readSet(dependencyPaths, pr.base.sha), readSet(dependencyPaths, pr.head.sha),
    readSet(generatedPaths, pr.base.sha), readSet(generatedPaths, pr.head.sha),
  ])
  validateDependencyInputs(before, inputs)
  return { eligible: true, number, branch: pr.head.ref, head: pr.head.sha, base: pr.base.sha,
    inputs, baseline, current, reason: `PR #${number}のOSS同期候補を生成します。` }
}

export function validateCandidate(plan, candidate) {
  if (!plan.eligible || candidate?.schema !== 1 || candidate.repository !== repository ||
      candidate.number !== plan.number || candidate.branch !== plan.branch || candidate.head !== plan.head || candidate.base !== plan.base ||
      stable(Object.keys(candidate).sort()) !== stable(['schema', 'repository', 'number', 'branch', 'head', 'base', 'files'].sort()) ||
      !candidate.files || stable(Object.keys(candidate.files).sort()) !== stable([...generatedPaths].sort())) {
    throw new Error('OSS candidate does not match the current PR and immutable revisions')
  }
  const parsed = Object.fromEntries(generatedPaths.map(path => {
    const text = candidate.files[path]
    if (typeof text !== 'string' || Buffer.byteLength(text) > maxFileSize) throw new Error('Invalid generated file size')
    const value = JSON.parse(text)
    if (stable(value) !== text) throw new Error('Generated files must be canonical JSON data')
    return [path, value]
  }))
  const previous = JSON.parse(plan.baseline[generatedPaths[2]]), catalog = parsed[generatedPaths[2]]
  const approval = advanceUnchangedApprovals(previous, catalog, JSON.parse(plan.baseline[generatedPaths[1]]))
  if (stable(approval) !== candidate.files[generatedPaths[1]]) throw new Error('OSS approvals differ from unchanged-notice policy')
  const bundled = parsed[generatedPaths[0]], oldBundled = JSON.parse(plan.baseline[generatedPaths[0]])
  const withoutVersion = list => list.map(({ version, ...entry }) => entry)
  if (stable(withoutVersion(bundled)) !== stable(withoutVersion(oldBundled))) throw new Error('Bundled package locations or distribution changed')
  verifyBundledPackages(catalog, bundled)
  const lock = JSON.parse(plan.inputs['frontend/package-lock.json'])
  for (const pkg of bundled) {
    if (lock.packages[pkg.directory]?.version !== pkg.version) throw new Error('Bundled version differs from PR lockfile')
  }
  const sums = goSums(plan.inputs['go.sum'])
  const { requirements } = goRequirements(plan.inputs['go.mod'])
  for (const component of catalog.components.filter(component => component.id.startsWith('go:'))) {
    if (!sums.has(`${component.name} ${component.version}`) ||
        (requirements.has(component.name) && requirements.get(component.name).version !== component.version)) {
      throw new Error('Distributed Go version differs from PR dependency inputs')
    }
  }
  const usage = parsed[generatedPaths[3]]
  if (!Array.isArray(usage.dependencies) || usage.dependencies.length > 12_000) throw new Error('Invalid usage inventory')
  const modules = usage.dependencies.filter(entry => entry.id?.startsWith('go:')).map(entry => {
    if (entry.id !== `go:${entry.name}` || !versionPattern.test(entry.version || '') ||
        !/^[A-Za-z0-9][A-Za-z0-9._~!+/-]*$/.test(entry.name || '') || !['direct', 'indirect'].includes(entry.dependencyType)) {
      throw new Error('Invalid Go usage identity')
    }
    // Lazy module loading omits sums for many non-distributed CLI/test modules.
    // Those entries carry no approval or public notice; the normal CI recomputes
    // their inventory. Every distributed module was checksum-checked above.
    return { Path: entry.name, Version: entry.version, Indirect: entry.dependencyType === 'indirect' }
  })
  if (new Set(modules.map(module => module.Path)).size !== modules.length) throw new Error('Duplicate Go usage')
  for (const [path, requirement] of requirements) {
    const module = modules.find(module => module.Path === path)
    if (!module || module.Version !== requirement.version || module.Indirect !== requirement.indirect) throw new Error('Missing or inconsistent required Go module')
  }
  if (stable(usageFromInputs(catalog, lock, modules)) !== candidate.files[generatedPaths[3]]) throw new Error('Unexpected dependency usage metadata')
  return Object.fromEntries(generatedPaths.filter(path => candidate.files[path] !== plan.current[path]).map(path => [path, candidate.files[path]]))
}

export async function applyOSSSync(options, candidate) {
  const plan = await planOSSSync(options)
  if (!plan.eligible) return plan
  const files = validateCandidate(plan, candidate)
  if (!Object.keys(files).length) return skip('OSS情報は同期済みです。追加コミットは不要です。')
  const { request } = options
  const parent = await request('GET', `${prefix}/git/commits/${plan.head}`)
  if (!shaPattern.test(parent.tree?.sha || '')) throw new Error('Invalid parent tree')
  const tree = await request('POST', `${prefix}/git/trees`, {
    base_tree: parent.tree.sha,
    tree: Object.entries(files).map(([path, content]) => ({ path, mode: '100644', type: 'blob', content })),
  })
  if (!shaPattern.test(tree.sha || '')) throw new Error('Invalid generated tree')
  const commit = await request('POST', `${prefix}/git/commits`, {
    message: '[dependabot skip] chore: OSSの版・依存用途一覧を同期',
    tree: tree.sha, parents: [plan.head],
  })
  if (!shaPattern.test(commit.sha || '')) throw new Error('Invalid generated commit')
  const current = await request('GET', `${prefix}/pulls/${plan.number}`)
  if (current.state !== 'open' || current.head?.sha !== plan.head || current.base?.sha !== plan.base) throw new Error('PR changed before publication; no update attempted')
  // A competing head update is rejected as non-fast-forward. Never force or
  // retry against another head, and never change a dependency or source file.
  await request('PATCH', `${prefix}/git/refs/heads/${plan.branch}`, { sha: commit.sha, force: false })
  return { eligible: true, commit: commit.sha, reason: `PR #${plan.number}へOSS情報の同期コミットを追加しました。通常CIの新しい結果を確認してください。` }
}

function readBoundedJSON(path) {
  const stat = lstatSync(path)
  if (!stat.isFile() || stat.isSymbolicLink() || stat.size > maxFileSize * 5) throw new Error('Expected a bounded JSON artifact')
  return JSON.parse(readFileSync(path, 'utf8'))
}

async function main() {
  const mode = process.argv[2]
  if (process.env.GITHUB_REPOSITORY !== repository || !['plan', 'prepare', 'pack', 'inspect', 'apply'].includes(mode)) throw new Error('Invalid OSS synchronization command')
  const candidatePath = process.env.OSS_SYNC_CANDIDATE
  const options = () => ({ event: JSON.parse(readFileSync(process.env.GITHUB_EVENT_PATH, 'utf8')),
    eventName: process.env.GITHUB_EVENT_NAME, request: githubRequest(process.env.GH_TOKEN) })
  let result
  if (mode === 'plan') {
    result = await planOSSSync(options())
    if (result.eligible && process.env.GITHUB_OUTPUT) appendFileSync(process.env.GITHUB_OUTPUT, `base=${result.base}\n`)
  } else if (mode === 'prepare') {
    const plan = await planOSSSync(options())
    if (plan.eligible) {
      mkdirSync(resolve(candidatePath, '..'), { recursive: true })
      writeFileSync(`${candidatePath}.plan`, stable(plan))
      // The second checkout is the validated main revision, never the PR head.
      if (process.env.OSS_SYNC_BASE !== plan.base) throw new Error('Base checkout does not match the validated main revision')
      for (const path of dependencyPaths) writeFileSync(join(process.env.OSS_SYNC_SOURCE, path), plan.inputs[path])
    }
    result = plan
  } else if (mode === 'pack') {
    const plan = readBoundedJSON(`${candidatePath}.plan`)
    for (const path of dependencyPaths) {
      if (readFileSync(join(process.env.OSS_SYNC_SOURCE, path), 'utf8') !== plan.inputs[path]) throw new Error('Generation changed dependency inputs')
    }
    const files = Object.fromEntries(generatedPaths.map(path => [path, readFileSync(join(process.env.OSS_SYNC_SOURCE, path), 'utf8')]))
    const candidate = { schema: 1, repository, number: plan.number, branch: plan.branch, head: plan.head, base: plan.base, files }
    const changed = validateCandidate(plan, candidate)
    writeFileSync(candidatePath, stable(candidate))
    result = { eligible: Object.keys(changed).length > 0, reason: Object.keys(changed).length ? '検証済みのOSS同期候補を保存しました。' : 'OSS情報は同期済みです。' }
  } else {
    const candidate = readBoundedJSON(candidatePath)
    if (mode === 'apply') result = await applyOSSSync(options(), candidate)
    else {
      const plan = await planOSSSync(options())
      const changed = plan.eligible ? validateCandidate(plan, candidate) : {}
      result = { eligible: Object.keys(changed).length > 0, reason: plan.eligible ? (Object.keys(changed).length ? 'OSS候補の内容と最新PRの一致を確認しました。' : 'OSS情報は同期済みです。') : plan.reason }
    }
  }
  if (process.env.GITHUB_OUTPUT) appendFileSync(process.env.GITHUB_OUTPUT, `eligible=${result.eligible}\n`)
  if (process.env.GITHUB_STEP_SUMMARY) appendFileSync(process.env.GITHUB_STEP_SUMMARY, `${result.reason}\n`)
  console.log(result.reason)
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().catch(error => {
    const message = `OSS自動同期を中止しました: ${error.message}`
    if (process.env.GITHUB_STEP_SUMMARY) appendFileSync(process.env.GITHUB_STEP_SUMMARY, `${message}\n`)
    console.error(message)
    process.exitCode = 1
  })
}
