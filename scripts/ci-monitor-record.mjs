import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { mkdirSync, writeFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

// Do not persist the full steps context: action outputs can contain credentials.
// outcome is the pre-continue-on-error result; conclusion alone hides failures.
export function stepReport(env = process.env) {
  assert.match(env.CI_MONITOR_JOB || '', /^[a-zA-Z0-9_-]+$/);
  assert.match(env.GITHUB_RUN_ID || '', /^[1-9][0-9]*$/);
  assert.match(env.GITHUB_RUN_ATTEMPT || '', /^[1-9][0-9]*$/);
  const values = JSON.parse(env.CI_MONITOR_STEP_RESULTS || 'null');
  const names = JSON.parse(env.CI_MONITOR_STEP_NAMES || '{}');
  const matrix = JSON.parse(env.CI_MONITOR_MATRIX || '{}') || {};
  assert.ok(values && typeof values === 'object' && !Array.isArray(values));
  assert.ok(names && typeof names === 'object' && !Array.isArray(names));
  assert.ok(matrix && typeof matrix === 'object' && !Array.isArray(matrix));
  const steps = Object.entries(values).map(([id, value]) => {
    assert.match(id, /^[a-zA-Z0-9_-]+$/);
    assert.ok(['success', 'failure', 'cancelled', 'skipped'].includes(value.outcome));
    assert.ok(['success', 'failure', 'cancelled', 'skipped'].includes(value.conclusion));
    const name = names[id] || id;
    assert.ok(typeof name === 'string' && name.length <= 1000);
    return { id, name, outcome: value.outcome, conclusion: value.conclusion };
  });
  assert.ok(steps.length > 0, 'No step outcomes were recorded');
  return { schema: 1, runID: env.GITHUB_RUN_ID, attempt: env.GITHUB_RUN_ATTEMPT,
    job: env.CI_MONITOR_JOB, matrix, steps };
}

export function main(env = process.env, directory = resolve('security-reports')) {
  const report = stepReport(env);
  const matrixID = createHash('sha256').update(JSON.stringify(report.matrix)).digest('hex').slice(0, 12);
  mkdirSync(directory, { recursive: true });
  writeFileSync(resolve(directory, `ci-steps-${report.job}-${matrixID}.json`), `${JSON.stringify(report, null, 2)}\n`);
  return report;
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) main();
