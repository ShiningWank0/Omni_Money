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
      // Each ledger contract is solved independently. Confirmed violations of
      // other contracts remain valid if one contract could not be checked.
      if (report.scan !== 'formal-ledger') continue;
    }
    for (const f of report.findings) {
      // Vulnerabilities group per scanner and package so every advisory for
      // the same dependency shares one issue. Static analysis stays per rule
      // and location because the rule identifies the defect.
      const key = ['gosec', 'formal'].includes(f.tool) ? hash([f.tool, f.id, f.component]) : hash([f.tool, f.component]);
      grouped.set(key, [...(grouped.get(key) || []), f]);
    }
  }
  return [...grouped].sort(([a], [b]) => a.localeCompare(b)).map(([key, findings]) => {
    let data;
    if (findings[0].kind === 'scan-error') data = findings[0];
    else {
      const first = findings[0];
      data = { kind: ['gosec', 'formal'].includes(first.tool) ? 'static-analysis' : 'vulnerability',
        tool: first.tool, component: first.component, id: unique(findings.map(f => f.id)) };
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
  const identifiers = Array.isArray(d.id) ? d.id.join(', ') : d.id;
  const title = `[Bot][Security: ${category}] ${d.tool}${isError ? '' : ` ${identifiers} / ${d.component}`}`.slice(0, 240);
  const details = isError
    ? '検査結果が欠落、無効、または検査自体が失敗しています。脆弱性の検出とは別の通知です。先行ステップ・依存先・artifact取得を含めて実行ログを確認してください。'
    : [
      `- 対象: ${clean(d.component)}`,
      `- 識別子: ${clean(identifiers)}`,
      `- 深刻度: ${clean(d.severity.join(', '))}（UNSPECIFIEDは検査元が深刻度を提供していない場合）`,
      `- 検出版／範囲: ${clean(d.installed.join(', ') || '該当なし／未提供')}`,
      `- 修正版／対応候補: ${clean(d.fixed.join(', ') || '検査元に修正版の記載なし。個別の対応確認が必要')}`,
      `- 検査条件: ${clean(d.context.join(', '))}`,
      `- 概要: ${clean(d.summary.join(' / '))}`,
    ].join('\n');
  const body = `${marker}\n${revision}\n\nGitHub Actions（github-actions[bot]）が定期・手動CIから自動作成した報告です。\n\n${details}\n\n[検査ログ](${runURL})\n\n以降の変化はコメントに記録します。対応内容を確認してから手動または修正PRで閉じてください。自動クローズ・再オープンは行いません。`;
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

const mutationLimit = 25;
// Reserve room below GitHub's issue/comment body limit, including UTF-8 text.
const bodyLimit = 60_000;
const botView = view => ({ ...view,
  title: (view.title.startsWith('[Bot]') ? view.title : `[Bot] ${view.title}`).slice(0, 240),
  body: view.body.includes('GitHub Actions（github-actions[bot]）が') ? view.body
    : `${view.body}\n\nGitHub Actions（github-actions[bot]）が自動作成した報告です。`,
});

async function latestReport(api, issue, marker) {
  const comments = await list(api, `issues/${issue.number}/comments`);
  return comments.filter(c => c.user?.login === 'github-actions[bot]' && c.body?.includes(marker) &&
    c.body.includes(`<!-- ${prefix}:revision:`)).at(-1)?.body || issue.body;
}

function reportDetails(view) {
  // The renderer owns these exact leading lines. Remove only that prefix;
  // never rewrite arbitrary HTML comments in the already escaped report body.
  const header = `${view.marker}\n${view.revision}\n`;
  if (!view.body.startsWith(header)) throw new Error('Invalid report marker header');
  return view.body.slice(header.length).trim();
}

function backlogView(scope, page, pending, runURL) {
  const marker = `<!-- ${prefix}:backlog:${scope}:${page} -->`;
  const state = pending.map(item => [item.entry.key, item.entry.digest]);
  const revision = `<!-- ${prefix}:revision:${hash(state)} -->`;
  const savedKeys = Buffer.from(JSON.stringify(state)).toString('base64');
  const details = pending.map((item, index) => `### ${index + 1}. ${clean(item.view.title)}\n\n${reportDetails(item.view)}`).join('\n\n---\n\n');
  return { marker, revision,
    title: `[Bot][Security: 報告待ち] ${scope} / ${page}`,
    body: `${marker}\n${revision}\n<!-- ${prefix}:pending:${savedKeys} -->\n\nGitHub Actions（github-actions[bot]）が自動作成した集約報告です。\n\n${pending.length ? `個別Issue化を待つ${pending.length}件を、本文に全件保存しています。次回の監視ではこの報告待ちを優先します。` : 'このページの個別報告待ちは解消しました。過去の検出内容はこのIssueの履歴に残っています。'}\n\n[検査ログ](${runURL})\n\n${details}`,
  };
}

function backlogPages(scope, pending, runURL) {
  const pages = [];
  for (const item of pending) {
    let page = pages.at(-1);
    if (!page) { page = []; pages.push(page); }
    if (Buffer.byteLength(backlogView(scope, pages.length, [...page, item], runURL).body) > bodyLimit) {
      if (!page.length) throw new Error('A finding is too large to report completely');
      page = []; pages.push(page);
    }
    page.push(item);
    if (Buffer.byteLength(backlogView(scope, pages.length, page, runURL).body) > bodyLimit) {
      throw new Error('A finding is too large to report completely');
    }
  }
  return pages.map((items, index) => backlogView(scope, index + 1, items, runURL));
}

async function publishQueued(api, findings, runURL, renderEntry, options, issues) {
  const scope = options.scope || 'security';
  if (!/^[A-Za-z0-9_.-]{1,80}$/.test(scope)) throw new Error('invalid reporting scope');
  const result = { created: 0, updated: 0, unchanged: 0, deferred: 0, backlogIssues: 0 };
  const backlogPrefix = `<!-- ${prefix}:backlog:`;
  const regular = issues.filter(issue => !issue.body?.includes(backlogPrefix));
  const backlog = new Map(), priority = new Map();
  for (const issue of issues.filter(issue => issue.state !== 'closed')) {
    const page = issue.body?.match(new RegExp(`<!-- ${prefix}:backlog:${scope.replaceAll('.', '\\.')}:(\\d+) -->`))?.[1];
    if (!page) continue;
    const marker = `<!-- ${prefix}:backlog:${scope}:${page} -->`;
    const latest = await latestReport(api, issue, marker);
    backlog.set(Number(page), { issue, latest });
    try {
      const encoded = latest.match(new RegExp(`<!-- ${prefix}:pending:([A-Za-z0-9+/=]+) -->`))?.[1];
      const pending = JSON.parse(Buffer.from(encoded || '', 'base64').toString('utf8'));
      if (!Array.isArray(pending) || !pending.every(item => Array.isArray(item) && item.length === 2 &&
        item.every(value => typeof value === 'string' && /^[a-f0-9]{64}$/.test(value)))) throw new Error();
      for (const [key] of pending) if (!priority.has(key)) priority.set(key, priority.size);
    } catch { throw new Error('Saved reporting backlog is invalid; refusing to lose pending findings'); }
  }
  const tasks = [];
  for (const entry of findings) {
    const target = options.pullRequests?.get(entry.key);
    if (target && (!Number.isSafeInteger(target.number) || target.number <= 0 || target.state !== 'open' ||
      target.user?.login !== 'dependabot[bot]')) throw new Error('Invalid Dependabot reporting target');
    const view = botView(renderEntry(entry, runURL));
    if (target) view.body += `\n\n既存のDependabot更新候補 [PR #${target.number}](${runURL.split('/actions/')[0]}/pull/${target.number}) へ検査結果を集約します。修正の適用可否は当該PRの検証で確認してください。`;
    if (Buffer.byteLength(view.body) > bodyLimit) throw new Error('A finding is too large to report completely');
    const existing = target || regular.find(issue => issue.state !== 'closed' && issue.body?.includes(view.marker)) ||
      regular.find(issue => issue.body?.includes(view.marker));
    if (existing && (await latestReport(api, existing, view.marker))?.includes(view.revision)) result.unchanged++;
    else tasks.push({ entry, view, existing });
  }
  tasks.sort((a, b) => (priority.get(a.entry.key) ?? Infinity) - (priority.get(b.entry.key) ?? Infinity));
  let plan;
  for (let count = Math.min(tasks.length, mutationLimit); count >= 0; count--) {
    const pending = tasks.slice(count), pages = backlogPages(scope, pending, runURL);
    const activePageCount = pages.length;
    for (const page of backlog.keys()) if (page > activePageCount) pages.push(backlogView(scope, page, [], runURL));
    const updates = pages.map(view => {
      const page = Number(view.marker.match(/:(\d+) -->$/)[1]);
      return { view, existing: backlog.get(page)?.issue, latest: backlog.get(page)?.latest };
    }).filter(item => !item.latest?.includes(item.view.revision));
    if (count + updates.length <= mutationLimit) { plan = { count, pending, updates }; break; }
  }
  if (!plan) throw new Error('Reporting backlog exceeds the publication budget; findings were not fully delivered');
  // Persist deferred details before individual writes. An interrupted publication
  // can be retried without depending on expiring artifacts for the deferred set.
  for (const { view, existing } of plan.updates) {
    if (existing) {
      await api('POST', `issues/${existing.number}/comments`, { body: view.body });
      result.updated++;
    } else {
      await api('POST', 'issues', { title: view.title, body: view.body });
      result.created++;
    }
  }
  for (const { view, existing } of tasks.slice(0, plan.count)) {
    if (!existing || existing.state === 'closed') {
      await api('POST', 'issues', { title: view.title, body: existing
        ? `${view.body}\n\n終了済みIssue #${existing.number} から検査結果が変化しています。` : view.body });
      result.created++;
    } else {
      await api('POST', `issues/${existing.number}/comments`, { body: `検査結果に変更があります。\n\n${view.body}` });
      result.updated++;
    }
  }
  result.deferred = plan.pending.length;
  result.backlogIssues = backlogPages(scope, plan.pending, runURL).length;
  return result;
}

export async function publish(api, findings, runURL, renderEntry = render, options = {}) {
  if (!/^https:\/\/github\.com\/[\w.-]+\/[\w.-]+\/actions\/runs\/\d+\/attempts\/\d+$/.test(runURL)) throw new Error('invalid run URL');
  const issues = (await list(api, `issues?state=${options.respectClosed ? 'all' : 'open'}`)).filter(issue => !issue.pull_request && issue.user?.login === 'github-actions[bot]');
  if (options.deferOverflow) return publishQueued(api, findings, runURL, renderEntry, options, issues);
  const result = { created: 0, updated: 0, unchanged: 0 };
  let mutations = 0;
  for (const entry of findings) {
    const view = botView(renderEntry(entry, runURL));
    const existing = issues.find(issue => issue.state !== 'closed' && issue.body?.includes(view.marker)) ||
      issues.find(issue => issue.body?.includes(view.marker));
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
    if (existing.state === 'closed') {
      await api('POST', 'issues', { title: view.title, body: `${view.body}\n\n終了済みIssue #${existing.number} から検査結果が変化しています。` });
      result.created++;
      continue;
    }
    await api('POST', `issues/${existing.number}/comments`, { body: `検査結果に変更があります。\n\n${view.body}` });
    result.updated++;
  }
  return result;
}

export async function main(env = process.env) {
  if (!['schedule', 'workflow_dispatch'].includes(env.GITHUB_EVENT_NAME) || env.GITHUB_REF !== 'refs/heads/main' || env.GITHUB_REPOSITORY !== 'ShiningWank0/Omni_Money') {
    throw new Error('Issue creation is restricted to scheduled or manually dispatched main runs of ShiningWank0/Omni_Money');
  }
  const reports = collect('security-reports', env.SECURITY_REPORT_DOWNLOAD_OUTCOME === 'success');
  const runURL = `https://github.com/${env.GITHUB_REPOSITORY}/actions/runs/${env.GITHUB_RUN_ID}/attempts/${env.GITHUB_RUN_ATTEMPT}`;
  const result = await publish(githubClient(env.GITHUB_TOKEN, env.GITHUB_REPOSITORY), entries(reports), runURL, render,
    { deferOverflow: true, scope: 'security' });
  const summary = `Security issues: ${JSON.stringify(result)}\n`;
  console.log(summary);
  if (env.GITHUB_STEP_SUMMARY) appendFileSync(env.GITHUB_STEP_SUMMARY, summary);
  // A scan error is a result to report. Only undelivered reports/API errors
  // fail this monitoring command; the separate PR/release gates stay strict.
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  main().catch(error => { console.error(error.message); process.exitCode = 1; });
}
