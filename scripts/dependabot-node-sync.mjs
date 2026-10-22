import { appendFileSync, readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const repository = 'ShiningWank0/Omni_Money'
const repositoryID = 1171756271
const prefix = `/repos/${repository}`
const shaPattern = /^[a-f0-9]{40}$/
const versionPattern = '(?:0|[1-9][0-9]*)\\.(?:0|[1-9][0-9]*)\\.(?:0|[1-9][0-9]*)'
const skip = reason => ({ eligible: false, reason })

export function githubRequest(token, fetchImpl = fetch) {
  if (!token) throw new Error('GitHub token is required')
  return async (method, path, body) => {
    if (!path.startsWith(`${prefix}/`)) throw new Error('Unexpected repository API path')
    const response = await fetchImpl(`https://api.github.com${path}`, {
      method,
      redirect: 'error',
      signal: AbortSignal.timeout(30_000),
      headers: {
        Authorization: `Bearer ${token}`,
        Accept: 'application/vnd.github+json',
        'X-GitHub-Api-Version': '2022-11-28',
        ...(body ? { 'Content-Type': 'application/json' } : {}),
      },
      ...(body ? { body: JSON.stringify(body) } : {}),
    })
    if (!response.ok) throw new Error(`GitHub API ${method} failed (${response.status}); no forced update will be attempted`)
    return response.json()
  }
}

async function readFile(request, path, ref) {
  const file = await request('GET', `${prefix}/contents/${path}?ref=${ref}`)
  if (file.type !== 'file' || file.encoding !== 'base64' || file.size > 65_536 || !shaPattern.test(file.sha)) {
    throw new Error('Expected a small regular repository file')
  }
  return Buffer.from(file.content, 'base64').toString('utf8')
}

function nodeImage(text) {
  const lines = text.split('\n').filter(line => /^FROM\s+node:/.test(line))
  if (lines.length !== 1) return null
  const match = new RegExp(`^FROM node:(${versionPattern})-alpine@sha256:([a-f0-9]{64}) AS frontend-builder$`).exec(lines[0])
  if (!match || !match[1].split('.').every(n => Number.isSafeInteger(Number(n)))) return null
  return { line: lines[0], version: match[1], digest: match[2] }
}

function compare(a, b) {
  const left = a.split('.').map(Number)
  const right = b.split('.').map(Number)
  for (let i = 0; i < 3; i++) if (left[i] !== right[i]) return Math.sign(left[i] - right[i])
  return 0
}

// Keep automatic changes inside the explicitly supported caret ranges. Any
// other engines syntax requires review instead of guessing its meaning.
function supported(version, engines) {
  if (typeof engines !== 'string') return false
  const ranges = engines.split(' || ')
  if (!ranges.every(range => new RegExp(`^\\^${versionPattern}$`).test(range))) return false
  return ranges.some(range => {
    const minimum = range.slice(1)
    return Number(minimum.split('.')[0]) > 0 && version.split('.')[0] === minimum.split('.')[0] && compare(version, minimum) >= 0
  })
}

// All PR files are read as data through the API. Only this default-branch
// implementation executes, including when the caller holds a write token.
export async function planNodeSync({ event, eventName, request }) {
  const run = event.workflow_run
  if (eventName !== 'workflow_run' || event.action !== 'requested' ||
      event.repository?.full_name !== repository || event.repository?.id !== repositoryID ||
      !run || run.event !== 'pull_request' || run.name !== 'CI' || run.path !== '.github/workflows/ci.yml' ||
      run.head_repository?.id !== repositoryID || run.head_repository?.full_name !== repository ||
      !/^dependabot\/docker\/node-[a-zA-Z0-9.-]+$/.test(run.head_branch || '') || !shaPattern.test(run.head_sha || '') ||
      run.pull_requests?.length !== 1 || !Number.isSafeInteger(run.pull_requests[0].number) || run.pull_requests[0].number < 1) {
    return skip('対象のDependabot Node更新PRではありません。')
  }
  const number = run.pull_requests[0].number
  const pr = await request('GET', `${prefix}/pulls/${number}`)
  if (pr.state !== 'open' || pr.draft || pr.user?.login !== 'dependabot[bot]' || pr.user?.id !== 49699333 || pr.user?.type !== 'Bot' ||
      pr.base?.ref !== 'main' || pr.base?.repo?.id !== repositoryID || pr.head?.repo?.id !== repositoryID ||
      pr.head?.ref !== run.head_branch || pr.head?.sha !== run.head_sha || !shaPattern.test(pr.base?.sha || '')) {
    return skip('PRの作成者・参照先・現在のコミットが対象条件と一致しません。')
  }
  if (pr.changed_files < 1 || pr.changed_files > 2) return skip('追加変更があるため手動レビューが必要です。')
  const files = await request('GET', `${prefix}/pulls/${number}/files?per_page=100`)
  if (!Array.isArray(files) || files.length !== pr.changed_files ||
      !files.some(file => file.filename === 'Dockerfile') ||
      files.some(file => !['Dockerfile', '.node-version'].includes(file.filename) || file.status !== 'modified')) {
    return skip('Dockerfileと.node-version以外の変更は自動処理しません。')
  }
  const base = pr.base.sha
  const head = pr.head.sha
  const oldDocker = await readFile(request, 'Dockerfile', base)
  const newDocker = await readFile(request, 'Dockerfile', head)
  const oldImage = nodeImage(oldDocker)
  const newImage = nodeImage(newDocker)
  if (!oldImage || !newImage || newDocker !== oldDocker.replace(oldImage.line, newImage.line)) {
    return skip('固定されたNodeイメージの1行だけを変更するPRに限定しています。')
  }
  if (oldImage.version === newImage.version) return skip('Nodeの版は変わっていません。')
  if (oldImage.version.split('.')[0] !== newImage.version.split('.')[0] ||
      compare(newImage.version, oldImage.version) <= 0 || oldImage.digest === newImage.digest) {
    return skip('メジャー更新・後退・不整合なdigestは手動レビューが必要です。')
  }
  const oldPin = await readFile(request, '.node-version', base)
  const currentPin = await readFile(request, '.node-version', head)
  if (currentPin === `${newImage.version}\n`) return skip('Nodeの固定版は同期済みです。')
  if (oldPin !== `${oldImage.version}\n` || currentPin !== oldPin) {
    return skip('既存の固定版に別の変更があるため、自動上書きしません。')
  }
  const pkg = JSON.parse(await readFile(request, 'frontend/package.json', base))
  if (!supported(newImage.version, pkg.engines?.node)) return skip('Nodeの対応範囲を手動で確認してください。')
  return { eligible: true, number, branch: pr.head.ref, head, version: newImage.version, reason: 'Node固定版を同じPRに同期できます。' }
}

export async function applyNodeSync(options) {
  // Re-plan with the current PR state after obtaining the short-lived token.
  const plan = await planNodeSync(options)
  if (!plan.eligible) return plan
  const { request } = options
  const commit = await request('GET', `${prefix}/git/commits/${plan.head}`)
  if (!shaPattern.test(commit.tree?.sha || '')) throw new Error('Invalid parent tree')
  const tree = await request('GET', `${prefix}/git/trees/${commit.tree.sha}`)
  const pin = tree.tree?.find(entry => entry.path === '.node-version')
  if (tree.truncated || pin?.mode !== '100644' || pin?.type !== 'blob') throw new Error('.node-version must be a regular non-executable file')
  const blob = await request('POST', `${prefix}/git/blobs`, { content: `${plan.version}\n`, encoding: 'utf-8' })
  if (!shaPattern.test(blob.sha || '')) throw new Error('Invalid new blob')
  const updatedTree = await request('POST', `${prefix}/git/trees`, {
    base_tree: commit.tree.sha,
    tree: [{ path: '.node-version', mode: '100644', type: 'blob', sha: blob.sha }],
  })
  if (!shaPattern.test(updatedTree.sha || '')) throw new Error('Invalid new tree')
  const updatedCommit = await request('POST', `${prefix}/git/commits`, {
    message: `[dependabot skip] chore: DockerのNode ${plan.version}に固定版を同期`,
    tree: updatedTree.sha,
    parents: [plan.head],
  })
  if (!shaPattern.test(updatedCommit.sha || '')) throw new Error('Invalid new commit')
  // A concurrently advanced branch rejects this commit as non-fast-forward.
  // Never retry with force, and never commit against an unvalidated new head.
  await request('PATCH', `${prefix}/git/refs/heads/${plan.branch}`, { sha: updatedCommit.sha, force: false })
  return { ...plan, commit: updatedCommit.sha, reason: `PR #${plan.number}のNode固定版を${plan.version}に同期しました。` }
}

async function main() {
  const mode = process.argv[2]
  if (!['inspect', 'apply'].includes(mode) || process.env.GITHUB_REPOSITORY !== repository) {
    throw new Error('Expected inspect/apply in the configured repository')
  }
  const options = {
    event: JSON.parse(readFileSync(process.env.GITHUB_EVENT_PATH, 'utf8')),
    eventName: process.env.GITHUB_EVENT_NAME,
    request: githubRequest(process.env.GH_TOKEN),
  }
  const result = await (mode === 'apply' ? applyNodeSync(options) : planNodeSync(options))
  if (mode === 'inspect' && process.env.GITHUB_OUTPUT) appendFileSync(process.env.GITHUB_OUTPUT, `eligible=${result.eligible}\n`)
  if (process.env.GITHUB_STEP_SUMMARY) appendFileSync(process.env.GITHUB_STEP_SUMMARY, `${result.reason}\n`)
  console.log(result.reason)
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().catch(error => { console.error(error.message); process.exitCode = 1 })
}
