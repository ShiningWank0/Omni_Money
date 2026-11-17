import assert from 'node:assert/strict'

export const upstreamVersion = version => version.replace(/-[^-]+$/, '').split('+')[0].split('~').at(-1).split(':').at(-1)

// Parse .SRCINFO as data. PKGBUILD and archived programs are never evaluated.
export function recipeIndex(cache) {
  assert.ok(cache && typeof cache === 'object' && !Array.isArray(cache))
  const index = new Map()
  for (const value of Object.values(cache)) for (const text of Object.values(value.srcinfo || {})) {
    assert.equal(typeof text, 'string')
    const fields = new Map()
    for (const line of text.split('\n')) {
      const match = line.match(/^\s*(pkgbase|pkgname|pkgver|pkgrel|epoch) = (\S+)\s*$/)
      if (match) fields.set(match[1], [...(fields.get(match[1]) || []), match[2]])
    }
    const base = fields.get('pkgbase')?.[0], version = fields.get('pkgver')?.[0], release = fields.get('pkgrel')?.[0]
    if (!base || !version || !release) continue
    const epoch = fields.get('epoch')?.[0]
    const fullVersion = `${epoch && epoch !== '0' ? epoch + ':' : ''}${version}-${release}`
    for (const name of fields.get('pkgname') || []) {
      assert.match(name, /^[a-zA-Z0-9@+_.-]+$/)
      const item = { base, fullVersion, version: upstreamVersion(fullVersion) }
      // Several architecture entries may describe the same package, but a
      // contradictory recipe cannot be silently picked for a security scan.
      const prior = index.get(name)
      assert.ok(!prior || JSON.stringify(prior) === JSON.stringify(item), `ambiguous recipe for ${name}`)
      index.set(name, item)
    }
  }
  assert.ok(index.size > 0, 'empty MSYS2 recipe index')
  return index
}

function replaceCPE(cpe, version) {
  // MSYS2's own generator emits URI-binding CPEs; also support formatted CPEs.
  assert.match(version, /^[a-zA-Z0-9._~-]+$/)
  if (cpe.startsWith('cpe:/')) {
    const values = cpe.split(':'); assert.ok(values.length >= 5)
    values[4] = encodeURIComponent(version); return values.join(':')
  }
  if (cpe.startsWith('cpe:2.3:')) {
    const values = cpe.match(/(?:\\.|[^:])+/g); assert.equal(values.length, 13)
    values[5] = version; return values.join(':')
  }
  throw new Error('unsupported MSYS2 CPE format')
}

export function msysSBOM(packages, datasets) {
  const recipes = new Map(), metadata = new Map(), gaps = [], identities = new Map()
  for (const { cache, sbom } of datasets) {
    assert.equal(sbom.bomFormat, 'CycloneDX'); assert.ok(sbom.components?.length)
    for (const [name, recipe] of recipeIndex(cache)) {
      const prior = recipes.get(name)
      assert.ok(!prior || JSON.stringify(prior) === JSON.stringify(recipe), `conflicting MSYS2 source for ${name}`)
      recipes.set(name, recipe)
    }
    for (const component of sbom.components) {
      for (const property of component.properties || []) if (property.name === 'msys2:pkgbase') {
        metadata.set(property.value, [...(metadata.get(property.value) || []), component])
      }
    }
  }
  const components = [], seen = new Set()
  const lines = packages.trim().split('\n')
  assert.ok(lines.length > 0)
  for (const line of lines) {
    const match = line.match(/^([a-zA-Z0-9@+_.-]+) ([^\s]+)$/)
    assert.ok(match, 'malformed saved pacman inventory')
    const [, name, installed] = match
    assert.ok(!seen.has(name), 'duplicate saved MSYS2 package'); seen.add(name)
    const recipe = recipes.get(name)
    if (!recipe) { gaps.push(`${name}@${installed}: package recipe unavailable`); continue }
    const entries = metadata.get(recipe.base) || []
    let mapped = 0
    for (const original of entries) {
      if (!original.cpe && !original.purl) continue
      const version = upstreamVersion(installed)
      // Explicit ecosystem version overrides may not translate to an old
      // snapshot. Never substitute the current repository's version for it.
      if (installed !== recipe.fullVersion && original.version !== recipe.version) continue
      const component = { ...original, version: installed === recipe.fullVersion ? original.version : version,
        'bom-ref': `msys2/${name}/${mapped}`, properties: [{ name: 'syft:location:0:path', value: `msys2/${name}` }] }
      try {
        if (original.cpe) component.cpe = replaceCPE(original.cpe, component.version)
        if (original.purl) {
          assert.match(original.purl, /^pkg:[^\s]+@[^\s]+$/)
          component.purl = original.purl.replace(/@[^?#]+/, '@' + encodeURIComponent(component.version))
        }
      } catch { continue }
      components.push(component); mapped++
    }
    if (!mapped) gaps.push(`${name}@${installed}: no usable CPE/PURL mapping for this saved version`)
    identities.set(`msys2/${name}`, { name, installed, latest: recipe.fullVersion, base: recipe.base })
  }
  // No VEX from a newer package is applied to an older snapshot: a backport
  // may have changed only pkgrel. Matches remain review candidates.
  return { sbom: { bomFormat: 'CycloneDX', specVersion: '1.5', version: 1, components }, gaps, identities,
    installedCount: seen.size, mappedCount: [...identities].filter(([path]) => components.some(c => c.properties[0].value === path)).length }
}
