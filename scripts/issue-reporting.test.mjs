import assert from 'node:assert/strict';
import test from 'node:test';
import { hash } from './security-reports.mjs';
import { publish, render } from './security-issues.mjs';

const runURL = 'https://github.com/ShiningWank0/Omni_Money/actions/runs/123/attempts/1';
const options = { deferOverflow: true, respectClosed: true, scope: 'ci-monitor' };
const bot = { login: 'github-actions[bot]' };
const entry = number => {
  const data = { kind: 'vulnerability', tool: 'npm', component: `unique-finding-${number}`, id: [`CVE-2099-${number}`],
    severity: ['HIGH'], installed: ['1.0.0'], fixed: ['1.0.1'], summary: [`complete evidence ${number}`], context: ['frontend'] };
  return { key: hash(['fixture', number]), digest: hash(data), data };
};

function mockAPI(initial = []) {
  const state = { issues: structuredClone(initial), comments: new Map(), writes: [] };
  const api = async (method, path, body) => {
    if (method === 'GET') {
      const page = Number(new URL(`https://example.test/${path}`).searchParams.get('page') || 1);
      const number = Number(path.match(/^issues\/(\d+)\/comments/)?.[1]);
      const values = path.startsWith('issues?') ? state.issues.filter(issue => path.includes('state=all') || issue.state !== 'closed')
        : state.comments.get(number) || [];
      return structuredClone(values.slice((page - 1) * 100, page * 100));
    }
    assert.equal(method, 'POST', 'human content and issue state must not be overwritten');
    state.writes.push({ method, path, body });
    if (path === 'issues') {
      const issue = { number: state.issues.length + 1, user: bot, state: 'open', ...body };
      state.issues.push(issue);
      return structuredClone(issue);
    }
    const number = Number(path.match(/^issues\/(\d+)\/comments$/)?.[1]);
    assert.ok(number, `unexpected mutation ${path}`);
    state.comments.set(number, [...state.comments.get(number) || [], { user: bot, ...body }]);
    return {};
  };
  return { api, state };
}

const backlog = state => state.issues.filter(issue => issue.body.includes(':backlog:'));
const regular = state => state.issues.filter(issue => !issue.body.includes(':backlog:'));

test('overflow is fully delivered to a Bot backlog within 25 writes and drains on the next run', async () => {
  const findings = Array.from({ length: 26 }, (_, number) => entry(number));
  const { api, state } = mockAPI();
  const first = await publish(api, findings, runURL, render, options);
  assert.deepEqual(first, { created: 25, updated: 0, unchanged: 0, deferred: 2, backlogIssues: 1 });
  assert.equal(state.writes.length, 25);
  assert.equal(regular(state).length, 24);
  assert.equal(backlog(state).length, 1);
  for (const item of findings.slice(24)) {
    assert.ok(backlog(state)[0].body.includes(item.data.component));
    assert.ok(backlog(state)[0].body.includes(item.data.summary[0]));
    assert.ok(!backlog(state)[0].body.includes(render(item, runURL).marker), 'a queue must never look like an individual finding');
  }
  assert.ok(state.issues.every(issue => issue.title.startsWith('[Bot]')));
  assert.ok(state.issues.every(issue => issue.body.includes('GitHub Actions（github-actions[bot]）が')));
  const second = await publish(api, findings, runURL.replace('/123/', '/124/'), render, options);
  assert.deepEqual(second, { created: 2, updated: 1, unchanged: 24, deferred: 0, backlogIssues: 0 });
  assert.equal(regular(state).length, 26);
  const queue = backlog(state)[0];
  assert.match(state.comments.get(queue.number).at(-1).body, /報告待ちは解消/);
  const writes = state.writes.length;
  await publish(api, findings, runURL.replace('/123/', '/125/'), render, options);
  assert.equal(state.writes.length, writes, 'unchanged runs must not repeat notifications');
});

test('previously deferred findings get individual issues before new arrivals', async () => {
  const previous = Array.from({ length: 26 }, (_, number) => entry(number));
  const { api, state } = mockAPI();
  await publish(api, previous, runURL, render, options);
  const fresh = Array.from({ length: 30 }, (_, number) => entry(number + 100));
  const writes = state.writes.length;
  await publish(api, [...fresh, ...previous], runURL, render, options);
  const promoted = state.writes.slice(writes).filter(write => write.path === 'issues' && !write.body.body.includes(':backlog:'));
  assert.ok(promoted[0].body.body.includes(render(previous[24], runURL).marker));
  assert.ok(promoted[1].body.body.includes(render(previous[25], runURL).marker));
  assert.ok(state.writes.length - writes <= 25);
});

test('large UTF-8 backlogs split into bounded pages without dropping details', async () => {
  const findings = Array.from({ length: 40 }, (_, number) => entry(number));
  const renderLarge = (item, url) => ({ ...render(item, url), body: `${render(item, url).body}\n${'検出証跡'.repeat(1000)}` });
  const { api, state } = mockAPI();
  const result = await publish(api, findings, runURL, renderLarge, options);
  assert.ok(result.backlogIssues > 1);
  assert.ok(state.writes.length <= 25);
  for (const write of state.writes) assert.ok(Buffer.byteLength(write.body.body) <= 60_000);
  for (const item of findings) {
    assert.ok(state.issues.some(issue => issue.body.includes(item.data.component)), `missing ${item.data.component}`);
    assert.ok(state.issues.some(issue => issue.body.includes(item.data.summary[0])), `missing evidence ${item.data.component}`);
  }
});

test('closed unchanged findings stay quiet and human copies of markers cannot claim reports', async () => {
  const item = entry(1), view = render(item, runURL);
  const { api, state } = mockAPI([
    { number: 1, state: 'closed', user: bot, body: view.body },
    { number: 2, state: 'open', user: { login: 'someone' }, body: render(entry(2), runURL).body },
  ]);
  const result = await publish(api, [item, entry(2)], runURL, render, options);
  assert.equal(result.unchanged, 1);
  assert.equal(result.created, 1);
  assert.equal(state.issues[0].state, 'closed');
});

test('publication errors propagate and an impossible backlog fails before any write', async () => {
  const { api } = mockAPI();
  await assert.rejects(publish(async (method, path, body) => {
    if (method !== 'GET') throw new Error('GitHub API POST failed (403)');
    return api(method, path, body);
  }, [entry(1)], runURL, render, options), /POST failed/);
  const { api: other, state } = mockAPI();
  const huge = (item, url) => ({ ...render(item, url), body: `${render(item, url).body}\n${'a'.repeat(40_000)}` });
  await assert.rejects(publish(other, Array.from({ length: 51 }, (_, number) => entry(number)), runURL, huge, options), /publication budget/);
  assert.equal(state.writes.length, 0);
});

test('a matching Dependabot PR receives one Bot comment and no duplicate Issue', async () => {
  const item = entry(1), { api, state } = mockAPI();
  const pull = { number: 42, state: 'open', user: { login: 'dependabot[bot]' }, body: 'Dependabot update proposal' };
  const route = { ...options, pullRequests: new Map([[item.key, pull]]) };
  const first = await publish(api, [item], runURL, render, route);
  assert.equal(first.created, 0);
  assert.equal(first.updated, 1);
  assert.equal(state.writes[0].path, 'issues/42/comments');
  assert.match(state.writes[0].body.body, /既存のDependabot更新候補/);
  const second = await publish(api, [item], runURL, render, route);
  assert.equal(second.unchanged, 1);
  assert.equal(state.writes.length, 1);
  await assert.rejects(publish(api, [item], runURL, render, { ...options,
    pullRequests: new Map([[item.key, { ...pull, user: { login: 'someone' } }]]) }), /Invalid Dependabot/);
});
