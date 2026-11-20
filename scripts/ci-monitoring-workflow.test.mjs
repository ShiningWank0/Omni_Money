import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'

const ci = readFileSync(new URL('../.github/workflows/ci.yml', import.meta.url), 'utf8')
const pinned = readFileSync(new URL('../.github/workflows/pinned-input-security.yml', import.meta.url), 'utf8')
const diagnostics = ['oss', 'go-checks', 'frontend', 'safe-update', 'windows-security', 'docker', 'security-automation', 'parser-fuzz', 'passkey']
const job = (source, id) => {
  const section = source.split(`\n  ${id}:\n`)[1]?.split(/\n  [a-z][a-z-]*:\n/)[0]
  assert.ok(section, `missing job ${id}`)
  return section
}
const step = (section, name) => {
  const block = section.split(`      - name: ${name}\n`)[1]?.split('\n      - ')[0]
  assert.ok(block, `missing step ${name}`)
  return block
}
const expression = (source, key, indentation = 4) => {
  const value = source.match(new RegExp(`^${' '.repeat(indentation)}${key}: (?:>-\\n\\s+)?(\\$\\{\\{[\\s\\S]*?\\}\\})`, 'm'))?.[1]
  assert.ok(value, `missing ${key} expression`)
  return value.slice(3, -2).trim()
}
const context = (event = 'schedule', overrides = {}) => ({
  github: { event_name: event, ref: 'refs/heads/main', repository: 'ShiningWank0/Omni_Money' },
  inputs: {}, env: { CI_MONITORING: 'true' },
  needs: Object.fromEntries(diagnostics.map(id => [id, { result: 'failure', outputs: {} }])),
  always: () => true, cancelled: () => false, success: () => true,
  ...overrides,
})
const evaluate = (source, key, scope, indentation) => Boolean(runInNewContext(expression(source, key, indentation), scope))

test('diagnostic continuation applies only to main monitoring, not PR, push, branch dispatch or release rehearsal', () => {
  const cases = [
    [context(), true],
    [context('workflow_dispatch'), true],
    [context('pull_request'), false],
    [context('push'), false],
    [context('schedule', { github: { event_name: 'schedule', ref: 'refs/heads/main', repository: 'someone/fork' } }), false],
    [context('workflow_dispatch', { github: { event_name: 'workflow_dispatch', ref: 'refs/heads/review', repository: 'ShiningWank0/Omni_Money' } }), false],
    [context('workflow_dispatch', { inputs: { release_rehearsal: true } }), false],
  ]
  for (const [scope, expected] of cases) {
    assert.equal(evaluate(ci, 'CI_MONITORING', scope, 2), expected)
    for (const id of diagnostics) assert.equal(evaluate(job(ci, id), 'continue-on-error', scope), expected, `${id}: ${scope.github.event_name}`)
    assert.equal(evaluate(job(ci, 'go'), 'if', scope), !expected, 'the merge/release verdict remains strict outside monitoring')
  }
})

test('an OSS failure cannot suppress independent scheduled diagnostics', () => {
  for (const id of diagnostics.filter(id => id !== 'oss')) {
    const section = job(ci, id)
    assert.equal(evaluate(section, 'if', context()), true, `${id} must still run after OSS failure`)
    const normal = context('pull_request', { env: { CI_MONITORING: 'false' } })
    assert.equal(evaluate(section, 'if', normal), false, `${id} must respect a failed prerequisite in PR checks`)
  }
  assert.equal(evaluate(job(ci, 'release-plan'), 'if', context()), false, 'monitoring does not prepare releases')
})

test('a finding or failed test does not suppress the remaining scheduled frontend and Go checks', () => {
  const monitoredSteps = [
    ['frontend', 'Audit dependencies'],
    ['frontend', 'Test frontend security boundaries'],
    ['frontend', 'Test frontend components'],
    ['frontend', 'Build frontend'],
    ['go-checks', 'Run Go tests'],
    ['go-checks', 'Run Go race tests'],
    ['go-checks', 'Run server Go race tests'],
    ['go-checks', 'Run Go security analyzer'],
    ['go-checks', 'Scan reachable Go vulnerabilities'],
    ['docker', 'Run server/browser E2E'],
    ['parser-fuzz', 'Fuzz bounded security parsers'],
  ]
  for (const [id, name] of monitoredSteps) {
    const block = step(job(ci, id), name)
    assert.equal(evaluate(block, 'continue-on-error', context(), 8), true, name)
    assert.equal(evaluate(block, 'continue-on-error', context('pull_request', { env: { CI_MONITORING: 'false' } }), 8), false, name)
  }
  assert.doesNotMatch(step(job(ci, 'frontend'), 'Install dependencies'), /continue-on-error:/, 'unusable prerequisites are not accepted as valid')
  assert.doesNotMatch(step(job(ci, 'go-checks'), 'Verify frozen Linux package inventory'), /continue-on-error:/, 'a changed build environment still stops execution inside that job')
})

test('the Bot reporter runs after every diagnostic even when monitoring self-tests fail', () => {
  const reporter = job(ci, 'security-issues')
  const needs = reporter.match(/^    needs: \[([^\]]+)\]/m)?.[1].split(',').map(s => s.trim())
  assert.deepEqual(needs?.sort(), diagnostics.slice().sort())
  assert.equal(evaluate(reporter, 'if', context()), true)
  assert.equal(evaluate(reporter, 'if', context('pull_request')), false)
  assert.equal(evaluate(reporter, 'if', context('workflow_dispatch', { inputs: { release_rehearsal: true } })), false)
  assert.equal(evaluate(reporter, 'if', context('schedule', { github: { event_name: 'schedule', ref: 'refs/heads/main', repository: 'someone/fork' } })), false)
  assert.doesNotMatch(reporter, /container:|verify-linux-environment|needs\.security-automation\.result|^    continue-on-error:/m)
  assert.match(reporter, /actions: read/)
  assert.match(reporter, /issues: write/)
  assert.doesNotMatch(reporter, /contents: write|packages: write|pull-requests: write/)
  assert.match(reporter, /CI_MONITOR_NEEDS_JSON: \$\{\{ toJSON\(needs\) \}\}/)
  assert.match(reporter, /CI_MONITOR_REPORT_JOB: security-issues/)
  assert.match(reporter, /run: node scripts\/ci-monitor\.mjs/)
  assert.match(step(reporter, 'Save the complete monitoring outcome'), /if: \$\{\{ always\(\) \}\}/)
  assert.match(reporter, /retention-days: 90/)
})

test('failed fuzz and license-layout evidence is preserved after diagnostic continuation', () => {
  assert.match(step(job(ci, 'parser-fuzz'), 'Upload failing fuzz corpus'), /steps\.fuzz\.outcome == 'failure'/)
  assert.match(step(job(ci, 'oss'), 'Save failed license layout screenshots'), /steps\.oss-layout\.outcome == 'failure'/)
  assert.match(step(job(ci, 'oss'), 'Save OSS mismatch evidence without accepting the candidate'), /steps\.oss-check\.outcome == 'failure'/)
})

test('pinned scans remain gated by their real validation results and notification still runs on failed validation', () => {
  const tests = job(pinned, 'tests'), scan = job(pinned, 'scan'), notify = job(pinned, 'notify')
  const output = tests.match(/^      validated: \$\{\{ (.*?) \}\}/m)?.[1]
  assert.ok(output)
  for (const validation of ['success', 'failure']) for (const extraction of ['success', 'failure']) {
    const valid = Boolean(runInNewContext(output, { steps: { validation: { outcome: validation }, extraction: { outcome: extraction } } }))
    assert.equal(valid, validation === 'success' && extraction === 'success')
    const scope = context('schedule', { needs: { tests: { result: 'success', outputs: { validated: String(valid) } } } })
    assert.equal(evaluate(scan, 'if', scope), valid)
    assert.equal(evaluate(notify, 'if', scope), true)
  }
  assert.match(notify, /actions: read/)
  assert.match(notify, /CI_MONITOR_NEEDS_JSON: \$\{\{ toJSON\(needs\) \}\}/)
  assert.match(notify, /CI_MONITOR_REPORT_JOB: notify/)
  assert.doesNotMatch(notify, /^    continue-on-error:/m)
})

test('monitoring records raw outcomes for every continued step and preserves them as attempt-scoped evidence', () => {
  for (const [source, ids] of [[ci, diagnostics], [pinned, ['tests', 'scan']]]) for (const id of ids) {
    const section = job(source, id)
    const record = step(section, 'Record actual diagnostic step outcomes')
    const names = JSON.parse(record.match(/CI_MONITOR_STEP_NAMES: >-\n\s+(\{[^\n]+\})/)?.[1] || 'null')
    assert.ok(names, id)
    assert.match(record, /CI_MONITOR_STEP_RESULTS: \$\{\{ toJSON\(steps\) \}\}/)
    assert.match(record, /CI_MONITOR_JOB: \$\{\{ github\.job \}\}/)
    assert.match(record, /CI_MONITOR_MATRIX: \$\{\{ toJSON\(matrix\) \}\}/)
    for (const block of section.split(/\n      - /).slice(1)) {
      if (!/continue-on-error:/.test(block)) continue
      const identity = block.match(/^\s*id: ([a-zA-Z0-9_-]+)$/m)?.[1]
      assert.ok(identity && names[identity], `${id}: a continued step must preserve its original outcome`)
    }
    assert.match(step(section, 'Save actual diagnostic step outcomes'), /github\.run_attempt/)
    assert.match(record, /always\(\) && env\.CI_MONITORING == 'true'/)
  }
})
