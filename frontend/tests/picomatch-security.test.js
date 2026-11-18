import test from 'node:test'
import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import pm from 'picomatch'
import utils from 'picomatch/lib/utils.js'
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

test('backslash removal preserves upstream semantics for every short bracket/newline combination', () => {
  const original = /(?:\[.*?[^\\]\]|\\(?=.))/g
  const alphabet = ['[', ']', '\\', 'a', '\n', '\r', '\u2028', '\u2029']
  let values = ['']
  for (let length = 0; length <= 5; length++) {
    for (const value of values) assert.equal(utils.removeBackslashes(value), value.replace(original, match => match === '\\' ? '' : match), JSON.stringify(value))
    if (length < 5) values = values.flatMap(value => alphabet.map(char => value + char))
  }
})

test('unterminated brackets with escaped characters are processed within a fixed deadline', () => {
  const code = `import assert from 'node:assert/strict'; import utils from 'picomatch/lib/utils.js';
    const size = 300000;
    assert.equal(utils.removeBackslashes('['.repeat(size) + '\\\\a'), '['.repeat(size) + 'a');
    assert.equal(utils.removeBackslashes('\\\\['.repeat(size) + '\\\\'), '['.repeat(size) + '\\\\');
    const bracketed = '[' + '\\\\['.repeat(size) + ']';
    assert.equal(utils.removeBackslashes(bracketed), bracketed);
    console.log('linear bracket scan passed');`
  const result = spawnSync(process.execPath, ['--input-type=module', '-e', code], {
    cwd: fileURLToPath(new URL('../', import.meta.url)), encoding: 'utf8', timeout: 3000,
  })
  assert.equal(result.status, 0, result.error?.message || result.stderr)
  assert.match(result.stdout, /linear bracket scan passed/)
})
