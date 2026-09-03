import assert from 'node:assert/strict'
import test from 'node:test'
import { formatBuildInfo, resolveBuildVersion } from '../src/utils/buildInfo.js'

test('build version preserves a normalized release value', () => {
  assert.equal(resolveBuildVersion('1.1.0'), '1.1.0')
  assert.equal(resolveBuildVersion(' v1.1.0 '), '1.1.0')
  assert.equal(formatBuildInfo('1.1.0'), 'Omni Money v1.1.0')
})

test('build version falls back to dev when unset or blank', () => {
  for (const value of [undefined, null, '', '   ', '\t\n']) {
    assert.equal(resolveBuildVersion(value), 'dev')
    assert.equal(formatBuildInfo(value), 'Omni Money dev')
  }
})

test('build version safely handles malformed environment values', () => {
  assert.equal(resolveBuildVersion({ version: '1.1.0' }), 'dev')
  assert.equal(resolveBuildVersion(`1.1.0\u0000`), 'dev')
  assert.equal(resolveBuildVersion('x'.repeat(65)), 'dev')
  // Printable labels used by local/CI builds remain displayable. Vue's text
  // interpolation escapes the value when rendering it in the browser.
  assert.equal(resolveBuildVersion('ci'), 'ci')
})
