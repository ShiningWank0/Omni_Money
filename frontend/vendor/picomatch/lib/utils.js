/*global navigator*/
'use strict';

const {
  REGEX_BACKSLASH,
  REGEX_SPECIAL_CHARS,
  REGEX_SPECIAL_CHARS_GLOBAL
} = require('./constants');

exports.isObject = val => val !== null && typeof val === 'object' && !Array.isArray(val);
exports.hasRegexChars = str => REGEX_SPECIAL_CHARS.test(str);
exports.isRegexChar = str => str.length === 1 && exports.hasRegexChars(str);
exports.escapeRegex = str => str.replace(REGEX_SPECIAL_CHARS_GLOBAL, '\\$1');
exports.toPosixSlashes = str => str.replace(REGEX_BACKSLASH, '/');

exports.isWindows = () => {
  if (typeof navigator !== 'undefined' && navigator.platform) {
    const platform = navigator.platform.toLowerCase();
    return platform === 'win32' || platform === 'windows';
  }

  if (typeof process !== 'undefined' && process.platform) {
    return process.platform === 'win32';
  }

  return false;
};

exports.removeBackslashes = str => {
  if (!str.includes('\\')) return str;

  // Preserve bracket spans exactly as the upstream regexp did, but compute
  // valid endings once. Repeating a suffix search at every '[' was quadratic.
  const length = str.length;
  const closing = new Int32Array(length + 1).fill(-1);
  const lineBreak = new Int32Array(length + 1).fill(-1);
  const isLineBreak = char => char === '\n' || char === '\r' || char === '\u2028' || char === '\u2029';
  let nextClosing = -1;
  let nextBreak = -1;
  for (let index = length - 1; index >= 0; index--) {
    if (str[index] === ']' && index > 0 && str[index - 1] !== '\\') nextClosing = index;
    if (isLineBreak(str[index])) nextBreak = index;
    closing[index] = nextClosing;
    lineBreak[index] = nextBreak;
  }

  const parts = [];
  let start = 0;
  for (let index = 0; index < length; index++) {
    if (str[index] === '[' && index + 2 < length) {
      const end = closing[index + 2];
      const newline = lineBreak[index + 1];
      // The final non-backslash before ']' may itself be a newline; the
      // lazy dot preceding it cannot cross an earlier line terminator.
      if (end !== -1 && (newline === -1 || end <= newline + 1)) { index = end; continue; }
    }
    if (str[index] === '\\' && index + 1 < length && !isLineBreak(str[index + 1])) {
      parts.push(str.slice(start, index));
      start = index + 1;
    }
  }
  parts.push(str.slice(start));
  return parts.join('');
};

exports.escapeLast = (input, char, lastIdx) => {
  const idx = input.lastIndexOf(char, lastIdx);
  if (idx === -1) return input;
  if (input[idx - 1] === '\\') return exports.escapeLast(input, char, idx - 1);
  return `${input.slice(0, idx)}\\${input.slice(idx)}`;
};

exports.removePrefix = (input, state = {}) => {
  let output = input;
  if (output.startsWith('./')) {
    output = output.slice(2);
    state.prefix = './';
  }
  return output;
};

exports.wrapOutput = (input, state = {}, options = {}) => {
  const prepend = options.contains ? '' : '^';
  const append = options.contains ? '' : '$';

  let output = `${prepend}(?:${input})${append}`;
  if (state.negated === true) {
    output = `(?:^(?!${output}).*$)`;
  }
  return output;
};

exports.basename = (path, { windows } = {}) => {
  const segs = path.split(windows ? /[\\/]/ : '/');
  const last = segs[segs.length - 1];

  if (last === '') {
    return segs[segs.length - 2];
  }

  return last;
};
