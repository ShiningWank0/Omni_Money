import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { cpSync, existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = resolve(fileURLToPath(new URL('..', import.meta.url)))
const os = { win32: 'windows', darwin: 'darwin', linux: 'linux' }[process.platform]
const arch = { x64: 'amd64', arm64: 'arm64' }[process.arch]
assert.ok(os && arch)
assert.ok(!process.env.BUILD_PLATFORM || process.env.BUILD_PLATFORM === os + '/' + arch, 'Only native CI builds are allowed')
const version = process.env.APP_VERSION || 'dev'
assert.match(version, /^(?:dev|v?\d+\.\d+\.\d+(?:-[A-Za-z0-9.-]+)?)$/)
const coreVersion = version.match(/\d+\.\d+\.\d+/)?.[0] || '0.0.0'
const dev = process.env.DEV === 'true'
const tags = new Set((process.env.EXTRA_TAGS || '').split(',').filter(Boolean))
for (const tag of tags) assert.match(tag, /^[a-z][a-z0-9_]*$/)
for (const bad of ['server', 'omni_wails_browser', 'bindings', 'purego']) assert.ok(!tags.has(bad), 'Wrong native build tag: ' + bad)
tags.add(dev ? 'dev' : 'production'); tags.add('desktop')
tags.add('libsqlite3'); tags.add('sqlite_omit_load_extension')
if (os === 'linux') tags.add('gtk3')
const run = (program, args, cwd = root, env = process.env) => {
  const result = spawnSync(program, args, { cwd, env, stdio: 'inherit', shell: false })
  assert.equal(result.status, 0, String(result.error || program + ' failed'))
}
const npm = join(dirname(process.execPath), os === 'windows' ? 'node_modules/npm/bin/npm-cli.js' : '../lib/node_modules/npm/bin/npm-cli.js')
assert.ok(existsSync(npm), 'Use the official pinned Node distribution')
run(process.execPath, [npm, 'ci', '--ignore-scripts'], join(root, 'frontend'))
run(process.execPath, ['build/desktop.mjs', 'build'], join(root, 'frontend'))
const bin = join(root, 'build/bin')
mkdirSync(bin, { recursive: true })
let syso
if (os === 'windows') {
  const result = spawnSync('go', ['env', 'GOPATH'], { encoding: 'utf8' })
  assert.equal(result.status, 0)
  const cli = join(result.stdout.trim(), 'bin/wails3.exe')
  const info = join(process.env.RUNNER_TEMP, 'omni-windows-info.json')
  writeFileSync(info, JSON.stringify({ fixed: { file_version: coreVersion }, info: { '0000': {
    ProductVersion: version, CompanyName: 'ShiningWank0', FileDescription: 'Omni Money', ProductName: 'Omni Money' } } }))
  syso = join(root, 'wails_windows_' + arch + '.syso')
  assert.ok(!existsSync(syso), 'Refusing to replace a Windows resource file')
  run(cli, ['generate', 'syso', '-arch', arch, '-icon', join(root, 'build/windows/icon.ico'),
    '-manifest', join(root, 'build/windows/wails.exe.manifest'), '-info', info, '-out', syso])
}
try {
  const flags = ['-X main.version=' + version]
  if (!dev) flags.push('-s', '-w')
  if (os === 'windows' && !dev) flags.push('-H windowsgui')
  if (os === 'darwin') {
    const lock = JSON.parse(readFileSync(join(root, 'build/locks/macos.json'), 'utf8'))
    assert.equal(process.env.MACOSX_DEPLOYMENT_TARGET, '13.0')
    flags.push('-macos=13.0', '-macsdk=' + lock.xcode.macosSDK)
  }
  run('go', ['build', '-mod=readonly', '-trimpath', '-buildvcs=false', '-tags', [...tags].join(','),
    '-ldflags', flags.join(' '), '-o', join(bin, 'omni_money' + (os === 'windows' ? '.exe' : '')), '.'])
  if (os === 'darwin') {
    const bundle = join(bin, 'Omni Money.app/Contents')
    rmSync(join(bin, 'Omni Money.app'), { recursive: true, force: true })
    mkdirSync(join(bundle, 'MacOS'), { recursive: true })
    mkdirSync(join(bundle, 'Resources'), { recursive: true })
    cpSync(join(bin, 'omni_money'), join(bundle, 'MacOS/omni_money'))
    cpSync(join(root, 'build/darwin/iconfile.icns'), join(bundle, 'Resources/iconfile.icns'))
    const metadata = readFileSync(join(root, 'build/darwin/Info.plist'), 'utf8')
      .replaceAll('{{VERSION}}', coreVersion)
    writeFileSync(join(bundle, 'Info.plist'), metadata)
  }
} finally { if (syso) rmSync(syso, { force: true }) }
