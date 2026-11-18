// The upstream suite uses only synchronous Mocha describe/it APIs. Run its
// unchanged assertions with Node's built-in test runner, without adding Mocha.
const { describe, it } = require('node:test');
globalThis.describe = describe;
globalThis.it = it;
