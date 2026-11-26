import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { appendFileSync, chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { gunzipSync } from 'node:zlib'
import { githubClient, publish, render } from './security-issues.mjs'
import { compareVersion, groups, inventory, root, sha256 } from './pinned-security-inventory.mjs'
import { msysSBOM } from './pinned-security-msys.mjs'
import { classifyCNA, nvdPage } from './pinned-security-advisories.mjs'
import { auditModules, osvBatch } from './pinned-security-modules.mjs'

const limit = 32 * 1024 * 1024
const readJSON = path => JSON.parse(readFileSync(path, 'utf8'))
const saveJSON = (path, value) => { mkdirSync(dirname(path), { recursive: true }); writeFileSync(path, JSON.stringify(value, null, 2) + '\n') }
const safe = value => String(value ?? '').slice(0, 1000).replaceAll('@', '@\u200b').replace(/[&<>"'`\[\]\\|*_\r\n]/g, c => `&#${c.charCodeAt(0)};`)
const requiredString = value => { assert.ok(typeof value === 'string' && value.length > 0 && value.length <= 1000); return value }

export async function get(url, env = process.env, fetcher = fetch) {
  assert.ok(url.startsWith('https://'))
  const headers = url.startsWith('https://api.github.com/') && env.GITHUB_TOKEN
    ? { Authorization: `Bearer ${env.GITHUB_TOKEN}`, Accept: 'application/vnd.github+json' } : {}
  const response = await fetcher(url, { headers, signal: AbortSignal.timeout(90_000) })
  assert.ok(response.ok && response.url.startsWith('https://'), 'advisory/download request failed')
  const chunks = []; let size = 0
  for await (const chunk of response.body) {
    size += chunk.length; assert.ok(size <= 512 * 1024 * 1024, 'download exceeds limit'); chunks.push(chunk)
  }
  return Buffer.concat(chunks)
}

export function scanEnvironment(work, inherited) {
  const env = { ...inherited, DOCKER_CONFIG: join(work, 'registry'), TRIVY_CACHE_DIR: join(work, 'trivy-db'),
    GRYPE_DB_CACHE_DIR: join(work, 'grype-db'), GRYPE_DB_AUTO_UPDATE: 'false',
    GRYPE_CHECK_FOR_APP_UPDATE: 'false', GRYPE_MATCH_STOCK_USING_CPES: 'true' }
  // Global scanner credentials would also be offered to Docker Hub or a new
  // third-party registry. The repository token belongs only to ghcr.io.
  for (const key of ['TRIVY_USERNAME', 'TRIVY_PASSWORD', 'TRIVY_REGISTRY_TOKEN']) delete env[key]
  mkdirSync(env.DOCKER_CONFIG, { mode: 0o700 })
  assert.ok(inherited.GITHUB_ACTOR && inherited.GITHUB_TOKEN, 'read-only GHCR credentials are required')
  writeFileSync(join(env.DOCKER_CONFIG, 'config.json'), JSON.stringify({ auths: {
    'ghcr.io': { auth: Buffer.from(`${inherited.GITHUB_ACTOR}:${inherited.GITHUB_TOKEN}`).toString('base64') },
  } }), { mode: 0o600 })
  return env
}

function command(program, args, options = {}) {
  const result = spawnSync(program, args, { encoding: 'utf8', timeout: 20 * 60_000, maxBuffer: limit, ...options })
  if (result.stderr) process.stderr.write(result.stderr.slice(-4000))
  assert.equal(result.status, 0, `${program} failed (${result.error?.code || result.status})`)
  return result.stdout
}

async function payload(item, work, tool = item.name.split('/')[0]) {
  const folder = join(work, item.name.replaceAll('/', '-'))
  const archive = folder + '.tar.gz'
  if (item.url.startsWith('https://ghcr.io/')) {
    command('python3', [join(root, 'scripts/fetch-fixed-tool-archive.py'), tool, item.url, item.sha256, archive])
  } else {
    const data = await get(item.url)
    assert.equal(sha256(data), item.sha256, 'download checksum mismatch')
    if (!/\.(?:tar\.gz|tgz)$/.test(item.url)) {
      mkdirSync(folder, { mode: 0o700 })
      writeFileSync(join(folder, 'binary'), data, { mode: 0o700 })
      return folder
    }
    writeFileSync(archive, data, { mode: 0o600 })
  }
  try {
    command('python3', [join(root, 'scripts/pinned-security-extract.py'), archive, folder])
  } finally {
    rmSync(archive)
  }
  return folder
}

function cve(tool, id, component, installed, fixed, severity, context, summary) {
  return { tool, id: requiredString(id), component: requiredString(component), installed: String(installed || ''),
    fixed: String(fixed || ''), severity: String(severity || 'UNKNOWN'), context, summary: String(summary || '').slice(0, 1000) }
}

export function trivyReport(raw, target, type) {
  assert.equal(raw.SchemaVersion, 2); assert.ok(Array.isArray(raw.Results))
  const catalogs = raw.Results.filter(item => Array.isArray(item.Packages) && item.Packages.length)
  assert.ok(catalogs.length, 'scanner did not inventory packages')
  if (type === 'rootfs') assert.ok(catalogs.some(x => x.Type === 'gobinary'), 'Go binary inventory missing')
  if (/^(ubuntu|alpine-)/.test(target.name)) assert.ok(['ubuntu', 'alpine'].includes(raw.Metadata?.OS?.Family), 'OS inventory missing')
  return raw.Results.flatMap(item => (item.Vulnerabilities || []).map(v => cve('pinned-trivy', v.VulnerabilityID,
    `${target.name}/${item.Type}/${v.PkgName}`, v.InstalledVersion, v.FixedVersion, v.Severity,
    `${target.ref || target.sha256}; ${target.platform || 'static binary analysis'}`, v.Title)))
}

export function grypeReport(raw, context, identities = new Map()) {
  assert.ok(Array.isArray(raw.matches) && raw.source && raw.descriptor?.name === 'grype', 'incomplete Grype report')
  return raw.matches.map(match => {
    const v = match.vulnerability, artifact = match.artifact
    assert.ok(v && artifact?.name && artifact.version, 'incomplete Grype match')
    const location = artifact.locations?.map(x => x.path || x.accessPath).find(x => identities.has(x))
    const identity = identities.get(location)
    return cve('pinned-grype', v.id, `${context}/${identity?.name || artifact.name}`, identity?.installed || artifact.version,
      v.fix?.versions?.join(', '), v.severity, context,
      'CPE/PURL照合による候補です。対象platformとvendorのbackport・pkgrelを確認してください。')
  })
}

export async function scanImages(input, execute, evidence) {
  const results = []
  for (const target of input.images) results.push(await attempt(target.name, async () => {
    const file = join(evidence, target.name.replaceAll('/', '-') + '.json')
    execute('trivy', ['image', '--image-src', 'remote', '--platform', target.platform, '--scanners', 'vuln',
      '--pkg-types', 'os,library', '--list-all-pkgs', '--ignorefile', '/dev/null', '--format', 'json', '--output', file, '--exit-code', '0', target.ref])
    const raw = readJSON(file)
    return { findings: trivyReport(raw, target, 'image'), input: target,
      gaps: raw.Metadata?.OS?.EOSL ? ['OSのsecurity supportが終了しています。保存環境の更新が必要です。'] : [] }
  }))
  return results
}

export async function attempt(name, work) {
  try {
    const result = await work()
    return { name, status: result.gaps?.length ? 'partial' : result.findings?.length || result.updates?.length || result.reviews?.length ? 'findings' : 'clean',
      findings: [], ...result }
  } catch (error) {
    console.error(`${name}: ${error.message}`)
    return { name, status: 'error', findings: [], error: '検査・入力取得・schema検証のいずれかが失敗しました。実行ログを確認してください。' }
  }
}

async function vendorData(repo, evidence) {
  const release = JSON.parse(await get(`https://api.github.com/repos/msys2/${repo}/releases/tags/srcinfo-cache`))
  const values = {}
  for (const name of ['srcinfo.json.gz', 'sbom.cdx.json']) {
    const asset = release.assets?.find(x => x.name === name)
    assert.ok(asset && /^sha256:[a-f0-9]{64}$/.test(asset.digest), 'vendor metadata identity missing')
    assert.equal(asset.browser_download_url, `https://github.com/msys2/${repo}/releases/download/srcinfo-cache/${name}`)
    const age = Date.now() - Date.parse(asset.updated_at)
    assert.ok(age >= -3600_000 && age < 7 * 86400_000, 'MSYS2 advisory metadata is stale')
    const bytes = await get(asset.browser_download_url)
    assert.equal('sha256:' + sha256(bytes), asset.digest, 'vendor metadata checksum mismatch')
    values[name] = JSON.parse(name.endsWith('.gz') ? gunzipSync(bytes, { maxOutputLength: limit }).toString() : bytes.toString())
    saveJSON(join(evidence, `${repo}-${name}.identity.json`), { name, digest: asset.digest, updatedAt: asset.updated_at })
  }
  return { cache: values['srcinfo.json.gz'], sbom: values['sbom.cdx.json'] }
}

async function savedMSYSPackages(input, execute, work) {
  const manifestFile = join(work, 'msys2-manifest.json')
  execute('oras', ['manifest', 'fetch', '--output', manifestFile, input.msys.artifact])
  const raw = readFileSync(manifestFile)
  // Verify the manifest bytes too; do not trust only the tag or a supplied name.
  assert.equal('sha256:' + sha256(raw), input.msys.artifact.split('@')[1], 'MSYS2 manifest digest mismatch')
  const manifest = JSON.parse(raw.toString('utf8'))
  const layer = manifest.layers?.filter(x => x.annotations?.['org.opencontainers.image.title'] === 'packages.txt')
  assert.equal(layer?.length, 1); const descriptor = layer[0]
  assert.match(descriptor.digest, /^sha256:[a-f0-9]{64}$/); assert.ok(descriptor.size > 0 && descriptor.size < 1024 * 1024)
  const file = join(work, 'saved-msys2-packages.txt')
  execute('oras', ['blob', 'fetch', '--output', file, input.msys.artifact.split('@')[0] + '@' + descriptor.digest])
  const data = readFileSync(file); assert.equal('sha256:' + sha256(data), descriptor.digest)
  assert.equal(data.length, descriptor.size)
  return data.toString('utf8')
}

export function releaseUpdates(releases, source) {
  assert.ok(Array.isArray(releases) && releases.length > 0, 'release feed is empty')
  const versions = releases.filter(x => !x.draft && !x.prerelease).map(x => x.tag_name?.match(new RegExp(source.pattern))?.[1]).filter(Boolean)
  assert.ok(versions.length > 0, 'release feed does not cover the selected version line')
  const latest = versions.sort(compareVersion).at(-1)
  return compareVersion(latest, source.version) > 0
    ? [{ name: source.name, installed: source.version, latest,
      summary: source.trackingIssue
        ? `新しいstable版があります。Issue #${source.trackingIssue}の条件を確認し、公式修正版の採用と一時対応の撤去を行うPRを作成してください。修正の有無を版番号だけで断定しません。`
        : '新しいstable版があります。脆弱性の存在を断定する通知ではありません。更新PRで確認してください。' }] : []
}

async function scanNative(input, execute, work, evidence) {
  const results = []
  results.push(await attempt('native-libraries', async () => {
    const sbom = join(evidence, 'native.cdx.json'), output = join(evidence, 'native-grype.json')
    saveJSON(sbom, { bomFormat: 'CycloneDX', specVersion: '1.5', version: 1, components: input.native })
    execute('grype', ['sbom:' + sbom, '--output', 'json', '--file', output])
    return { findings: grypeReport(readJSON(output), 'native') }
  }))
  results.push(await attempt('msys2', async () => {
    const packages = await savedMSYSPackages(input, execute, work)
    const datasets = []
    for (const repo of ['MINGW-packages', 'MSYS2-packages']) datasets.push(await vendorData(repo, evidence))
    const value = msysSBOM(packages, datasets)
    const sbom = join(evidence, 'msys2.cdx.json'), output = join(evidence, 'msys2-grype.json')
    saveJSON(sbom, value.sbom)
    if (value.sbom.components.length) execute('grype', ['sbom:' + sbom, '--output', 'json', '--file', output])
    return { findings: value.sbom.components.length ? grypeReport(readJSON(output), 'msys2', value.identities) : [], gaps: value.gaps,
      input: { artifact: input.msys.artifact, installed: value.installedCount, mapped: value.mappedCount } }
  }))
  for (const source of input.advisories) results.push(await attempt('advisories/' + source.name, async () => {
    const ids = new Set(); let start = 0
    do {
      // NVD and CVE CNA records cover products that do not have a reliable
      // package ecosystem identifier. Unsupported ranges are review notices.
      const url = new URL('https://services.nvd.nist.gov/rest/json/cves/2.0')
      url.searchParams.set('keywordSearch', source.keyword); url.searchParams.set('keywordExactMatch', '')
      url.searchParams.set('resultsPerPage', '2000'); url.searchParams.set('startIndex', String(start))
      const data = JSON.parse(await get(url.href))
      for (const id of nvdPage(data, start)) ids.add(id)
      start += data.vulnerabilities.length
      if (start >= data.totalResults) break
      assert.ok(start < 10000, 'NVD pagination limit exceeded')
    } while (true)
    const findings = [], reviews = []
    for (const id of ids) {
      const record = JSON.parse(await get(`https://cveawg.mitre.org/api/cve/${id}`))
      assert.equal(record.cveMetadata?.cveId, id)
      const status = classifyCNA(record, source)
      if (status === 'affected') findings.push(cve('pinned-cna', id, source.name, source.version, '', 'UNKNOWN', 'CVE CNA version range',
        '公開CNAの製品名とversion範囲に一致します。platform・利用条件を確認してください。'))
      if (status === 'review') reviews.push(`${id}: ${source.name}@${source.version}; 製品・版の自動判定ができないため確認が必要`)
      saveJSON(join(evidence, `${source.name}-${id}.json`), record)
    }
    return { findings, reviews, input: source }
  }))
  for (const source of input.sources) results.push(await attempt('updates/' + source.name, async () => ({
    updates: releaseUpdates(JSON.parse(await get(`https://api.github.com/repos/${source.repo}/releases?per_page=100`)), source), findings: [],
  })))
  return results
}

export async function scanTools(input, execute, work, evidence, download = payload) {
  const results = []
  for (const target of input.tools) results.push(await attempt(target.name, async () => {
    const folder = await download({ ...target, name: 'audit-' + target.name }, work)
    const output = join(evidence, target.name.replaceAll('/', '-') + '.json')
    try {
      // Filesystem mode disables individual package analyzers, including Go
      // executables. Rootfs mode inspects the downloaded bytes without running them.
      execute('trivy', ['rootfs', '--scanners', 'vuln', '--pkg-types', 'library', '--list-all-pkgs', '--ignorefile', '/dev/null',
        '--format', 'json', '--output', output, '--exit-code', '0', folder])
      return { findings: trivyReport(readJSON(output), target, 'rootfs'), input: target }
    } finally { rmSync(folder, { recursive: true, force: true }) }
  }))
  return results
}

export function expectedNames(input, group) {
  return group === 'images' ? input.images.map(x => x.name) : group === 'tools' ? input.tools.map(x => x.name)
    : group === 'modules' ? [...input.modules.map(x => x.path), ...input.remoteModules.map(x => `${x.module}@${x.version}`), 'Go-toolchain']
    : ['native-libraries', 'msys2', ...input.advisories.map(x => 'advisories/' + x.name), ...input.sources.map(x => 'updates/' + x.name)]
}

export function validateReport(report, input, group) {
  assert.equal(report.schema, 1); assert.equal(report.group, group); assert.equal(report.inputIdentity, input.identity)
  assert.deepEqual(report.results.map(x => x.name).sort(), expectedNames(input, group).sort(), 'scan targets are missing, duplicated or unexpected')
  for (const result of report.results) {
    assert.ok(['clean', 'findings', 'partial', 'error'].includes(result.status))
    assert.ok(Array.isArray(result.findings))
    for (const finding of result.findings) for (const key of ['tool', 'id', 'component', 'installed', 'fixed', 'severity', 'context', 'summary']) {
      assert.ok(typeof finding[key] === 'string' && finding[key].length <= 1000)
    }
    assert.ok(result.findings.every(x => ['pinned-trivy', 'pinned-grype', 'pinned-cna', 'pinned-osv'].includes(x.tool) && x.id && x.component))
    assert.ok((result.gaps || []).every(x => typeof x === 'string' && x.length <= 1000))
    assert.ok((result.reviews || []).every(x => typeof x === 'string' && x.length <= 1000))
    assert.ok((result.updates || []).every(x => ['name', 'installed', 'latest', 'summary'].every(k => typeof x[k] === 'string' && x[k].length <= 1000)))
    const found = result.findings.length || result.updates?.length || result.reviews?.length
    if (result.status === 'clean') assert.ok(!found && !result.gaps?.length)
    if (result.status === 'findings') assert.ok(found)
    if (result.status === 'partial') assert.ok(result.gaps?.length)
  }
  return report
}

export function reportEntries(reports) {
  const values = new Map()
  const add = data => {
    const key = sha256(JSON.stringify(['pinned-input-security', data.kind, data.component]))
    values.set(key, { key, digest: sha256(JSON.stringify(data)), data })
  }
  for (const report of reports) for (const result of report.results) {
    const component = `${report.group}/${result.name}`
    if (result.status === 'error') add({ kind: 'scan-error', tool: 'pinned-inputs', component, detail: result.error || '検査結果が欠落または無効です。' })
    if (result.gaps?.length) add({ kind: 'coverage', tool: 'pinned-inputs', component, detail: result.gaps.slice().sort() })
    if (result.reviews?.length) add({ kind: 'review', tool: 'pinned-inputs', component, detail: result.reviews.slice().sort() })
    for (const update of result.updates || []) add({ kind: 'update', tool: 'pinned-inputs', component: update.name,
      installed: update.installed, latest: update.latest, detail: update.summary })
    const packages = new Map()
    for (const finding of result.findings) packages.set(finding.component, [...(packages.get(finding.component) || []), finding])
    for (const [component, findings] of packages) {
      const distinct = field => [...new Set(findings.map(x => x[field]).filter(Boolean))].sort()
      add({ kind: 'vulnerability', tool: findings[0].tool, component, id: distinct('id'), severity: distinct('severity'),
        installed: distinct('installed'), fixed: distinct('fixed'), summary: distinct('summary'), context: distinct('context') })
    }
  }
  return [...values.values()].sort((a, b) => a.key.localeCompare(b.key))
}

export function renderPinned(entry, runURL) {
  if (entry.data.kind === 'vulnerability') {
    const result = render(entry, runURL)
    result.body += '\n\n固定入力の自動照合結果です。CPE/PURL候補はvendorの修正・platform条件を確認してください。全件と入力・DBの証跡は同runのpinned-evidence artifactを参照してください。'
    return result
  }
  const d = entry.data, labels = { 'scan-error': '検査エラー', coverage: '検査範囲の不足', review: 'Advisory要確認', update: '更新候補' }
  const marker = `<!-- omni-security-monitor:v1:${entry.key} -->`, revision = `<!-- omni-security-monitor:v1:revision:${entry.digest} -->`
  const detail = Array.isArray(d.detail) ? `${d.detail.length}件。以下は先頭20件です。全件は検査reportを確認してください。\n` + d.detail.slice(0, 20).map(safe).join('\n') : safe(d.detail)
  return { marker, revision, title: `[Security: ${labels[d.kind]}] 固定入力 ${safe(d.component)}`.slice(0, 240),
    body: `${marker}\n${revision}\n\n対象: ${safe(d.component)}\n\n${detail}\n\n${d.kind === 'update' ? `使用版: ${safe(d.installed)}\n候補版: ${safe(d.latest)}\n\n` : ''}[検査ログ・証跡](${runURL})\n\n同じ状態の再通知は行いません。差分をコメントに記録し、自動close・再open・version切替は行いません。` }
}

export function reportSummary(report) {
  const rows = report.results.map(result => `| ${safe(result.name)} | ${safe(result.status)} | ${result.findings.length} | ${result.gaps?.length || 0} | ${result.reviews?.length || 0} | ${result.updates?.length || 0} |`)
  return `## 固定入力の検知結果: ${safe(report.group)}\n\nこれは通知用の収集結果です。job成功は脆弱性なしを意味しません。partialは未照合範囲あり、errorは検査の実行不備です。既存の配布用security gateは別途必須です。\n\n| 対象 | 状態 | 脆弱性候補 | 未照合 | 要確認 | 更新候補 |\n| --- | --- | ---: | ---: | ---: | ---: |\n${rows.join('\n')}\n\n全件は同runのpinned-report / pinned-evidence artifactを参照してください。mainの定期・手動実行でIssueへ通知します。PRでは書込権限を与えず、このsummaryとartifactに記録します。\n`
}

export function requireCollected(report) {
  assert.ok(report.results.every(x => x.status !== 'error'), 'Pinned input collection failed; see reports and separate scan-error issues')
}

export async function publishReports(env = process.env, apiFactory = githubClient, loadInventory = inventory) {
  assert.ok(['schedule', 'workflow_dispatch'].includes(env.GITHUB_EVENT_NAME) && env.GITHUB_REF === 'refs/heads/main' &&
    env.GITHUB_REPOSITORY === 'ShiningWank0/Omni_Money', 'Issue creation requires scheduled/manual main')
  const api = apiFactory(env.GITHUB_TOKEN, env.GITHUB_REPOSITORY)
  const runURL = `https://github.com/${env.GITHUB_REPOSITORY}/actions/runs/${env.GITHUB_RUN_ID}/attempts/${env.GITHUB_RUN_ATTEMPT}`
  const options = { respectClosed: true, deferOverflow: true, scope: 'pinned-inputs' }
  const monitor = env.CI_MONITOR_NEEDS_JSON ? await import('./ci-monitor.mjs') : null
  let jobEntries = []
  if (monitor) {
    try { jobEntries = await monitor.monitorJobs(api, env) }
    catch { jobEntries = [monitor.collectionErrorEntry(env)] }
  }
  const renderer = (entry, url) => entry.data.kind.startsWith('ci-') ? monitor.renderMonitor(entry, url) : renderPinned(entry, url)
  const deliver = async findings => {
    let pullRequests
    const items = [...findings, ...jobEntries]
    if (monitor) {
      try { pullRequests = await monitor.dependabotTargets(api, items) }
      catch {
        const issue = monitor.collectionErrorEntry(env)
        issue.data.component = 'Pinned input security/dependabot-prs'
        issue.data.detail = '既存のDependabot更新PRを確認できませんでした。検出した問題はIssueへ報告します。'
        issue.key = sha256(JSON.stringify(['ci-monitor', issue.data.component]))
        issue.digest = sha256(JSON.stringify(issue.data))
        items.push(issue)
      }
    }
    const result = await publish(api, items, runURL, renderer, { ...options, pullRequests })
    if (env.GITHUB_STEP_SUMMARY) appendFileSync(env.GITHUB_STEP_SUMMARY,
      `## Botによる固定入力監視の報告\n\n検出結果・検査不能を報告しました。監視成功は問題なしの判定ではありません。\n\n| 項目 | 件数 |\n| --- | ---: |\n| 報告対象 | ${items.length} |\n| Issue作成（集約を含む） | ${result.created} |\n| Issue・既存Dependabot PRへの更新 | ${result.updated} |\n| 集約Issueに保存した個別報告待ち | ${result.deferred} |\n`)
    return result
  }
  if (env.PINNED_TEST_OUTCOME !== 'success') {
    const failed = reportEntries([{ group: 'workflow', results: [{ name: 'validation', status: 'error', findings: [],
      error: '検査処理の回帰テストが失敗・中断・未実行です。脆弱性検査が完了していないため、先行jobを確認してください。' }] }])
    const result = await deliver(failed)
    console.log(JSON.stringify(result))
    return result
  }
  let input
  try { input = loadInventory() } catch {
    const failed = reportEntries([{ group: 'inventory', results: [{ name: 'configuration', status: 'error', findings: [],
      error: '固定入力・lock・moduleの構成を検証できません。新しい固定入力が検査対象に登録されていない可能性も含め、ログを確認してください。' }] }])
    const result = await deliver(failed)
    console.log(JSON.stringify(result))
    return result
  }
  const reports = []
  for (const group of groups) {
    try {
      assert.equal(env.PINNED_REPORT_DOWNLOAD_OUTCOME, 'success')
      reports.push(validateReport(readJSON(`security-reports/pinned-${group}.json`), input, group))
    } catch {
      reports.push({ group, results: [{ name: 'report', status: 'error', findings: [], error: '検査reportが欠落・不正、またはartifact取得に失敗しています。' }] })
    }
  }
  const result = await deliver(reportEntries(reports))
  console.log(JSON.stringify(result))
  // Reported inspection failures are a completed monitoring run. Scanner CLI
  // and PR/release enforcement continue to use requireCollected separately.
  return result
}

async function scan(group) {
  assert.ok(groups.includes(group) && process.env.GITHUB_ACTIONS === 'true' && process.platform === 'linux' && process.arch === 'x64')
  const input = inventory(), work = mkdtempSync(join(process.env.RUNNER_TEMP, 'pinned-security-'))
  const evidence = resolve('pinned-evidence'); mkdirSync(evidence, { recursive: true })
  let report
  try {
    const bin = join(work, 'bin'); mkdirSync(bin)
    const tools = group === 'modules' ? {} : group === 'native' ? { grype: input.monitor.grype, oras: input.monitor.oras }
      : { trivy: { ...input.trivy.platforms.x86_64, version: input.trivy.version } }
    for (const [name, item] of Object.entries(tools)) {
      const installed = await payload({ ...item, name: 'installer-' + name }, work, name)
      const binary = join(installed, name); assert.ok(existsSync(binary)); chmodSync(binary, 0o700)
      const link = join(bin, name)
      writeFileSync(link, readFileSync(binary), { mode: 0o700 })
      saveJSON(join(evidence, name + '-binary.json'), { version: item.version, archiveSHA256: item.sha256, binarySHA256: sha256(readFileSync(binary)) })
    }
    const env = scanEnvironment(work, { ...process.env, PATH: bin + ':' + process.env.PATH })
    const grypeConfig = join(evidence, 'grype-config.yaml'), trivyConfig = join(evidence, 'trivy-config.yaml')
    saveJSON(grypeConfig, { ignore: [], 'check-for-app-update': false, match: { stock: { 'using-cpes': true } }, db: { 'auto-update': false } })
    saveJSON(trivyConfig, { severity: ['UNKNOWN', 'LOW', 'MEDIUM', 'HIGH', 'CRITICAL'], 'ignore-unfixed': false })
    env.TRIVY_IGNORE_UNFIXED = 'false'; env.TRIVY_SEVERITY = 'UNKNOWN,LOW,MEDIUM,HIGH,CRITICAL'
    const execute = (program, args) => command(program,
      program === 'grype' ? ['--config', grypeConfig, ...args] : program === 'trivy' ? ['--config', trivyConfig, ...args] : args, { env })
    if (group === 'modules') {
      assert.equal(command('go', ['env', 'GOVERSION'], { env: { ...env, GOTOOLCHAIN: 'local' } }).trim(), 'go' + input.goVersion)
      // Inspect exact copied manifests; neither graph loading nor remote tool
      // metadata downloads may rewrite the checked-out application/tool locks.
      const graphRoot = join(work, 'module-graphs'); mkdirSync(graphRoot)
      for (const item of input.modules) for (const path of [item.path, item.sum]) {
        mkdirSync(dirname(join(graphRoot, path)), { recursive: true })
        writeFileSync(join(graphRoot, path), readFileSync(join(root, path)))
      }
      const selected = await auditModules(input, (args, cwd) => command('go', args, { cwd, env: { ...env, GOTOOLCHAIN: 'local', GOWORK: 'off' } }), async queries => {
        const findings = []
        for (let start = 0; start < queries.length; start += 100) {
          const batch = queries.slice(start, start + 100)
          const response = await fetch('https://api.osv.dev/v1/querybatch', { method: 'POST', headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ queries: batch }), signal: AbortSignal.timeout(90_000) })
          assert.ok(response.ok, 'OSV request failed')
          findings.push(...osvBatch(await response.json(), batch))
        }
        return findings
      }, graphRoot)
      assert.equal(inventory().identity, input.identity, 'module auditing changed reviewed source inputs')
      saveJSON(join(evidence, 'selected-go-modules.json'), selected)
      report = { results: selected.map(x => x.error ? { name: x.name, status: 'error', findings: [], error: x.error }
        : { name: x.name, status: x.vulnerabilities.length ? 'findings' : 'clean', findings: x.vulnerabilities.map(v =>
          cve('pinned-osv', v.id, `${x.name}/${v.name}`, v.version, '', 'UNKNOWN', 'OSV selected module version',
            '選択中のGo module版に対する既知advisoryです。到達可能性の判定とは区別してください。')) }) }
    } else if (group === 'native') {
      execute('grype', ['db', 'update'])
      writeFileSync(join(evidence, 'grype-db.json'), execute('grype', ['db', 'status', '--output', 'json']))
      report = { results: await scanNative(input, execute, work, evidence) }
    } else {
      execute('trivy', ['image', '--download-db-only'])
      writeFileSync(join(evidence, 'trivy-db.json'), execute('trivy', ['version', '--format', 'json']))
      report = { results: group === 'images' ? await scanImages(input, execute, evidence) : await scanTools(input, execute, work, evidence) }
    }
    Object.assign(report, { schema: 1, group, inputIdentity: input.identity })
    validateReport(report, input, group)
  } finally {
    if (report) saveJSON(`security-reports/pinned-${group}.json`, report)
    rmSync(work, { recursive: true, force: true })
  }
  // Collection and vulnerability policy are distinct. Keep every candidate and
  // known coverage gap in the reports; operational errors must still fail.
  for (const result of report.results) console.log(JSON.stringify({ target: result.name, status: result.status,
    findings: result.findings.length, coverageGaps: result.gaps?.length || 0,
    advisoryReviews: result.reviews?.length || 0, updates: result.updates?.length || 0 }))
  if (process.env.GITHUB_STEP_SUMMARY) appendFileSync(process.env.GITHUB_STEP_SUMMARY, reportSummary(report))
  requireCollected(report)
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const task = process.argv[2] === 'publish' ? publishReports() : scan(process.argv[2])
  task.catch(error => { console.error(error.message); process.exitCode = 1 })
}
