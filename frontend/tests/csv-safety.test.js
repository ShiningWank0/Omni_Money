import assert from 'node:assert/strict'
import { afterEach, beforeEach, test } from 'node:test'

globalThis.window = {
  location: {
    origin: 'https://money.example.test',
    pathname: '/'
  },
  dispatchEvent() {}
}

const domState = {
  anchors: [],
  appended: [],
  blobs: [],
  revoked: [],
  clickError: null
}

globalThis.document = {
  body: {
    appendChild(anchor) {
      domState.appended.push(anchor)
    }
  },
  createElement(name) {
    assert.equal(name, 'a')
    const anchor = {
      href: '',
      download: '',
      clicked: false,
      removed: false,
      click() {
        this.clicked = true
        if (domState.clickError) throw domState.clickError
      },
      remove() {
        this.removed = true
      }
    }
    domState.anchors.push(anchor)
    return anchor
  }
}

const originalCreateObjectURL = URL.createObjectURL
const originalRevokeObjectURL = URL.revokeObjectURL
URL.createObjectURL = blob => {
  domState.blobs.push(blob)
  return `blob:test-${domState.blobs.length}`
}
URL.revokeObjectURL = value => domState.revoked.push(value)

const { backupToCSVFile, importCSV } = await import('../src/utils/api.js')
const { canStartCSVImport, csvExportWarning } = await import('../src/utils/csvSafety.js')

beforeEach(() => {
  domState.anchors.length = 0
  domState.appended.length = 0
  domState.blobs.length = 0
  domState.revoked.length = 0
  domState.clickError = null
})

afterEach(() => {
  delete globalThis.fetch
})

test('CSV export rejects HTTP errors and non-CSV responses before creating a download', async () => {
  globalThis.fetch = async () => new Response(JSON.stringify({ error: 'export rejected' }), {
    status: 503,
    headers: { 'Content-Type': 'application/json' }
  })
  await assert.rejects(backupToCSVFile(), /export rejected/)
  assert.equal(domState.anchors.length, 0)
  assert.equal(domState.blobs.length, 0)

  globalThis.fetch = async () => new Response('<html>proxy error</html>', {
    status: 200,
    headers: { 'Content-Type': 'text/html' }
  })
  await assert.rejects(backupToCSVFile(), /CSVではない応答/)
  assert.equal(domState.anchors.length, 0)
  assert.equal(domState.blobs.length, 0)
})

test('CSV export always removes its anchor and revokes its Object URL', async () => {
  globalThis.fetch = async () => new Response('account,amount\ncash,100\n', {
    status: 200,
    headers: { 'Content-Type': 'text/csv; charset=utf-8' }
  })
  const name = await backupToCSVFile()
  assert.match(name, /^transactions_backup_\d{4}-\d{2}-\d{2}\.csv$/)
  assert.equal(domState.anchors.length, 1)
  assert.equal(domState.appended.length, 1)
  assert.equal(domState.anchors[0].clicked, true)
  assert.equal(domState.anchors[0].removed, true)
  assert.deepEqual(domState.revoked, ['blob:test-1'])
  const bytes = new Uint8Array(await domState.blobs[0].arrayBuffer())
  assert.deepEqual([...bytes.slice(0, 3)], [0xEF, 0xBB, 0xBF])
  assert.match(new TextDecoder().decode(bytes.slice(3)), /^account,amount/)

  domState.clickError = new Error('download blocked')
  await assert.rejects(backupToCSVFile(), /download blocked/)
  assert.equal(domState.anchors[1].removed, true)
  assert.deepEqual(domState.revoked, ['blob:test-1', 'blob:test-2'])
})

test('CSV export refuses oversized legacy-browser fallback before reading the body', async () => {
  globalThis.fetch = async () => new Response('small body', {
    status: 200,
    headers: {
      'Content-Type': 'text/csv; charset=utf-8',
      'Content-Length': String(64 * 1024 * 1024)
    }
  })
  await assert.rejects(backupToCSVFile(), /大きすぎます/)
  assert.equal(domState.anchors.length, 0)
  assert.equal(domState.blobs.length, 0)
})

test('CSV export acquires the picker before fetch and does not fetch on cancel', async () => {
  let canceled = 0
  let fetchCalls = 0
  const previousPicker = window.showSaveFilePicker
  window.showSaveFilePicker = async () => { throw new Error('picker canceled') }
  globalThis.fetch = async () => { fetchCalls++; throw new Error('network must not start') }
  await assert.rejects(backupToCSVFile(), /picker canceled/)
  assert.equal(fetchCalls, 0)
  assert.equal(canceled, 0)

  window.showSaveFilePicker = async () => ({
    createWritable: async () => { throw new Error('writable failed') }
  })
  globalThis.fetch = async () => ({
    ok: true,
    status: 200,
    headers: new Headers({ 'Content-Type': 'text/csv; charset=utf-8' }),
    body: {
      pipeTo: async () => { throw new Error('stream failed') },
      cancel: async () => { canceled++ }
    }
  })
  await assert.rejects(backupToCSVFile(), /writable failed/)
  assert.equal(canceled, 1)
  if (previousPicker === undefined) delete window.showSaveFilePicker
  else window.showSaveFilePicker = previousPicker
})

test('CSV export propagates picker SecurityError without starting fetch', async () => {
  let fetchCalls = 0
  const previousPicker = window.showSaveFilePicker
  window.showSaveFilePicker = async () => { throw new DOMException('activation lost', 'SecurityError') }
  globalThis.fetch = async () => { fetchCalls++; throw new Error('network must not start') }
  await assert.rejects(backupToCSVFile(), error => error.name === 'SecurityError')
  assert.equal(fetchCalls, 0)
  if (previousPicker === undefined) delete window.showSaveFilePicker
  else window.showSaveFilePicker = previousPicker
})

test('CSV export streams the response into the handle acquired by the click', async () => {
  const events = []
  const previousPicker = window.showSaveFilePicker
  window.showSaveFilePicker = async () => {
    events.push('picker')
    return {
      createWritable: async () => {
        events.push('writable')
        return { close: async () => events.push('close') }
      }
    }
  }
  globalThis.fetch = async () => {
    events.push('fetch')
    return {
      ok: true,
      status: 200,
      headers: new Headers({ 'Content-Type': 'text/csv; charset=utf-8' }),
      body: {
        pipeTo: async writable => {
          assert.ok(writable)
          events.push('pipe')
        },
        cancel: async () => events.push('cancel')
      }
    }
  }
  await backupToCSVFile()
  assert.deepEqual(events, ['picker', 'fetch', 'writable', 'pipe'])
  assert.equal(domState.anchors.length, 0)
  if (previousPicker === undefined) delete window.showSaveFilePicker
  else window.showSaveFilePicker = previousPicker
})

test('CSV export supports a response without a reader using the bounded arrayBuffer fallback', async () => {
  const bytes = new TextEncoder().encode('account,amount\ncash,100\n')
  globalThis.fetch = async () => ({
    ok: true,
    status: 200,
    headers: new Headers({ 'Content-Type': 'text/csv; charset=utf-8' }),
    body: null,
    arrayBuffer: async () => bytes.buffer
  })
  const name = await backupToCSVFile()
  assert.match(name, /^transactions_backup_\d{4}-\d{2}-\d{2}\.csv$/)
  const downloaded = new Uint8Array(await domState.blobs[0].arrayBuffer())
  assert.deepEqual([...downloaded.slice(0, 3)], [0xEF, 0xBB, 0xBF])
  assert.match(new TextDecoder().decode(downloaded.slice(3)), /^account,amount/)
})

test('CSV import rejects error and malformed success responses', async () => {
  globalThis.fetch = async () => new Response(JSON.stringify({ error: 'invalid CSV' }), {
    status: 400,
    headers: { 'Content-Type': 'application/json' }
  })
  await assert.rejects(importCSV('bad', 'append'), /invalid CSV/)

  globalThis.fetch = async () => new Response(JSON.stringify({ imported_count: '1' }), {
    status: 200,
    headers: { 'Content-Type': 'application/json' }
  })
  await assert.rejects(importCSV('header', 'append'), /応答が不正/)

  globalThis.fetch = async () => new Response(JSON.stringify({ imported_count: 2 }), {
    status: 200,
    headers: { 'Content-Type': 'application/json' }
  })
  assert.equal(await importCSV('header', 'append'), 2)
})

test('replace import and export warnings stay fail-closed', () => {
  assert.equal(canStartCSVImport({ hasFile: true, importing: false, mode: 'replace', replaceConfirmed: false }), false)
  assert.equal(canStartCSVImport({ hasFile: true, importing: false, mode: 'replace', replaceConfirmed: true }), true)
  assert.equal(canStartCSVImport({ hasFile: true, importing: true, mode: 'append', replaceConfirmed: false }), false)
  assert.match(csvExportWarning(true), /保存先ディレクトリを選択/)
  assert.match(csvExportWarning(false), /ブラウザのダウンロード先/)
  assert.match(csvExportWarning(false), /完全に消去されたことを保証できません/)
})

process.on('exit', () => {
  URL.createObjectURL = originalCreateObjectURL
  URL.revokeObjectURL = originalRevokeObjectURL
})
