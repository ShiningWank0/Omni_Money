import { spawnSync } from 'node:child_process'
import { copyFileSync, mkdirSync, readFileSync, readdirSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { root, targets, targetConfigs, normalizeLineEndings } from './oss-notices.mjs'

export function verifyDependencies(buildInfo, catalog, target) {
  if (!targets.includes(target)) throw new Error('Unknown release target')
  if (!catalog.applicationModule) throw new Error('Missing application module identity')
  const expected = catalog.components.filter(component => component.id.startsWith('go:') && component.targets.includes(target)).map(component => `${component.name}@${component.version}`).sort()
  if (/^\s*=>/m.test(buildInfo)) throw new Error('Replaced Go dependency requires review')
  const dependencies = [...buildInfo.matchAll(/^\s*dep\s+(\S+)\s+(\S+)/gm)]
  if (new Set(dependencies.map(match => match[1])).size !== dependencies.length) throw new Error('Duplicate built Go dependency')
  const actual = dependencies
    .filter(match => !(match[1] === catalog.applicationModule && match[2] === '(devel)'))
    .map(match => `${match[1]}@${match[2]}`).sort()
  if (JSON.stringify(expected) !== JSON.stringify(actual)) throw new Error(`Built Go dependencies differ from reviewed OSS inventory for ${target}`)
  const goVersion = catalog.components.find(component => component.id === 'native:go').version
  if (!buildInfo.split('\n')[0].includes(`go${goVersion}`)) throw new Error('Built Go toolchain differs from reviewed runtime')
  const config = targetConfigs.find(config => config.id === target)
  for (const [key, value] of [['GOOS', config.goos], ['GOARCH', config.goarch]]) {
    if (buildInfo.match(new RegExp(`^\\s*build\\s+${key}=(\\S+)`, 'm'))?.[1] !== value) throw new Error('Built binary platform differs from reviewed release target')
  }
}

export function canonicalApacheLicense(text) {
  return text.split(/APPENDIX:/)[0].replace(/https?:\/\/www\.apache\.org\/licenses\//g, 'APACHE-LICENSE-URL').replace(/\s+/g, ' ').trim()
}

export function verifyNativeLicense(actual, reviewed) {
  if (normalizeLineEndings(actual).trim() !== normalizeLineEndings(reviewed).trim()) throw new Error('Built SQLCipher license differs from approved text')
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const [target, binary, sqlcipherPrefix, opensslVersion] = process.argv.slice(2)
    const catalog = JSON.parse(readFileSync(join(root, 'frontend/src/generated/thirdPartyNotices.json')))
    const result = spawnSync('go', ['version', '-m', binary], { encoding: 'utf8' })
    if (result.status !== 0) throw new Error('Cannot inspect built Go dependency inventory')
    verifyDependencies(result.stdout, catalog, target)
    if (!/^OpenSSL 3\./.test(opensslVersion || '')) throw new Error('Native OpenSSL license review required for a different release series')
    const directory = join(root, 'licenses/build', target)
    mkdirSync(join(directory, 'native'), { recursive: true })
    const sqlcipherLicense = normalizeLineEndings(readFileSync(join(sqlcipherPrefix, 'share/licenses/sqlcipher/LICENSE.md'), 'utf8'))
    const reviewed = readFileSync(join(root, 'licenses/native/sqlcipher.txt'), 'utf8')
    verifyNativeLicense(sqlcipherLicense, reviewed)
    writeFileSync(join(directory, 'native/SQLCipher-LICENSE.txt'), sqlcipherLicense)
    if (process.env.OSS_OPENSSL_LICENSE_DIRECTORY) {
      const source = process.env.OSS_OPENSSL_LICENSE_DIRECTORY
      const files = readdirSync(source).filter(name => /^(LICENSE|NOTICE|COPYING)/i.test(name))
      if (!files.length) throw new Error('OpenSSL distribution license is missing')
      const reviewedOpenSSL = catalog.components.find(component => component.id === 'native:openssl').files[0].text
      for (const file of files) {
        const text = readFileSync(join(source, file), 'utf8')
        if (!/^LICENSE/i.test(file) || canonicalApacheLicense(text) !== canonicalApacheLicense(reviewedOpenSSL)) throw new Error(`OpenSSL native notice review required: ${file}`)
        copyFileSync(join(source, file), join(directory, 'native', `OpenSSL-${file}`))
      }
    }
    writeFileSync(join(directory, 'BUILD_DEPENDENCIES.txt'), `${result.stdout}\n${opensslVersion}\n`)
  } catch (error) { console.error(error.message); process.exitCode = 1 }
}
