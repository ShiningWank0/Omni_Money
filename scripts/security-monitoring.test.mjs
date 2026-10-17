import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { spawnSync } from 'node:child_process';
import { jsonStream, normalize, scans, fuzzReport, fuzzTargets } from './security-reports.mjs';
import { collect, entries, render, publish, githubClient, main } from './security-issues.mjs';

const runURL = 'https://github.com/ShiningWank0/Omni_Money/actions/runs/123/attempts/1';
const bot = { login: 'github-actions[bot]' };
const goConfig = { config: { protocol_version: 'v1.0.0', scan_level: 'symbol' } };
const goSBOM = { SBOM: { roots: ['example.org/app'], modules: [{ path: 'example.org/dependency' }] } };
const goOSV = { osv: { id: 'GO-2099-0001', summary: 'Example flaw' } };
const goFinding = { finding: { osv: 'GO-2099-0001', fixed_version: 'v1.2.3',
  trace: [{ module: 'example.org/dependency', version: 'v1.2.0', package: 'example.org/dependency', function: 'Parse' }] } };
const stream = messages => messages.map(m => JSON.stringify(m, null, 2)).join('\n');
const goInput = stream([goConfig, goSBOM, goOSV, goFinding]);
const npmInput = { auditReportVersion: 2, vulnerabilities: {
  lib: { name: 'lib', severity: 'high', range: '<2.0.0',
    fixAvailable: { name: 'lib', version: '2.0.0', isSemVerMajor: true },
    via: [{ source: 123, url: 'https://github.com/advisories/GHSA-abcd-1234-5678', severity: 'high', title: 'Example flaw' }] },
  parent: { severity: 'high', via: ['lib'] },
}, metadata: { vulnerabilities: { high: 2, critical: 0 } } };
const gosecInput = { Stats: { files: 3, found: 1 }, 'Golang errors': {}, Issues: [
  { severity: 'MEDIUM', rule_id: 'G401', file: '/repo/backend/file.go', line: '42', details: 'Weak primitive' },
] };
const trivyInput = { SchemaVersion: 2, ArtifactName: 'omni-money:ci', Results: [{ Type: 'alpine', Target: 'omni-money:ci', Vulnerabilities: [
  { VulnerabilityID: 'CVE-2099-0001', PkgName: 'libx', Severity: 'HIGH', InstalledVersion: '1.0', FixedVersion: '1.1', Title: 'Example flaw' },
  { VulnerabilityID: 'CVE-2099-0002', PkgName: 'libx', Severity: 'HIGH' },
  { VulnerabilityID: 'CVE-2099-0003', PkgName: 'libx', Severity: 'LOW', FixedVersion: '1.1' },
] }] };
const report = () => normalize('govulncheck-server', goInput, 0);
const entry = () => entries([report()])[0];

test('govulncheck JSON exit 0 still detects reachable symbols, not module/package-only findings', () => {
  const r = report();
  assert.equal(r.status, 'findings');
  assert.equal(r.findings[0].fixed, 'v1.2.3');
  for (const frame of [{ module: 'x' }, { module: 'x', package: 'x/y' }]) {
    const f = { finding: { osv: goOSV.osv.id, trace: [frame] } };
    assert.equal(normalize('govulncheck-desktop', stream([goConfig, goSBOM, goOSV, f]), 0).status, 'clean');
  }
});

test('govulncheck handles braces and escaped quotes in stream strings, rejects truncated streams', () => {
  assert.deepEqual(jsonStream(stream([{ message: 'a { b \\" [ ]' }, { progress: {} }])), [{ message: 'a { b \\" [ ]' }, { progress: {} }]);
  for (const input of ['', goInput.slice(0, -2), '{} garbage', '{}', stream([goConfig])]) {
    assert.equal(normalize('govulncheck-server', input, 0).status, 'error');
  }
});

test('npm includes advisory and major-version fix, deduplicates transitive via names', () => {
  const r = normalize('npm', JSON.stringify(npmInput), 1);
  assert.equal(r.status, 'findings');
  assert.equal(r.findings.length, 1);
  assert.equal(r.findings[0].id, 'GHSA-abcd-1234-5678');
  assert.match(r.findings[0].fixed, /2\.0\.0 \(major update\)/);
  const clean = { auditReportVersion: 2, vulnerabilities: {}, metadata: { vulnerabilities: { high: 0, critical: 0 } } };
  assert.equal(normalize('npm', JSON.stringify(clean), 0).status, 'clean');
  assert.equal(normalize('npm', JSON.stringify({ ...clean, error: { code: 'ENOAUDIT' } }), 1).status, 'error');
});

test('gosec reports medium findings and stable workspace-relative locations', () => {
  const r = normalize('gosec-server', JSON.stringify(gosecInput), 1, '/repo');
  assert.equal(r.status, 'findings');
  assert.equal(r.findings[0].component, 'backend/file.go:42');
  assert.equal(normalize('gosec-server', JSON.stringify({ ...gosecInput, 'Golang errors': { x: ['compile error'] } }), 1).status, 'error');
});

test('Trivy includes only fixable high/critical findings; action failure is a scan error', () => {
  const r = normalize('trivy', JSON.stringify(trivyInput), 0);
  assert.equal(r.status, 'findings');
  assert.equal(r.findings.length, 1);
  assert.equal(r.findings[0].fixed, '1.1');
  assert.equal(normalize('trivy', JSON.stringify(trivyInput), 1).status, 'error');
});

test('network errors, wrong schemas, killed tools and nonzero clean scans never become clean reports', () => {
  for (const scan of scans) {
    for (const input of ['', '{}', 'connection refused', JSON.stringify({ error: 'registry error' })]) {
      assert.equal(normalize(scan, input, 1).status, 'error');
    }
    assert.equal(normalize(scan, '', null).status, 'error');
  }
  assert.equal(normalize('govulncheck-server', goInput, 1).status, 'error');
  assert.equal(normalize('gosec-server', JSON.stringify({ Stats: { files: 1 }, Issues: [] }), 1).status, 'error');
});

test('missing, mismatched and malformed artifacts become individual scan-error entries', t => {
  const directory = mkdtempSync(join(tmpdir(), 'omni-security-test-'));
  t.after(() => rmSync(directory, { recursive: true, force: true }));
  for (const scan of scans) writeFileSync(join(directory, `${scan}.json`), JSON.stringify({ schema: 1, scan, status: 'clean', findings: [] }));
  assert.equal(entries(collect(directory)).length, 0);
  writeFileSync(join(directory, 'npm.json'), JSON.stringify({ schema: 1, scan: 'trivy', status: 'clean', findings: [] }));
  writeFileSync(join(directory, 'trivy.json'), '{');
  rmSync(join(directory, 'gosec-server.json'));
  const result = entries(collect(directory));
  assert.equal(result.length, 3);
  assert.ok(result.every(e => e.data.kind === 'scan-error'));
});

test('duplicates across Go build modes and call traces produce one issue with sorted contexts', () => {
  const desktop = normalize('govulncheck-desktop', goInput, 0);
  const result = entries([report(), desktop, report()]);
  assert.equal(result.length, 1);
  assert.deepEqual(result[0].data.context, ['govulncheck-desktop', 'govulncheck-server']);
  assert.deepEqual(result, entries([desktop, report()]));
});

test('multiple advisories for one package share one issue while gosec stays per rule', () => {
  const first = report();
  first.findings[0].id = 'GO-2099-0001';
  const second = report();
  second.findings[0].id = 'GO-2099-0002';
  const grouped = entries([first, second]);
  assert.equal(grouped.length, 1);
  assert.deepEqual(grouped[0].data.id, ['GO-2099-0001', 'GO-2099-0002']);
  assert.match(render(grouped[0], runURL).body, /GO-2099-0001, GO-2099-0002/);

  const gosec = normalize('gosec-server', JSON.stringify({ Stats: { files: 1, found: 2 }, 'Golang errors': {}, Issues: [
    { severity: 'MEDIUM', rule_id: 'G401', file: '/repo/backend/file.go', line: '42', details: 'a' },
    { severity: 'HIGH', rule_id: 'G402', file: '/repo/backend/file.go', line: '42', details: 'b' },
  ] }), 1, '/repo');
  assert.equal(entries([gosec]).length, 2);
});

test('failed artifact download or digest verification rejects even complete-looking files', t => {
  const directory = mkdtempSync(join(tmpdir(), 'omni-security-integrity-'));
  t.after(() => rmSync(directory, { recursive: true, force: true }));
  for (const scan of scans) writeFileSync(join(directory, `${scan}.json`), JSON.stringify({ schema: 1, scan, status: 'clean', findings: [] }));
  assert.equal(entries(collect(directory, true)).length, 0);
  const result = entries(collect(directory, false));
  assert.equal(result.length, scans.length);
  assert.ok(result.every(e => e.data.kind === 'scan-error'));
});

function mockAPI(initial = [], initialComments = []) {
  const state = { issues: structuredClone(initial), comments: structuredClone(initialComments), writes: [], calls: [] };
  const api = async (method, path, body) => {
    state.calls.push({ method, path });
    if (method === 'GET') {
      const page = Number(new URL(`https://example.test/${path}`).searchParams.get('page') || 1);
      const values = path.startsWith('issues?') ? state.issues.filter(i => i.state !== 'closed') : state.comments;
      return structuredClone(values.slice((page - 1) * 100, page * 100));
    }
    state.writes.push({ method, path, body });
    assert.equal(method, 'POST', 'existing issue state and user content must never be overwritten');
    if (path === 'issues') state.issues.push({ number: state.issues.length + 1, user: bot, state: 'open', ...body });
    else state.comments.push({ user: bot, ...body });
    return {};
  };
  return { state, api };
}

test('new finding opens once; unchanged and clean reruns produce no writes', async () => {
  const { api, state } = mockAPI();
  assert.deepEqual(await publish(api, [entry()], runURL), { created: 1, updated: 0, unchanged: 0 });
  assert.deepEqual(await publish(api, [entry()], runURL.replace('/123/', '/124/')), { created: 0, updated: 0, unchanged: 1 });
  await publish(api, [], runURL);
  assert.equal(state.writes.length, 1);
  assert.match(state.writes[0].body.body, /v1\.2\.3/);
});

test('changed fix triggers one comment, reruns stay quiet, regression to initial state notifies again', async () => {
  const first = entry();
  const { api, state } = mockAPI();
  await publish(api, [first], runURL);
  const changedReport = report();
  changedReport.findings[0].fixed = 'v1.2.4';
  const changed = entries([changedReport])[0];
  await publish(api, [changed], runURL);
  await publish(api, [changed], runURL);
  assert.equal(state.writes.length, 2);
  assert.equal(state.writes[1].path, 'issues/1/comments');
  await publish(api, [first], runURL);
  assert.equal(state.writes.length, 3);
});

test('closed issues remain closed; recurrence creates a new issue, human marker copies are ignored', async () => {
  const body = render(entry(), runURL).body;
  const { api, state } = mockAPI([
    { number: 1, state: 'closed', user: bot, body },
    { number: 2, state: 'open', user: { login: 'someone' }, body },
    { number: 3, state: 'open', user: bot, body, pull_request: {} },
  ]);
  await publish(api, [entry()], runURL);
  assert.equal(state.writes.length, 1);
  assert.equal(state.writes[0].path, 'issues');
  assert.equal(state.issues[0].state, 'closed');
});

test('pagination finds existing issue beyond first 100 and ignores forged human comments', async () => {
  const issues = Array.from({ length: 100 }, (_, i) => ({ number: i + 1, body: 'unrelated', user: bot }));
  issues.push({ number: 101, body: render(entry(), runURL).body, user: bot });
  const { api, state } = mockAPI(issues, [{ user: { login: 'someone' }, body: 'untrusted update' }]);
  assert.equal((await publish(api, [entry()], runURL)).unchanged, 1);
  assert.ok(state.calls.some(c => c.path.includes('page=2')));
  assert.equal(state.writes.length, 0);
});

test('batch limit bounds notification volume and fails visibly for remaining findings', async () => {
  const reports = Array.from({ length: 26 }, (_, i) => {
    const r = report(); r.findings[0].component = `example.org/dependency${i}`; return r;
  });
  const { api, state } = mockAPI();
  await assert.rejects(publish(api, entries(reports), runURL), /limit reached/);
  assert.equal(state.writes.length, 25);
  await publish(api, entries(reports), runURL);
  assert.equal(state.writes.length, 26);
});

test('report text cannot inject mentions, HTML, markdown links or state markers', () => {
  const r = report();
  r.findings[0].summary = '@everyone <img src=x> [click](https://evil.test) <!-- fake -->';
  const body = render(entries([r])[0], runURL).body;
  assert.ok(!body.includes('@everyone'));
  assert.ok(!body.includes('<img'));
  assert.ok(!body.includes('[click]'));
  assert.ok(!body.includes('<!-- fake -->'));
});

test('PR/push/fork events cannot publish and untrusted run URLs are rejected', async () => {
  for (const env of [
    { GITHUB_EVENT_NAME: 'pull_request', GITHUB_REF: 'refs/heads/main', GITHUB_REPOSITORY: 'ShiningWank0/Omni_Money' },
    { GITHUB_EVENT_NAME: 'push', GITHUB_REF: 'refs/heads/main', GITHUB_REPOSITORY: 'ShiningWank0/Omni_Money' },
    { GITHUB_EVENT_NAME: 'schedule', GITHUB_REF: 'refs/heads/main', GITHUB_REPOSITORY: 'someone/fork' },
    { GITHUB_EVENT_NAME: 'schedule', GITHUB_REF: 'refs/heads/other', GITHUB_REPOSITORY: 'ShiningWank0/Omni_Money' },
    { GITHUB_EVENT_NAME: 'workflow_dispatch', GITHUB_REF: 'refs/heads/other', GITHUB_REPOSITORY: 'ShiningWank0/Omni_Money' },
    { GITHUB_EVENT_NAME: 'workflow_dispatch', GITHUB_REF: 'refs/tags/v2.1.2', GITHUB_REPOSITORY: 'ShiningWank0/Omni_Money' },
    { GITHUB_EVENT_NAME: 'workflow_dispatch', GITHUB_REF: 'refs/heads/main', GITHUB_REPOSITORY: 'someone/fork' },
    { GITHUB_EVENT_NAME: 'repository_dispatch', GITHUB_REF: 'refs/heads/main', GITHUB_REPOSITORY: 'ShiningWank0/Omni_Money' },
  ]) await assert.rejects(main(env), /restricted/);
  for (const url of [
    'https://evil.test',
    'https://github.com.evil.test/ShiningWank0/Omni_Money/actions/runs/123/attempts/1',
    'https://evil.test/https://github.com/ShiningWank0/Omni_Money/actions/runs/123/attempts/1',
  ]) await assert.rejects(publish(() => assert.fail('unexpected request'), [], url), /invalid run URL/);
});

test('scheduled and manual main runs publish findings through the same CLI path', t => {
  const directory = mkdtempSync(join(tmpdir(), 'omni-security-dispatch-'));
  t.after(() => rmSync(directory, { recursive: true, force: true }));
  mkdirSync(join(directory, 'security-reports'));
  for (const scan of scans) {
    const value = scan === 'npm' ? normalize('npm', JSON.stringify(npmInput), 1)
      : { schema: 1, scan, status: 'clean', findings: [] };
    writeFileSync(join(directory, `security-reports/${scan}.json`), JSON.stringify(value));
  }
  const program = `
    import assert from 'node:assert/strict';
    import { main } from ${JSON.stringify(new URL('./security-issues.mjs', import.meta.url).href)};
    let writes = 0;
    globalThis.fetch = async (url, options) => {
      assert.equal(options.headers.Authorization, 'Bearer mock-token');
      if (options.method === 'GET') {
        assert.equal(url, 'https://api.github.com/repos/ShiningWank0/Omni_Money/issues?state=open&per_page=100&page=1');
        return { ok: true, json: async () => [] };
      }
      assert.equal(options.method, 'POST');
      assert.equal(url, 'https://api.github.com/repos/ShiningWank0/Omni_Money/issues');
      const body = JSON.parse(options.body);
      assert.match(body.title, /GHSA-abcd-1234-5678/);
      assert.ok(body.body.includes(${JSON.stringify(runURL)}));
      writes++;
      return { ok: true, json: async () => ({ number: 1 }) };
    };
    await main(process.env);
    assert.equal(writes, 1);
  `;
  for (const event of ['schedule', 'workflow_dispatch']) {
    const result = spawnSync(process.execPath, ['--input-type=module', '-e', program], {
      cwd: directory, encoding: 'utf8',
      env: { ...process.env, GITHUB_EVENT_NAME: event, GITHUB_REF: 'refs/heads/main',
        GITHUB_REPOSITORY: 'ShiningWank0/Omni_Money', GITHUB_RUN_ID: '123', GITHUB_RUN_ATTEMPT: '1',
        GITHUB_TOKEN: 'mock-token', GITHUB_STEP_SUMMARY: '', SECURITY_REPORT_DOWNLOAD_OUTCOME: 'success' },
    });
    assert.equal(result.status, 0, `${event}: ${result.stderr}`);
    assert.match(result.stdout, /"created":1,"updated":0,"unchanged":0/);
  }
});

test('workflow coverage runs the full safe-update matrix for scheduled and manual scans', t => {
  const directory = mkdtempSync(join(tmpdir(), 'omni-security-coverage-'));
  t.after(() => rmSync(directory, { recursive: true, force: true }));
  const workflow = readFileSync(resolve('.github/workflows/ci.yml'), 'utf8');
  const coverage = workflow.split('      - name: Determine full-matrix coverage\n')[1]
    ?.split('      - name: Run safe-update full matrix\n')[0];
  const block = coverage?.match(/        run: \|\n((?:          .*\n|\n)+)/)?.[1];
  assert.ok(block, 'workflow coverage script is missing');
  const program = block.replace(/^          /gm, '');
  for (const [event, ref, expected] of [
    ['schedule', 'refs/heads/main', 'true'],
    ['workflow_dispatch', 'refs/heads/main', 'true'],
    ['workflow_dispatch', 'refs/heads/review', 'true'],
    ['push', 'refs/heads/main', 'true'],
    ['push', 'refs/heads/review', 'false'],
  ]) {
    const output = join(directory, `${event}-${expected}-${ref.split('/').at(-1)}`);
    const result = spawnSync('bash', ['-c', program], {
      cwd: directory, encoding: 'utf8',
      env: { ...process.env, EVENT_NAME: event, REF: ref, PR_NUMBER: '', PR_BASE_SHA: '', PR_HEAD_SHA: '',
        GITHUB_OUTPUT: output },
    });
    assert.equal(result.status, 0, result.stderr);
    assert.equal(readFileSync(output, 'utf8'), `run_full=${expected}\n`);
  }
});

test('API uses fixed GitHub endpoint, disallows redirects and surfaces 403 without secrets', async () => {
  let request;
  const api = githubClient('secret-value', 'ShiningWank0/Omni_Money', async (url, options) => {
    request = { url, options }; return { ok: false, status: 403 };
  });
  await assert.rejects(api('POST', 'issues', { title: 'example' }), /^Error: GitHub API POST failed \(403\)$/);
  assert.equal(request.url, 'https://api.github.com/repos/ShiningWank0/Omni_Money/issues');
  assert.equal(request.options.redirect, 'error');
});

test('gosec CLI uses locked scanner dependencies and rejects incomplete scans in both modes', t => {
  for (const [input, exitCode, status] of [
    [{ Stats: { files: 1 }, 'Golang errors': {}, Issues: [] }, 0, 'clean'],
    [{ Stats: { files: 1 }, 'Golang errors': { 'main.go': ['unsupported export data version'] }, Issues: [] }, 1, 'error'],
    [gosecInput, 1, 'findings'],
  ]) {
    const directory = mkdtempSync(join(tmpdir(), 'omni-gosec-cli-'));
    t.after(() => rmSync(directory, { recursive: true, force: true }));
    mkdirSync(join(directory, 'bin'));
    writeFileSync(join(directory, 'bin/go'), `#!${process.execPath}\nconst fs = require('node:fs');\nfs.appendFileSync('scanner-args.jsonl', JSON.stringify(process.argv.slice(2)) + '\\n');\nprocess.stdout.write(process.env.MOCK_GOSEC_REPORT);\nprocess.exit(Number(process.env.MOCK_GOSEC_EXIT));\n`, { mode: 0o755 });
    const result = spawnSync(process.execPath, [resolve('scripts/security-reports.mjs'), 'gosec'], {
      cwd: directory, encoding: 'utf8',
      env: { ...process.env, PATH: `${join(directory, 'bin')}:${process.env.PATH}`,
        MOCK_GOSEC_REPORT: JSON.stringify(input), MOCK_GOSEC_EXIT: String(exitCode) },
    });
    assert.equal(result.status, status === 'clean' ? 0 : 1, result.stderr);
    const calls = readFileSync(join(directory, 'scanner-args.jsonl'), 'utf8').trim().split('\n').map(line => JSON.parse(line));
    const command = ['run', '-mod=readonly', `-modfile=${resolve('scripts/gosec.mod')}`,
      'github.com/securego/gosec/v2/cmd/gosec', '-terse', '-fmt=json', '-severity=medium'];
    assert.deepEqual(calls, [[...command, './...'], [...command, '-tags', 'server', './...']]);
    for (const mode of ['desktop', 'server']) {
      const report = JSON.parse(readFileSync(join(directory, `security-reports/gosec-${mode}.json`), 'utf8'));
      assert.equal(report.status, status);
    }
  }
});

test('CLI enforces failure on govulncheck JSON findings even when tool exits successfully', t => {
  const directory = mkdtempSync(join(tmpdir(), 'omni-security-cli-'));
  t.after(() => rmSync(directory, { recursive: true, force: true }));
  mkdirSync(join(directory, 'bin'));
  writeFileSync(join(directory, 'bin/go'), `#!${process.execPath}\nrequire('node:fs').appendFileSync('scanner-args.jsonl', JSON.stringify(process.argv.slice(2)) + '\\n');\nprocess.stdout.write(${JSON.stringify(goInput)});\n`, { mode: 0o755 });
  const result = spawnSync(process.execPath, [resolve('scripts/security-reports.mjs'), 'govulncheck'], {
    cwd: directory, env: { ...process.env, PATH: `${join(directory, 'bin')}:${process.env.PATH}` }, encoding: 'utf8',
  });
  assert.equal(result.status, 1);
  const calls = readFileSync(join(directory, 'scanner-args.jsonl'), 'utf8').trim().split('\n').map(line => JSON.parse(line));
  assert.deepEqual(calls, [
    ['run', 'golang.org/x/vuln/cmd/govulncheck@v1.6.0', '-json', './...'],
    ['run', 'golang.org/x/vuln/cmd/govulncheck@v1.6.0', '-json', '-tags', 'server', './...'],
  ]);
  for (const mode of ['desktop', 'server']) {
    const r = JSON.parse(readFileSync(join(directory, `security-reports/govulncheck-${mode}.json`), 'utf8'));
    assert.equal(r.status, 'findings');
  }
});


const fuzzOutput = scan => {
  const [pkg, target] = fuzzTargets[scan];
  const Package = `omni_money/${pkg.slice(2)}`;
  return [
    { Action: 'start', Package },
    { Action: 'run', Package, Test: target },
    { Action: 'output', Package, Test: target, Output: 'fuzz: elapsed: 3s, execs: 42 (14/sec), new interesting: 1 (total: 2)\n' },
    { Action: 'pass', Package, Test: target },
    { Action: 'pass', Package },
  ].map(value => JSON.stringify(value)).join('\n');
};

test('fuzz reports fail closed without presenting parser failures as known vulnerabilities', () => {
  for (const scan of Object.keys(fuzzTargets)) {
    assert.deepEqual(fuzzReport(scan, 0, fuzzOutput(scan)), { schema: 1, scan, status: 'clean', findings: [] });
    for (const code of [1, 2, null, undefined]) {
      const report = fuzzReport(scan, code);
      assert.equal(report.status, 'error');
      assert.deepEqual(report.findings, []);
      const view = render(entries([report])[0], runURL);
      assert.match(view.title, /検査エラー/);
      assert.deepEqual(view.body.split('\n').filter(line => line.startsWith('[検査ログ](')), [`[検査ログ](${runURL})`]);
      assert.doesNotMatch(view.body, /CVE-/);
    }
  }
  assert.throws(() => fuzzReport('../unknown', 0), /unknown fuzz target/);
});

test('missing fuzz reports create individual errors and failed downloads reject their clean results', t => {
  const directory = mkdtempSync(join(tmpdir(), 'omni-fuzz-reports-'));
  t.after(() => rmSync(directory, { recursive: true, force: true }));
  for (const scan of scans.filter(s => !Object.hasOwn(fuzzTargets, s))) {
    writeFileSync(join(directory, `${scan}.json`), JSON.stringify({ schema: 1, scan, status: 'clean', findings: [] }));
  }
  assert.deepEqual(collect(directory).filter(r => r.status === 'error').map(r => r.scan).sort(), Object.keys(fuzzTargets).sort());
  for (const scan of Object.keys(fuzzTargets)) writeFileSync(join(directory, `${scan}.json`), JSON.stringify(fuzzReport(scan, 0, fuzzOutput(scan))));
  assert.ok(collect(directory).every(r => r.status === 'clean'));
  assert.ok(collect(directory, false).every(r => r.status === 'error'));
});

test('required Go check rejects fuzz failure, cancellation and unexpected skips', t => {
  const workflow = readFileSync(resolve('.github/workflows/ci.yml'), 'utf8');
  const gate = workflow.split('      - name: Require expected Go result and successful auxiliary checks\n')[1]
    ?.split('  security-issues:')[0];
  const block = gate?.match(/        run: \|\n((?:          .*\n|\n)+)/)?.[1];
  assert.ok(block, 'required-check script missing');
  const program = block.replace(/^          /gm, '');
  for (const expected of ['success', 'skipped']) {
    for (const result of ['success', 'skipped', 'failure', 'cancelled', '']) {
      const child = spawnSync('bash', ['-e', '-c', program], {
        encoding: 'utf8', env: { ...process.env, GO_RESULT: 'success', EXPECTED_GO_RESULT: 'success',
          SAFE_UPDATE_RESULT: 'success', WINDOWS_RESULT: 'success', SECURITY_AUTOMATION_RESULT: 'success',
          FUZZ_RESULT: result, EXPECTED_FUZZ_RESULT: expected },
      });
      assert.equal(child.status === 0, result === expected, `${expected}/${result}`);
    }
  }
  const fuzzJob = workflow.split('  parser-fuzz:\n')[1]?.split('  # Keep the existing required check name')[0];
  assert.match(fuzzJob, /if: \$\{\{ github.event_name == 'schedule' \|\| github.event_name == 'workflow_dispatch' \}\}/);
  assert.match(fuzzJob, /max-parallel: 2/);
  assert.match(fuzzJob, /fail-fast: false/);
  assert.match(fuzzJob, /target: \[fuzz-csv, fuzz-json, fuzz-image\]/);
  assert.match(workflow, /needs: \[go-checks, safe-update, windows-security, security-automation, parser-fuzz\]/);
  assert.match(workflow, /needs: \[go-checks, frontend, docker, security-automation, parser-fuzz\]/);
  assert.match(workflow, /EXPECTED_FUZZ_RESULT: .*'schedule'.*'workflow_dispatch'.*'success' \|\| 'skipped'/);
});


test('zero exit without a completed matching fuzz engine never becomes a clean report', t => {
  const scan = 'fuzz-json';
  const valid = fuzzOutput(scan);
  for (const output of ['', 'testing: warning: no fuzz tests to fuzz', '{}', valid.slice(0, -1),
    valid.replaceAll('FuzzStrictServerJSONKeys', 'FuzzStaleName'),
    valid.replaceAll('omni_money/backend/api', 'omni_money/backend/core'),
    valid.replace('execs: 42 ', 'execs: 0 '),
    valid.replace('"Action":"pass","Package":"omni_money/backend/api","Test"', '"Action":"skip","Package":"omni_money/backend/api","Test"')]) {
    assert.equal(fuzzReport(scan, 0, output).status, 'error');
  }
  const directory = mkdtempSync(join(tmpdir(), 'omni-fuzz-cli-'));
  t.after(() => rmSync(directory, { recursive: true, force: true }));
  mkdirSync(join(directory, 'bin'));
  for (const [output, expectedStatus] of [['testing: warning: no fuzz tests to fuzz', 'error'], [valid, 'clean']]) {
    writeFileSync(join(directory, 'bin/go'), `#!${process.execPath}\nrequire('node:fs').writeFileSync('fuzz-args.json', JSON.stringify(process.argv.slice(2)));\nprocess.stdout.write(${JSON.stringify(output)});\n`, { mode: 0o755 });
    const child = spawnSync(process.execPath, [resolve('scripts/security-reports.mjs'), scan], {
      cwd: directory, env: { ...process.env, PATH: `${join(directory, 'bin')}:${process.env.PATH}` }, encoding: 'utf8',
    });
    assert.equal(child.status, expectedStatus === 'clean' ? 0 : 1);
    assert.equal(JSON.parse(readFileSync(join(directory, `security-reports/${scan}.json`), 'utf8')).status, expectedStatus);
    assert.deepEqual(JSON.parse(readFileSync(join(directory, 'fuzz-args.json'), 'utf8')),
      ['test', '-json', '-run=^$', '-fuzz=^FuzzStrictServerJSONKeys$', '-fuzztime=120s', '-fuzzminimizetime=10s', '-parallel=1', '-timeout=4m', './backend/api']);
  }
});
