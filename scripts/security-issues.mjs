import { readFileSync, appendFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
import { hash, scans } from './security-reports.mjs';

const prefix = 'omni-security-monitor:v1';
const clean = value => String(value ?? '').slice(0, 1000).replaceAll('@', '@\u200b')
  .replace(/[&<>"'`\[\]\\|*_\r\n]/g, c => `&#${c.charCodeAt(0)};`);
const unique = values => [...new Set(values)].sort();

export function collect(directory, downloadSucceeded = true) {
  return scans.map(scan => {
    try {
      // download-artifact may leave files behind when digest verification
      // fails. Never trust partial output from a failed download step.
      if (!downloadSucceeded) throw new Error();
      const r = JSON.parse(readFileSync(resolve(directory, `${scan}.json`), 'utf8'));
      if (r.schema !== 1 || r.scan !== scan || !['clean', 'findings', 'error'].includes(r.status) || !Array.isArray(r.findings) ||
        (r.status === 'clean' && r.findings.length) || (r.status === 'findings' && !r.findings.length)) throw new Error();
      for (const f of r.findings) {
        for (const key of ['tool', 'id', 'component', 'severity', 'installed', 'fixed', 'summary', 'context']) {
          if (typeof f[key] !== 'string' || f[key].length > 1000) throw new Error();
        }
        if (f.tool !== scan.split('-')[0] || !f.id || !f.component) throw new Error();
      }
      return r;
    } catch {
      return { schema: 1, scan, status: 'error', findings: [] };
    }
  });
}

export function entries(reports) {
  const grouped = new Map();
  for (const report of reports) {
    if (report.status === 'error') {
      const data = { kind: 'scan-error', tool: report.scan };
      grouped.set(hash(data), [data]);
      continue;
    }
    for (const f of report.findings) {
      const key = hash([f.tool, f.id, f.component]);
      grouped.set(key, [...(grouped.get(key) || []), f]);
    }
  }
  return [...grouped].sort(([a], [b]) => a.localeCompare(b)).map(([key, findings]) => {
    let data;
    if (findings[0].kind === 'scan-error') data = findings[0];
    else {
      const first = findings[0];
      data = { kind: first.tool === 'gosec' ? 'static-analysis' : 'vulnerability',
        tool: first.tool, id: first.id, component: first.component };
      for (const field of ['severity', 'installed', 'fixed', 'summary', 'context']) {
        data[field] = unique(findings.map(f => f[field]).filter(Boolean));
      }
    }
    return { key, digest: hash(data), data };
  });
}

export function render(entry, runURL) {
  const { key, digest, data: d } = entry;
  const marker = `<!-- ${prefix}:${key} -->`;
  const revision = `<!-- ${prefix}:revision:${digest} -->`;
  const isError = d.kind === 'scan-error';
  const category = isError ? '検査エラー' : d.kind === 'static-analysis' ? '静的解析' : '脆弱性';
  const title = `[Security: ${category}] ${d.tool}${isError ? '' : ` ${d.id} / ${d.component}`}`.slice(0, 240);
  const details = isError
    ? '検査結果が欠落、無効、または検査自体が失敗しています。脆弱性の検出とは別の通知です。先行ステップ・依存先・artifact取得を含めて実行ログを確認してください。'
    : [
      `- 対象: ${clean(d.component)}`,
      `- 識別子: ${clean(d.id)}`,
      `- 深刻度: ${clean(d.severity.join(', '))}（UNSPECIFIEDは検査元が深刻度を提供していない場合）`,
      `- 検出版／範囲: ${clean(d.installed.join(', ') || '該当なし／未提供')}`,
      `- 修正版／対応候補: ${clean(d.fixed.join(', ') || '検査元に修正版の記載なし。個別の対応確認が必要')}`,
      `- 検査条件: ${clean(d.context.join(', '))}`,
      `- 概要: ${clean(d.summary.join(' / '))}`,
    ].join('\n');
  const body = `${marker}\n${revision}\n\n${details}\n\n[検査ログ](${runURL})\n\n定期CIによる自動報告です。以降の変化はコメントに記録します。対応内容を確認してから手動または修正PRで閉じてください。自動クローズ・再オープンは行いません。`;
  return { title: title.replaceAll('@', '@\u200b').replace(/[\r\n]/g, ' '), body, marker, revision };
}

export function githubClient(token, repository, fetcher = fetch) {
  if (!token || !/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(repository)) throw new Error('invalid GitHub configuration');
  return async (method, path, body) => {
    const response = await fetcher(`https://api.github.com/repos/${repository}/${path}`, {
      method, redirect: 'error', signal: AbortSignal.timeout(30_000),
      headers: { Authorization: `Bearer ${token}`, Accept: 'application/vnd.github+json',
        'Content-Type': 'application/json', 'X-GitHub-Api-Version': '2026-03-10' },
      ...(body ? { body: JSON.stringify(body) } : {}),
    });
    // Never echo the token, response body, or a URL obtained from a report.
    if (!response.ok) throw new Error(`GitHub API ${method} failed (${response.status})`);
    return response.json();
  };
}

async function list(api, path) {
  const all = [];
  for (let page = 1; ; page++) {
    if (page > 100) throw new Error('GitHub pagination limit reached');
    const batch = await api('GET', `${path}${path.includes('?') ? '&' : '?'}per_page=100&page=${page}`);
    if (!Array.isArray(batch)) throw new Error('invalid GitHub list response');
    all.push(...batch);
    if (batch.length < 100) return all;
  }
}

export async function publish(api, findings, runURL) {
  if (!/^https:\/\/github\.com\/[\w.-]+\/[\w.-]+\/actions\/runs\/\d+\/attempts\/\d+$/.test(runURL)) throw new Error('invalid run URL');
  const issues = (await list(api, 'issues?state=open')).filter(issue => !issue.pull_request && issue.user?.login === 'github-actions[bot]');
  const result = { created: 0, updated: 0, unchanged: 0 };
  let mutations = 0;
  for (const entry of findings) {
    const view = render(entry, runURL);
    const existing = issues.find(issue => issue.body?.includes(view.marker));
    if (!existing) {
      if (++mutations > 25) throw new Error('Issue update limit reached; remaining findings will be retried on the next scheduled run');
      await api('POST', 'issues', { title: view.title, body: view.body });
      result.created++;
      continue;
    }
    // Record changes in comments without overwriting the original issue.
    // The latest bot comment is current state; checking every historical digest
    // would incorrectly suppress a regression back to an earlier state.
    const comments = await list(api, `issues/${existing.number}/comments`);
    const previous = comments.filter(c => c.user?.login === 'github-actions[bot]' && c.body?.includes(`<!-- ${prefix}:revision:`));
    const latest = previous.at(-1)?.body || existing.body;
    if (latest?.includes(view.revision)) { result.unchanged++; continue; }
    if (++mutations > 25) throw new Error('Issue update limit reached; remaining findings will be retried on the next scheduled run');
    await api('POST', `issues/${existing.number}/comments`, { body: `検査結果に変更があります。\n\n${view.body}` });
    result.updated++;
  }
  return result;
}

export async function main(env = process.env) {
  if (env.GITHUB_EVENT_NAME !== 'schedule' || env.GITHUB_REF !== 'refs/heads/main' || env.GITHUB_REPOSITORY !== 'ShiningWank0/Omni_Money') {
    throw new Error('Issue creation is restricted to scheduled main runs of ShiningWank0/Omni_Money');
  }
  const reports = collect('security-reports', env.SECURITY_REPORT_DOWNLOAD_OUTCOME === 'success');
  const runURL = `https://github.com/${env.GITHUB_REPOSITORY}/actions/runs/${env.GITHUB_RUN_ID}/attempts/${env.GITHUB_RUN_ATTEMPT}`;
  const result = await publish(githubClient(env.GITHUB_TOKEN, env.GITHUB_REPOSITORY), entries(reports), runURL);
  const summary = `Security issues: ${JSON.stringify(result)}\n`;
  console.log(summary);
  if (env.GITHUB_STEP_SUMMARY) appendFileSync(env.GITHUB_STEP_SUMMARY, summary);
  if (reports.some(report => report.status === 'error')) throw new Error('One or more security scans are incomplete; see the scan-error issues');
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  main().catch(error => { console.error(error.message); process.exitCode = 1; });
}
