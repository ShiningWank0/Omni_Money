import test from 'node:test'
import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import pm from 'picomatch'
import { verifyPicomatchPatch } from '../../scripts/verify-picomatch-patch.mjs'

test('all declared importers use the exact reviewed security backport', () => {
  assert.equal(verifyPicomatchPatch().trackingIssue, 348)
})

test('single-branch repeated wildcards cannot block the build process', () => {
  const code = `import assert from 'node:assert/strict'; import pm from 'picomatch';
    for (const options of [{}, { windows: true }, { capture: true }, { dot: true }, { maxExtglobRecursion: false }]) {
      for (const [pattern, value] of [['+(*a)', 'a'.repeat(512) + '!'], ['+(*)b', 'a'.repeat(512) + 'X'],
        ['*(*a)b', 'a'.repeat(512) + '!'], ['+([ab]*a)', 'a'.repeat(512) + '!']])
        assert.equal(pm.isMatch(value, pattern, options), false, pattern);
    }
    console.log('attack inputs rejected');`
  const result = spawnSync(process.execPath, ['--input-type=module', '-e', code], {
    cwd: fileURLToPath(new URL('../', import.meta.url)), encoding: 'utf8', timeout: 1500,
  })
  assert.equal(result.status, 0, result.error?.message || result.stderr)
  assert.match(result.stdout, /attack inputs rejected/)
})

test('ordinary globs, positive closure and optional empty closure keep matching correctly', () => {
  for (const options of [{}, { windows: true }, { capture: true }, { dot: true }]) {
    for (const [pattern, values] of [
      ['+(*a)', ['a', 'aaa', 'baba']], ['*(a*b)', ['ab', 'abab', 'axbbab']],
      ['x*(a*b)y', ['xy', 'xaby', 'xabababy']], ['+(abc)', ['abc', 'abcabc']],
      ['*(*(f)*(o))', ['fff', 'ooo', 'fffooo']], ['**/*.{js,vue}', ['src/a.js', 'src/a.vue']],
      ['+([ab]*a)', ['aa', 'abba', 'bababa']],
    ]) for (const value of values) assert.equal(pm.isMatch(value, pattern, options), true, `${pattern}: ${value}`)
    assert.equal(pm.isMatch('a/b', '+(*a)'), false)
    assert.equal(pm.isMatch('aaa!', '+(*a)'), false)
  }
  assert.equal(pm.scan('src/**/*.vue').isGlob, true)
})
