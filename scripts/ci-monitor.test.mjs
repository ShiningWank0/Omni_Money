import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { dependabotTargets, main, monitorJobs, renderMonitor } from './ci-monitor.mjs';
import { main as recordSteps, stepReport } from './ci-monitor-record.mjs';
import { scans } from './security-reports.mjs';

const bot = { login: 'github-actions[bot]' };
const expected = {
  oss: ['OSS licenses'], 'go-checks': ['Go checks'], frontend: ['Frontend build'],
  'safe-update': [0, 1, 2, 3].map(shard => `Safe-update full matrix (${shard})`),
  'windows-security': ['Windows security boundaries'], docker: ['Docker image build'],
  'security-automation': ['Security issue reporting tests'],
  'parser-fuzz': ['fuzz-csv', 'fuzz-json', 'fuzz-image'].map(target => `Parser fuzz (${target})`),
  passkey: ['Passkey regression tests'],
};
const needs = Object.fromEntries(Object.keys(expected).map(key => [key, { result: 'success' }]));
const environment = { GITHUB_EVENT_NAME: 'schedule', GITHUB_REF: 'refs/heads/main',
  GITHUB_REPOSITORY: 'ShiningWank0/Omni_Money', GITHUB_WORKFLOW: 'CI',
  GITHUB_RUN_ID: '123', GITHUB_RUN_ATTEMPT: '2', GITHUB_TOKEN: 'fixture-token',
  CI_MONITOR_NEEDS_JSON: JSON.stringify(needs), CI_MONITOR_REPORT_JOB: 'security-issues',
  SECURITY_REPORT_DOWNLOAD_OUTCOME: 'success' };
const runURL = 'https://github.com/ShiningWank0/Omni_Money/actions/runs/123/attempts/2';
const fixtures = () => Object.values(expected).flat().map((name, index) => ({ id: index + 1, name,
  status: 'completed', conclusion: 'success', steps: [{ number: 1, name: 'Check', conclusion: 'success' }] }));
const cleanReports = () => scans.map(scan => ({ schema: 1, scan, status: 'clean', findings: [] }));
function recorded(jobs = fixtures(), mapping = expected) {
  return jobs.flatMap(job => {
    const id = Object.keys(mapping).find(id => mapping[id].includes(job.name));
    if (!id || !job.steps?.length) return [];
    const matrix = id === 'safe-update' ? { shard: Number(job.name.match(/\((\d)\)/)[1]) }
      : id === 'parser-fuzz' ? { target: job.name.match(/\(([^)]+)\)/)[1] }
        : id === 'scan' ? { group: job.name.match(/\(([^)]+)\)/)[1] } : {};
    return [{ schema: 1, runID: '123', attempt: '2', job: id, matrix,
      steps: job.steps.map((step, index) => ({ id: `step${index}`, name: step.name,
        outcome: step.conclusion, conclusion: step.conclusion })) }];
  });
}

function apiFixture(jobs) {
  const state = { calls: [], writes: [], issues: [] };
  const api = async (method, path, body) => {
    state.calls.push({ method, path });
    if (method === 'GET' && path.startsWith('actions/runs/123/attempts/2/jobs?')) {
      const page = Number(new URL(`https://fixture.test/${path}`).searchParams.get('page'));
      return { total_count: jobs.length, jobs: structuredClone(jobs.slice((page - 1) * 100, page * 100)) };
    }
    if (method === 'GET' && path.startsWith('issues?')) return structuredClone(state.issues);
    if (method === 'GET' && /^issues\/\d+\/comments\?/.test(path)) return [];
    if (method === 'POST') {
      state.writes.push({ path, body });
      if (path === 'issues') {
        const issue = { number: state.issues.length + 1, user: bot, state: 'open', ...body };
        state.issues.push(issue); return issue;
      }
      return {};
    }
    assert.fail(`Unexpected API request ${method} ${path}`);
  };
  return { state, api };
}

test('continued audit/test failures remain visible even when jobs report success', async () => {
  const jobs = fixtures();
  const frontend = jobs.find(job => job.name === 'Frontend build');
  frontend.steps = [
    { number: 1, name: 'Audit dependencies', conclusion: 'failure' },
    { number: 2, name: 'Test frontend security boundaries', conclusion: 'failure' },
    { number: 3, name: 'Build frontend', conclusion: 'success' },
  ];
  const { api } = apiFixture(jobs);
  const evidence = recorded(jobs);
  // GitHub's post-continue-on-error conclusion can be success in both APIs.
  frontend.steps[0].conclusion = 'success';
  frontend.steps[1].conclusion = 'success';
  const result = await monitorJobs(api, environment, { stepReports: evidence });
  assert.equal(result.length, 1);
  assert.equal(result[0].data.status, 'failure');
  assert.deepEqual(result[0].data.failedSteps, ['Audit dependencies', 'Test frontend security boundaries']);
  assert.deepEqual(result[0].data.skippedSteps, []);
  const view = renderMonitor(result[0], runURL);
  assert.match(view.title, /^\[Bot\]/);
  assert.match(view.body, /GitHub Actionsが自動作成/);
  assert.match(view.body, /\/job\/3\)/);
});

test('prerequisite failure reports downstream unexecuted steps, while independent jobs remain collected', async () => {
  const jobs = fixtures();
  const go = jobs.find(job => job.name === 'Go checks');
  go.conclusion = 'failure';
  go.steps = [
    { number: 1, name: 'Setup Go', conclusion: 'failure' },
    { number: 2, name: 'Run Go tests', conclusion: 'skipped' },
    { number: 3, name: 'Run Go vet', conclusion: 'skipped' },
    { number: 4, name: 'Post Checkout', conclusion: 'skipped' },
  ];
  const fuzz = jobs.find(job => job.name === 'Parser fuzz (fuzz-json)');
  fuzz.conclusion = 'cancelled'; fuzz.steps = null;
  const { api } = apiFixture(jobs);
  const result = await monitorJobs(api, environment, { stepReports: recorded(jobs) });
  assert.equal(result.length, 2);
  assert.deepEqual(result[0].data.skippedSteps, ['Run Go tests', 'Run Go vet']);
  assert.equal(result[1].data.status, 'cancelled');
});

test('an omitted skipped matrix is reported and intentional release skips do not create issues', async () => {
  const jobs = fixtures().filter(job => !job.name.startsWith('Safe-update full matrix'));
  for (const name of ['Desktop release', 'Docker release', 'Go tests', 'Plan release builds']) {
    jobs.push({ id: jobs.length + 100, name, status: 'completed', conclusion: 'skipped', steps: null });
  }
  const { api } = apiFixture(jobs);
  const result = await monitorJobs(api, { ...environment,
    CI_MONITOR_NEEDS_JSON: JSON.stringify({ ...needs, 'safe-update': { result: 'skipped' } }) }, { stepReports: recorded(jobs) });
  assert.equal(result.length, 4);
  assert.ok(result.every(entry => entry.data.status === 'skipped' && entry.data.component.includes('Safe-update')));
});

test('pinned input validation failure and unexecuted groups use the same reporting contract', async () => {
  const jobs = [{ id: 100, name: 'Validate pinned input monitoring', status: 'completed', conclusion: 'success',
    steps: [{ number: 1, name: 'Test detection', conclusion: 'failure' }] }];
  const { api } = apiFixture(jobs);
  const result = await monitorJobs(api, { ...environment, GITHUB_WORKFLOW: 'Pinned input security',
    CI_MONITOR_REPORT_JOB: 'notify', CI_MONITOR_NEEDS_JSON: JSON.stringify({ tests: { result: 'success' }, scan: { result: 'skipped' } }) },
    { stepReports: recorded(jobs, { tests: ['Validate pinned input monitoring'] }) });
  assert.equal(result.length, 5);
  assert.equal(result[0].data.failedSteps[0], 'Test detection');
  assert.ok(result.slice(1).every(entry => entry.data.status === 'skipped'));
});

test('latest-attempt job collection paginates and rejects incomplete or ambiguous inventories', async () => {
  const jobs = fixtures();
  for (let i = 0; i < 105; i++) jobs.push({ id: 1000 + i, name: `Unrelated ${i}`, status: 'completed', conclusion: 'success' });
  const { api, state } = apiFixture(jobs);
  assert.deepEqual(await monitorJobs(api, environment, { stepReports: recorded(jobs) }), []);
  assert.ok(state.calls.some(call => call.path.endsWith('page=2')));
  assert.ok(state.calls.every(call => call.path.startsWith('actions/runs/123/attempts/2/jobs?')));
  await assert.rejects(monitorJobs(async () => ({ total_count: 100, jobs: fixtures() }), environment, { stepReports: [] }), /Incomplete/);
  const duplicate = [...fixtures(), { ...fixtures()[0], id: 400 }];
  await assert.rejects(monitorJobs(apiFixture(duplicate).api, environment, { stepReports: [] }), /Ambiguous/);
  await assert.rejects(monitorJobs(apiFixture([...fixtures(), fixtures()[0]]).api, environment, { stepReports: [] }), /Duplicate/);
});

test('issue identity ignores run IDs and renders API-provided names as text', async () => {
  const jobs = fixtures();
  jobs[0].steps[0] = { number: 1, name: '@everyone <script> <SCRIPT> <ScRiPt> [click](https://evil.test) <!-- marker -->', conclusion: 'failure' };
  const [entry] = await monitorJobs(apiFixture(jobs).api, environment, { stepReports: recorded(jobs) });
  const first = renderMonitor(entry, runURL);
  const second = renderMonitor(entry, runURL.replace('/123/', '/124/'));
  assert.equal(first.revision, second.revision);
  const lowerCaseBody = first.body.toLowerCase();
  for (const payload of ['@everyone', '<script>', '[click]', '<!-- marker -->']) {
    assert.ok(!lowerCaseBody.includes(payload), `Unsafe report content: ${payload}`);
  }
  assert.throws(() => renderMonitor(entry, 'https://evil.test/actions/runs/123/attempts/2'), /match/);
});

test('a scheduled scan with findings, incomplete reports and test failures completes after Bot publication', async t => {
  const directory = mkdtempSync(join(tmpdir(), 'omni-ci-monitor-'));
  t.after(() => rmSync(directory, { recursive: true, force: true }));
  const jobs = fixtures();
  jobs[0].steps[0].conclusion = 'failure';
  const { api, state } = apiFixture(jobs);
  const reports = cleanReports();
  reports[0] = { schema: 1, scan: 'npm', status: 'error', findings: [] };
  reports[1] = { schema: 1, scan: 'gosec-desktop', status: 'findings', findings: [
    { tool: 'gosec', id: 'G401', component: 'backend/example.go:4', severity: 'MEDIUM',
      installed: '', fixed: '', summary: 'Fixture', context: 'gosec-desktop' },
  ] };
  const summary = await main(environment, { directory, apiFactory: () => api, collectReports: () => reports, stepReports: recorded(jobs) });
  assert.equal(summary.status, 'reported');
  assert.equal(summary.scansWithFindings, 1);
  assert.equal(summary.incompleteScans, 1);
  assert.equal(summary.diagnosticProblems, 1);
  assert.equal(state.writes.length, 3);
  assert.ok(state.writes.every(write => write.body.title.startsWith('[Bot]')));
  assert.equal(JSON.parse(readFileSync(join(directory, 'ci-monitor.json'), 'utf8')).diagnostics.length, 1);
});

test('collection errors are reported explicitly; publication errors still reject the monitor', async t => {
  const directory = mkdtempSync(join(tmpdir(), 'omni-ci-monitor-error-'));
  t.after(() => rmSync(directory, { recursive: true, force: true }));
  const { api, state } = apiFixture(fixtures());
  const broken = async (...args) => {
    if (args[1].startsWith('actions/')) throw new Error('service unavailable');
    return api(...args);
  };
  const summary = await main(environment, { directory, apiFactory: () => broken, collectReports: cleanReports });
  assert.equal(summary.diagnosticProblems, 1);
  assert.match(state.writes[0].body.title, /結果取得エラー/);
  const failing = fixtures(); failing[0].steps[0].conclusion = 'failure';
  await assert.rejects(main(environment, { directory, collectReports: cleanReports, stepReports: recorded(failing),
    apiFactory: () => async (method, path) => {
      if (path.startsWith('actions/')) return { total_count: fixtures().length, jobs: fixtures() };
      if (method === 'GET') return [];
      throw new Error('Issue publication unavailable');
    } }), /publication unavailable/);
});

test('missing, wrong-attempt and failed-download evidence cannot hide continued failures', async () => {
  const jobs = fixtures(), { api } = apiFixture(jobs);
  const missing = await monitorJobs(api, environment, { stepReports: [] });
  assert.equal(missing.length, jobs.length);
  assert.ok(missing.every(entry => entry.data.status === 'incomplete-step-results'));
  const stale = recorded(jobs); stale[0].attempt = '1';
  await assert.rejects(monitorJobs(api, environment, { stepReports: stale }), /not from this run/);
  await assert.rejects(monitorJobs(api, { ...environment, SECURITY_REPORT_DOWNLOAD_OUTCOME: 'failure' },
    { stepReports: recorded(jobs) }), /download did not succeed/);
});

test('recording preserves pre-continuation outcomes and excludes action outputs and secrets', () => {
  const result = stepReport({ ...environment, CI_MONITOR_JOB: 'frontend', CI_MONITOR_MATRIX: 'null',
    CI_MONITOR_STEP_NAMES: JSON.stringify({ audit: 'Audit dependencies' }),
    CI_MONITOR_STEP_RESULTS: JSON.stringify({ audit: { outcome: 'failure', conclusion: 'success',
      outputs: { token: 'must-not-persist' } } }) });
  assert.deepEqual(result.steps, [{ id: 'audit', name: 'Audit dependencies', outcome: 'failure', conclusion: 'success' }]);
  assert.ok(!JSON.stringify(result).includes('must-not-persist'));
  assert.throws(() => stepReport({ ...environment, CI_MONITOR_JOB: '../outside', CI_MONITOR_STEP_RESULTS: '{}' }));
});

test('recorded files preserve failures hidden by successful Actions conclusions', async t => {
  const directory = mkdtempSync(join(tmpdir(), 'omni-ci-step-evidence-'));
  t.after(() => rmSync(directory, { recursive: true, force: true }));
  const jobs = fixtures();
  for (const evidence of recorded(jobs)) {
    const values = Object.fromEntries(evidence.steps.map((step, index) => [step.id, {
      outcome: evidence.job === 'frontend' && index === 0 ? 'failure' : 'success',
      conclusion: 'success', outputs: { credential: 'not-public-evidence' },
    }]));
    recordSteps({ ...environment, CI_MONITOR_JOB: evidence.job, CI_MONITOR_MATRIX: JSON.stringify(evidence.matrix),
      CI_MONITOR_STEP_RESULTS: JSON.stringify(values) }, directory);
  }
  const entries = await monitorJobs(apiFixture(jobs).api, environment, { directory });
  assert.equal(entries.length, 1);
  assert.equal(entries[0].data.component, 'CI/Frontend build');
  assert.deepEqual(entries[0].data.failedSteps, ['step0']);
});

test('exact Dependabot dependency/advisory matches reuse Bot proposals without trusting human PRs', async () => {
  const finding = { key: 'fixture', data: { kind: 'vulnerability', component: 'go.mod/example.org/lib', id: ['GO-2099-1234'] } };
  const pull = number => ({ number, state: 'open', user: { login: 'dependabot[bot]' },
    base: { ref: 'main' }, head: { repo: { full_name: 'ShiningWank0/Omni_Money' } },
    body: 'updated-dependencies:\n- dependency-name: example.org/lib\n' });
  const human = { ...pull(1), user: { login: 'someone' } };
  const unrelated = { ...pull(2), body: '- dependency-name: example.org/library\n' };
  const target = pull(3);
  const api = async () => [human, unrelated, target];
  assert.equal((await dependabotTargets(api, [finding])).get('fixture').number, 3);
  assert.equal((await dependabotTargets(async () => [human, unrelated], [finding])).size, 0);
  assert.equal((await dependabotTargets(async () => [{ ...target, body: 'Fixes GO-2099-1234.' }], [finding])).size, 1);
});

test('PR, push, fork and non-main invocations cannot publish monitoring issues', async () => {
  for (const override of [{ GITHUB_EVENT_NAME: 'pull_request' }, { GITHUB_EVENT_NAME: 'push' },
    { GITHUB_REF: 'refs/heads/review' }, { GITHUB_REPOSITORY: 'someone/fork' }]) {
    await assert.rejects(main({ ...environment, ...override }, { apiFactory: () => assert.fail('Unexpected API') }), /restricted/);
  }
});
