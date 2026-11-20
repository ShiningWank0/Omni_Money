import assert from 'node:assert/strict';
import { appendFileSync, lstatSync, mkdirSync, readdirSync, readFileSync, writeFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
import { collect, entries, githubClient, publish, render } from './security-issues.mjs';
import { hash } from './security-reports.mjs';

const repository = 'ShiningWank0/Omni_Money';
const prefix = 'omni-security-monitor:v1';
const clean = value => String(value ?? '').slice(0, 1000).replaceAll('@', '@\u200b')
  .replace(/[&<>"'`\[\]\\|*_\r\n]/g, c => `&#${c.charCodeAt(0)};`);
const requiredJobs = {
  oss: ['OSS licenses'],
  'go-checks': ['Go checks'],
  frontend: ['Frontend build'],
  'safe-update': [0, 1, 2, 3].map(shard => `Safe-update full matrix (${shard})`),
  'windows-security': ['Windows security boundaries'],
  docker: ['Docker image build'],
  'security-automation': ['Security issue reporting tests'],
  'parser-fuzz': ['fuzz-csv', 'fuzz-json', 'fuzz-image'].map(target => `Parser fuzz (${target})`),
  passkey: ['Passkey regression tests'],
};
const pinnedJobs = {
  tests: ['Validate pinned input monitoring'],
  scan: ['images', 'tools', 'native', 'modules'].map(group => `Collect saved input advisories (${group})`),
};

function checkContext(env) {
  assert.ok(['schedule', 'workflow_dispatch'].includes(env.GITHUB_EVENT_NAME) &&
    env.GITHUB_REF === 'refs/heads/main' && env.GITHUB_REPOSITORY === repository,
  'Monitoring publication is restricted to scheduled/manual main runs');
  assert.match(env.GITHUB_RUN_ID || '', /^[1-9][0-9]*$/);
  assert.match(env.GITHUB_RUN_ATTEMPT || '', /^[1-9][0-9]*$/);
}

function jobEntry(workflow, name, status, failedSteps = [], skippedSteps = [], jobID) {
  const data = { kind: 'ci-failure', tool: 'ci', component: `${workflow}/${name}`,
    status, failedSteps, skippedSteps };
  return { key: hash(['ci-monitor', data.component]), digest: hash(data), data,
    ...(jobID === undefined ? {} : { jobID }) };
}

// Fetch this attempt only: successful retries must not inherit older failures.
export function readStepReports(directory, env) {
  let paths;
  try { paths = readdirSync(directory).filter(path => /^ci-steps-[a-zA-Z0-9_-]+\.json$/.test(path)); }
  catch (error) { if (error.code === 'ENOENT') return []; throw error; }
  return paths.map(path => {
    const file = resolve(directory, path), stat = lstatSync(file);
    assert.ok(stat.isFile() && stat.size <= 2 * 1024 * 1024, 'Invalid step evidence file');
    return JSON.parse(readFileSync(file, 'utf8'));
  });
}

function reportJobName(report, required) {
  if (!Object.hasOwn(required, report.job)) return null;
  if (report.job === 'safe-update') return `Safe-update full matrix (${report.matrix.shard})`;
  if (report.job === 'parser-fuzz') return `Parser fuzz (${report.matrix.target})`;
  if (report.job === 'scan') return `Collect saved input advisories (${report.matrix.group})`;
  return required[report.job][0];
}

export async function monitorJobs(api, env, options = {}) {
  checkContext(env);
  assert.equal(env.SECURITY_REPORT_DOWNLOAD_OUTCOME || env.PINNED_REPORT_DOWNLOAD_OUTCOME, 'success',
    'Monitoring evidence download did not succeed');
  const needs = JSON.parse(env.CI_MONITOR_NEEDS_JSON || 'null');
  assert.ok(needs && typeof needs === 'object' && !Array.isArray(needs), 'Missing monitoring job results');
  const required = env.CI_MONITOR_REPORT_JOB === 'notify' ? pinnedJobs : requiredJobs;
  const workflow = env.GITHUB_WORKFLOW || (required === pinnedJobs ? 'Pinned input security' : 'CI');
  const stepReports = options.stepReports || readStepReports(options.directory || resolve('security-reports'), env);
  const recorded = new Map();
  for (const report of stepReports) {
    assert.ok(report.schema === 1 && report.runID === env.GITHUB_RUN_ID && report.attempt === env.GITHUB_RUN_ATTEMPT,
      'Step evidence is not from this run attempt');
    assert.ok(report.matrix && typeof report.matrix === 'object' && !Array.isArray(report.matrix) && Array.isArray(report.steps), 'Invalid step evidence');
    const name = reportJobName(report, required);
    assert.ok(name && required[report.job].includes(name) && !recorded.has(name), 'Unexpected or duplicate step evidence');
    assert.ok(report.steps.length > 0 && report.steps.every(step => typeof step.id === 'string' &&
      typeof step.name === 'string' && step.name.length <= 1000 &&
      ['success', 'failure', 'cancelled', 'skipped'].includes(step.outcome) &&
      ['success', 'failure', 'cancelled', 'skipped'].includes(step.conclusion)), 'Invalid recorded outcome');
    recorded.set(name, report);
  }
  const jobs = [];
  let total;
  for (let page = 1; ; page++) {
    assert.ok(page <= 100, 'Workflow job pagination limit reached');
    const batch = await api('GET', `actions/runs/${env.GITHUB_RUN_ID}/attempts/${env.GITHUB_RUN_ATTEMPT}/jobs?per_page=100&page=${page}`);
    assert.ok(Number.isSafeInteger(batch?.total_count) && batch.total_count >= 0 && Array.isArray(batch.jobs),
      'Invalid workflow job response');
    if (total === undefined) total = batch.total_count;
    assert.equal(batch.total_count, total, 'Workflow job inventory changed during collection');
    jobs.push(...batch.jobs);
    if (batch.jobs.length < 100) break;
  }
  assert.equal(jobs.length, total, 'Incomplete workflow job inventory');
  assert.equal(new Set(jobs.map(job => job.id)).size, jobs.length, 'Duplicate workflow job');
  for (const job of jobs) {
    assert.ok(Number.isSafeInteger(job.id) && job.id > 0 && typeof job.name === 'string', 'Invalid workflow job identity');
    assert.ok(['queued', 'in_progress', 'completed', 'waiting', 'pending', 'requested'].includes(job.status), 'Invalid workflow job status');
  }
  const result = [];
  for (const [id, names] of Object.entries(required)) {
    assert.ok(needs[id] && ['success', 'failure', 'cancelled', 'skipped'].includes(needs[id].result),
      `Missing expected monitoring job: ${id}`);
    for (const name of names) {
      const matches = jobs.filter(job => job.name === name);
      assert.ok(matches.length <= 1, 'Ambiguous workflow job name');
      const job = matches[0];
      if (!job) {
        // GitHub can omit an entire skipped matrix from the jobs response.
        result.push(jobEntry(workflow, name, needs[id].result === 'skipped' ? 'skipped' : 'missing'));
        continue;
      }
      assert.equal(job.status, 'completed', 'A required diagnostic job has not completed');
      assert.ok(Array.isArray(job.steps) || job.steps === null, 'Invalid workflow step inventory');
      const steps = job.steps || [];
      assert.ok(steps.every(step => typeof step.name === 'string' && typeof step.conclusion === 'string'), 'Invalid workflow step');
      const evidence = recorded.get(name);
      const rawFailed = evidence?.steps.filter(step => ['failure', 'cancelled'].includes(step.outcome)) || [];
      const failed = steps.filter(step => ['failure', 'cancelled', 'timed_out', 'action_required', 'stale', 'startup_failure'].includes(step.conclusion));
      const failure = failed.length > 0 || !['success', 'neutral'].includes(job.conclusion);
      if (!failure && !rawFailed.length && evidence) continue;
      if (!failure && !rawFailed.length && !evidence) {
        result.push(jobEntry(workflow, name, 'incomplete-step-results', [], [], job.id));
        continue;
      }
      const firstFailure = failed[0];
      const skipped = firstFailure ? steps.filter(step => step.conclusion === 'skipped' &&
        Number.isSafeInteger(step.number) && step.number > firstFailure.number && !step.name.startsWith('Post ')) : [];
      result.push(jobEntry(workflow, name, failed.length || rawFailed.length ? 'failure' : job.conclusion || 'unknown',
        [...new Set([...rawFailed.map(step => step.name), ...failed.map(step => step.name)])], skipped.map(step => step.name), job.id));
    }
  }
  return result;
}

export function collectionErrorEntry(env) {
  const data = { kind: 'ci-collection-error', tool: 'ci', component: `${env.GITHUB_WORKFLOW || 'CI'}/job-results`,
    detail: '検査・テストの実行結果を完全に取得できませんでした。未検査を問題なしとして扱わず、Actionsの各jobと監視設定を確認してください。' };
  return { key: hash(['ci-monitor', data.component]), digest: hash(data), data };
}

// Reuse an existing Dependabot proposal when its dependency metadata or
// advisory identity matches exactly. Unrelated PR/test failures stay in Issues.
export async function dependabotTargets(api, findings) {
  const candidates = findings.filter(entry => entry.data.kind === 'vulnerability');
  const targets = new Map();
  if (!candidates.length) return targets;
  const pulls = [];
  for (let page = 1; ; page++) {
    assert.ok(page <= 100, 'Dependabot PR pagination limit reached');
    const batch = await api('GET', `pulls?state=open&base=main&per_page=100&page=${page}`);
    assert.ok(Array.isArray(batch), 'Invalid Dependabot PR response');
    pulls.push(...batch.filter(pr => pr.user?.login === 'dependabot[bot]' && pr.state === 'open' &&
      pr.base?.ref === 'main' && pr.head?.repo?.full_name === repository && Number.isSafeInteger(pr.number) && pr.number > 0));
    if (batch.length < 100) break;
  }
  pulls.sort((a, b) => a.number - b.number);
  for (const entry of candidates) {
    const component = entry.data.component.replace(/^.*\.mod\//, '');
    const identifiers = Array.isArray(entry.data.id) ? entry.data.id : [entry.data.id];
    const existing = pulls.find(pr => {
      const body = typeof pr.body === 'string' ? pr.body : '';
      const dependencies = [...body.matchAll(/^\s*-\s*dependency-name:\s*([^\r\n]+)$/gm)]
        .map(match => match[1].trim().replace(/^['"]|['"]$/g, ''));
      return dependencies.includes(component) || identifiers.some(id => typeof id === 'string' &&
        /^(?:CVE-\d{4}-\d+|GHSA-[a-z0-9-]+|GO-\d{4}-\d+)$/.test(id) &&
        new RegExp(`(?:^|[^A-Za-z0-9-])${id}(?:$|[^A-Za-z0-9-])`).test(body));
    });
    if (existing) targets.set(entry.key, existing);
  }
  return targets;
}

export function renderMonitor(entry, runURL) {
  if (!entry.data.kind.startsWith('ci-')) return render(entry, runURL);
  assert.match(runURL, /^https:\/\/github\.com\/ShiningWank0\/Omni_Money\/actions\/runs\/[1-9][0-9]*\/attempts\/[1-9][0-9]*$/);
  const { data: d, key, digest } = entry;
  const marker = `<!-- ${prefix}:${key} -->`, revision = `<!-- ${prefix}:revision:${digest} -->`;
  const details = d.kind === 'ci-collection-error' ? clean(d.detail)
    : `| 項目 | 結果 |\n| --- | --- |\n| 対象 | ${clean(d.component)} |\n| 状態 | ${clean(d.status)} |\n| 失敗した処理 | ${d.failedSteps.map(clean).join('<br>') || 'job開始失敗・未実行など（ログを確認）'} |\n| その後に実行されなかった処理 | ${d.skippedSteps.map(clean).join('<br>') || 'なし／job全体が未実行'} |`;
  const jobURL = Number.isSafeInteger(entry.jobID) && entry.jobID > 0
    ? runURL.replace(/\/attempts\/\d+$/, `/job/${entry.jobID}`) : runURL;
  return { marker, revision,
    title: `[Bot][CI: ${d.kind === 'ci-collection-error' ? '結果取得エラー' : '検査・テストの問題'}] ${clean(d.component)}`.slice(0, 240),
    body: `${marker}\n${revision}\n\nGitHub Actionsが自動作成した報告です。\n\n${details}\n\n[実行ログ](${jobURL})\n\n検出した問題はこのIssueで追跡します。監視処理の成功は、テスト成功や脆弱性なしを意味しません。同じ状態の再通知は行わず、変化をコメントに記録します。` };
}

export async function main(env = process.env, options = {}) {
  checkContext(env);
  const api = (options.apiFactory || githubClient)(env.GITHUB_TOKEN, env.GITHUB_REPOSITORY);
  const directory = options.directory || resolve('security-reports');
  const reports = (options.collectReports || collect)(directory, env.SECURITY_REPORT_DOWNLOAD_OUTCOME === 'success');
  let diagnostics;
  try { diagnostics = await monitorJobs(api, env, { directory, stepReports: options.stepReports }); }
  catch { diagnostics = [collectionErrorEntry(env)]; }
  const findings = [...entries(reports), ...diagnostics];
  let pullRequests;
  try { pullRequests = await dependabotTargets(api, findings); }
  catch {
    const entry = collectionErrorEntry(env);
    entry.data.component = `${env.GITHUB_WORKFLOW || 'CI'}/dependabot-prs`;
    entry.data.detail = '既存のDependabot更新PRを確認できませんでした。検出した問題はIssueへ報告します。';
    entry.key = hash(['ci-monitor', entry.data.component]); entry.digest = hash(entry.data);
    findings.push(entry);
  }
  const runURL = `https://github.com/${repository}/actions/runs/${env.GITHUB_RUN_ID}/attempts/${env.GITHUB_RUN_ATTEMPT}`;
  const publication = await publish(api, findings, runURL, renderMonitor,
    { respectClosed: true, deferOverflow: true, scope: 'ci-monitor', pullRequests });
  const summary = { schema: 1, status: 'reported',
    scansWithFindings: reports.filter(report => report.status === 'findings').length,
    incompleteScans: reports.filter(report => report.status === 'error').length,
    diagnosticProblems: diagnostics.length, publication };
  mkdirSync(directory, { recursive: true });
  writeFileSync(resolve(directory, 'ci-monitor.json'), `${JSON.stringify({ ...summary, reports, diagnostics }, null, 2)}\n`);
  const markdown = `## Botによる定期監視の報告結果\n\n監視結果をIssueへ報告しました。検査対象に問題がないという判定ではありません。\n\n| 項目 | 件数 |\n| --- | ---: |\n| 脆弱性等を検出した検査 | ${summary.scansWithFindings} |\n| 不完全・実行不能な検査 | ${summary.incompleteScans} |\n| 検査・テストの問題 | ${summary.diagnosticProblems} |\n| Issue作成（集約を含む） | ${publication.created} |\n| Issue・既存Dependabot PRへの更新 | ${publication.updated} |\n| 集約Issueに保存した個別報告待ち | ${publication.deferred || 0} |\n`;
  if (env.GITHUB_STEP_SUMMARY) appendFileSync(env.GITHUB_STEP_SUMMARY, markdown);
  console.log(JSON.stringify(summary));
  return summary;
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  main().catch(error => { console.error(error.message); process.exitCode = 1; });
}
