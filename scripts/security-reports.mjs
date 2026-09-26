import { createHash } from 'node:crypto';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { relative, resolve } from 'node:path';
import { spawnSync } from 'node:child_process';
import { pathToFileURL } from 'node:url';

export const scans = ['npm', 'gosec-desktop', 'gosec-server', 'govulncheck-desktop', 'govulncheck-server', 'trivy'];
export const hash = value => createHash('sha256').update(JSON.stringify(value)).digest('hex');
const requireValue = (condition, message) => { if (!condition) throw new Error(message); };
const high = value => ['HIGH', 'CRITICAL'].includes(String(value).toUpperCase());
const text = value => String(value ?? '').slice(0, 1000);

function finding(tool, id, component, severity, installed, fixed, summary, context) {
  requireValue(id && component, 'finding identity missing');
  return { tool, id: text(id), component: text(component), severity: text(severity),
    installed: text(installed), fixed: text(fixed), summary: text(summary), context: text(context) };
}

// govulncheck emits a stream of pretty-printed JSON objects, not JSONL.
export function jsonStream(input) {
  const values = [];
  let start = 0, depth = 0, quoted = false, escaped = false;
  for (let i = 0; i < input.length; i++) {
    const c = input[i];
    if (depth === 0) {
      if (/\s/.test(c)) continue;
      requireValue(c === '{', 'invalid JSON stream');
      start = i;
    }
    if (quoted) {
      if (escaped) escaped = false;
      else if (c === '\\') escaped = true;
      else if (c === '"') quoted = false;
    } else if (c === '"') quoted = true;
    else if (c === '{' || c === '[') depth++;
    else if (c === '}' || c === ']') depth--;
    if (depth === 0) values.push(JSON.parse(input.slice(start, i + 1)));
  }
  requireValue(depth === 0 && !quoted && values.length > 0, 'incomplete JSON stream');
  return values;
}

export function parseReport(scan, input, root = process.cwd()) {
  if (scan.startsWith('govulncheck-')) {
    const messages = jsonStream(input);
    requireValue(messages[0].config?.protocol_version === 'v1.0.0' &&
      messages[0].config.scan_level === 'symbol' && messages.some(m => m.SBOM?.roots?.length), 'incomplete govulncheck report');
    const advisories = new Map(messages.filter(m => m.osv).map(m => [m.osv.id, m.osv]));
    for (const { finding: f } of messages.filter(m => m.finding)) {
      requireValue(f.osv && Array.isArray(f.trace) && f.trace[0]?.module, 'invalid Go finding');
    }
    return messages.filter(m => m.finding?.trace?.[0]?.function).map(({ finding: f }) => {
      const frame = f.trace[0];
      requireValue(advisories.has(f.osv), 'Go advisory missing');
      return finding('govulncheck', f.osv, frame.module, 'UNSPECIFIED', frame.version, f.fixed_version,
        advisories.get(f.osv).summary || '到達可能な脆弱性', scan);
    });
  }
  const data = JSON.parse(input);
  if (scan === 'npm') {
    requireValue(data.auditReportVersion === 2 && !data.error && data.vulnerabilities &&
      data.metadata?.vulnerabilities, 'invalid npm audit report');
    requireValue(Number.isInteger(data.metadata.vulnerabilities.high) && Number.isInteger(data.metadata.vulnerabilities.critical),
      'invalid npm vulnerability totals');
    const result = [];
    for (const [name, v] of Object.entries(data.vulnerabilities)) {
      if (!high(v.severity)) continue;
      requireValue(Array.isArray(v.via), 'invalid npm advisory list');
      for (const via of v.via) {
        // String entries point to another vulnerable package in this same report.
        if (typeof via === 'string') {
          requireValue(data.vulnerabilities[via], 'npm dependency advisory missing');
          continue;
        }
        if (!high(via.severity)) continue;
        const id = String(via.url || '').match(/GHSA-[\w-]+/)?.[0] || `npm-${via.source}`;
        requireValue(via.source || id.startsWith('GHSA-'), 'npm advisory identity missing');
        const fix = typeof v.fixAvailable === 'object'
          ? `${v.fixAvailable.name}@${v.fixAvailable.version}${v.fixAvailable.isSemVerMajor ? ' (major update)' : ''}`
          : v.fixAvailable === true ? '修正あり（npm auditを確認）' : '';
        result.push(finding('npm', id, name, via.severity.toUpperCase(), v.range, fix, via.title, 'frontend'));
      }
    }
    requireValue(result.length || !(data.metadata.vulnerabilities.high + data.metadata.vulnerabilities.critical),
      'npm reports vulnerabilities without usable advisories');
    return result;
  }
  if (scan.startsWith('gosec-')) {
    requireValue(data.Stats?.files > 0 && (data.Issues === null || Array.isArray(data.Issues)), 'incomplete gosec report');
    requireValue(!Object.values(data['Golang errors'] || {}).some(errors => errors?.length), 'gosec compilation errors');
    requireValue((data.Issues || []).every(v => ['LOW', 'MEDIUM', 'HIGH', 'CRITICAL'].includes(v.severity) &&
      v.rule_id && v.file && v.line), 'invalid gosec finding');
    return (data.Issues || []).filter(v => ['MEDIUM', 'HIGH', 'CRITICAL'].includes(v.severity)).map(v => {
      const file = relative(root, resolve(root, v.file)).replaceAll('\\', '/');
      return finding('gosec', v.rule_id, `${file}:${v.line}`, v.severity, '', '', v.details, scan);
    });
  }
  if (scan === 'trivy') {
    requireValue(data.SchemaVersion === 2 && data.ArtifactName && Array.isArray(data.Results) && data.Results.length,
      'incomplete Trivy report');
    return data.Results.flatMap(result => (result.Vulnerabilities || [])
      .filter(v => high(v.Severity) && v.FixedVersion)
      .map(v => finding('trivy', v.VulnerabilityID, `${result.Type}:${v.PkgName}`, v.Severity,
        v.InstalledVersion, v.FixedVersion, v.Title || v.VulnerabilityID, result.Target)));
  }
  throw new Error('unknown scanner');
}

export function normalize(scan, input, exitCode, root) {
  const report = { schema: 1, scan, status: 'error', findings: [] };
  try {
    requireValue(scans.includes(scan), 'unknown scanner');
    const findings = parseReport(scan, input, root);
    // JSON govulncheck and the Trivy action use exit 0 even for findings.
    const acceptable = scan.startsWith('govulncheck-') || scan === 'trivy'
      ? exitCode === 0 : exitCode === 0 || (exitCode === 1 && findings.length > 0);
    requireValue(acceptable, 'scanner execution failed');
    report.findings = findings;
    report.status = findings.length ? 'findings' : 'clean';
  } catch {
    // Raw stderr may contain registry URLs or environment details. Keep it in
    // the Actions log, never copy it into a public issue.
    report.error = '検査に失敗、または完全な検査結果を取得できませんでした。実行ログを確認してください。';
  }
  return report;
}

function save(report) {
  mkdirSync('security-reports', { recursive: true });
  writeFileSync(`security-reports/${report.scan}.json`, `${JSON.stringify(report, null, 2)}\n`);
  console.log(JSON.stringify(report));
  return report.status !== 'clean';
}

function run(tool) {
  if (tool === 'trivy') {
    let raw = '';
    try { raw = readFileSync('trivy-report.json', 'utf8'); } catch { /* recorded as an error */ }
    return save(normalize('trivy', raw, process.env.TRIVY_OUTCOME === 'success' ? 0 : 1));
  }
  requireValue(['npm', 'gosec', 'govulncheck'].includes(tool), 'unknown scanner');
  let failed = false;
  for (const scan of scans.filter(name => name === tool || name.startsWith(`${tool}-`))) {
    const server = scan.endsWith('-server') ? ['-tags', 'server'] : [];
    const command = tool === 'npm' ? ['npm', 'audit', '--json', '--audit-level=high']
      : tool === 'gosec' ? ['go', 'run', 'github.com/securego/gosec/v2/cmd/gosec@v2.28.0', '-terse', '-fmt=json', '-severity=medium', ...server, './...']
        : ['go', 'run', 'golang.org/x/vuln/cmd/govulncheck@v1.6.0', '-json', ...server, './...'];
    const result = spawnSync(command[0], command.slice(1), {
      cwd: tool === 'npm' ? resolve('frontend') : process.cwd(), encoding: 'utf8',
      timeout: 20 * 60 * 1000, maxBuffer: 32 * 1024 * 1024, shell: false,
    });
    if (result.stderr) process.stderr.write(result.stderr);
    failed = save(normalize(scan, result.stdout || '', result.error ? null : result.status)) || failed;
  }
  return failed;
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  process.exitCode = run(process.argv[2]) ? 1 : 0;
}
