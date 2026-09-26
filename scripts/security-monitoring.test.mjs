import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { spawnSync } from 'node:child_process';
import { jsonStream, normalize, scans } from './security-reports.mjs';
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
    const r = report(); r.findings[0].id = `GO-2099-${i}`; return r;
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
  ]) await assert.rejects(main(env), /restricted/);
  await assert.rejects(publish(() => assert.fail('unexpected request'), [], 'https://evil.test'), /invalid run URL/);
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
