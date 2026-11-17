import assert from 'node:assert/strict'
import { dirname, join } from 'node:path'
import { jsonStream } from './security-reports.mjs'

export function moduleQueries(raw) {
  const values = jsonStream(raw), queries = []
  for (const item of values) {
    assert.ok(item.Path && !item.Error && !item.Replace, 'module graph is incomplete or replaced')
    if (item.Main) continue
    assert.match(item.Version, /^v\S+$/)
    queries.push({ package: { name: item.Path, ecosystem: 'Go' }, version: item.Version })
  }
  assert.ok(queries.length > 0, 'empty selected module graph')
  return queries
}

export async function auditModules(input, execute, query, directory) {
  const results = []
  for (const item of input.modules) {
    try {
      const raw = execute(['list', '-mod=readonly', '-modfile=' + join(directory, item.path), '-m', '-json', 'all'], dirname(join(directory, item.path)))
      const queries = moduleQueries(raw)
      results.push({ name: item.path, queries, vulnerabilities: await query(queries) })
    } catch { results.push({ name: item.path, error: '選択中のGo依存graphまたはOSV検査を完全に取得できませんでした。' }) }
  }
  for (const item of input.remoteModules) {
    const name = `${item.module}@${item.version}`
    try {
      const selected = JSON.parse(execute(['mod', 'download', '-json', `${item.module}@${item.version}`], directory))
      assert.ok(selected.Dir && !selected.Error)
      const raw = execute(['list', '-mod=readonly', '-m', '-json', 'all'], selected.Dir)
      const queries = [{ package: { name: item.module, ecosystem: 'Go' }, version: item.version }, ...moduleQueries(raw)]
      results.push({ name, queries, vulnerabilities: await query(queries) })
    } catch { results.push({ name, error: '独立して取得するGo製CIツールの依存graphまたはOSV検査を完全に取得できませんでした。' }) }
  }
  try {
    const queries = [{ package: { name: 'stdlib', ecosystem: 'Go' }, version: input.goVersion }]
    results.push({ name: 'Go-toolchain', queries, vulnerabilities: await query(queries) })
  } catch { results.push({ name: 'Go-toolchain', error: 'Go標準ライブラリの版に対するOSV検査を取得できませんでした。' }) }
  return results
}

export function osvBatch(data, queries) {
  assert.ok(Array.isArray(data.results) && data.results.length === queries.length, 'OSV batch is incomplete')
  return data.results.flatMap((result, i) => {
    assert.ok(!result.next_page_token, 'OSV query was truncated')
    assert.ok(result.vulns === undefined || Array.isArray(result.vulns))
    return (result.vulns || []).map(v => {
      assert.ok(typeof v.id === 'string' && v.id.length > 0 && v.id.length < 200)
      return { id: v.id, name: queries[i].package.name, version: queries[i].version }
    })
  })
}
