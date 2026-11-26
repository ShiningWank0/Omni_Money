import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import test from 'node:test'
import { capturePaths, checkLinuxEnvironments, workflowPaths } from './linux-environments.mjs'

function fixture(t) {
  const root = mkdtempSync(join(tmpdir(), 'omni-linux-lock-'))
  t.after(() => rmSync(root, { recursive: true, force: true }))
  for (const path of ['build/locks/linux-environments.json', 'Dockerfile', ...workflowPaths, ...capturePaths]) {
    mkdirSync(dirname(join(root, path)), { recursive: true })
    writeFileSync(join(root, path), readFileSync(new URL(`../${path}`, import.meta.url)))
  }
  return root
}

test('all normal Linux CI/release inputs match one immutable lock', () => checkLinuxEnvironments())
test('major updates cannot delete the capture workflow or its source recipe', t => {
  for (const path of ['.github/workflows/capture-build-environments.yml', 'build/environments/ubuntu.Dockerfile']) {
    const root = fixture(t)
    rmSync(join(root, path))
    assert.throws(() => checkLinuxEnvironments(root), error => error.code === 'ENOENT' && error.path === join(root, path))
  }
})
test('a workflow-only digest change is rejected instead of silently changing the environment', t => {
  const root = fixture(t), path = join(root, workflowPaths[0])
  writeFileSync(path, readFileSync(path, 'utf8').replace(/sha256:[a-f0-9]{64}/g, `sha256:${'f'.repeat(64)}`))
  assert.throws(() => checkLinuxEnvironments(root), /differs from reviewed lock/)
})
test('moving OS package resolution cannot return to an ordinary Docker build', t => {
  const root = fixture(t), path = join(root, 'Dockerfile')
  writeFileSync(path, readFileSync(path, 'utf8') + '\nRUN apk add openssl-dev\n')
  assert.throws(() => checkLinuxEnvironments(root), /must not resolve OS packages again/)
})

test('the fixed APK install requires signature verification and no network', t => {
  const root = fixture(t), path = join(root, 'Dockerfile')
  const dockerfile = readFileSync(path, 'utf8')
  for (const [before, after] of [
    ['--network=none --mount=type=bind,from=', '--network=default --mount=type=bind,from='],
    ['apk verify "$@"', 'true'],
    ['apk add --no-network --no-cache', 'apk add --no-cache'],
  ]) {
    assert.ok(dockerfile.includes(before))
    writeFileSync(path, dockerfile.replace(before, after))
    assert.throws(() => checkLinuxEnvironments(root), /must not resolve OS packages again/)
  }
})

test('job containers cannot silently lose orphan reaping', t => {
  const root = fixture(t), path = join(root, '.github/workflows/release-desktop.yml')
  writeFileSync(path, readFileSync(path, 'utf8').replace('--init ', ''))
  assert.throws(() => checkLinuxEnvironments(root), /must reap orphaned child processes/)
})

test('OCI export allocates a writable path from the container environment', t => {
  const root = fixture(t), temp = join(root, 'container-temp'), output = join(root, 'outputs')
  mkdirSync(temp)
  const workflow = readFileSync(join(root, '.github/workflows/release-docker.yml'), 'utf8')
  const setup = workflow.match(/id: oci-output\n\s+run: \|\n([\s\S]*?)\n\s+- name:/)?.[1]
  assert.ok(setup, 'OCI export path setup is missing')
  const result = spawnSync('bash', ['-c', setup], {
    env: { ...process.env, RUNNER_TEMP: temp, GITHUB_OUTPUT: output }, encoding: 'utf8',
  })
  assert.equal(result.status, 0, result.stderr)
  const path = readFileSync(output, 'utf8').trim().replace(/^path=/, '')
  assert.ok(path.startsWith(temp + '/'))
  writeFileSync(path, 'OCI fixture')
  assert.match(workflow, /outputs: type=oci,dest=\$\{\{ steps\.oci-output\.outputs\.path \}\}/)
})

test('scanned digests cross container jobs through their allocated paths, never host runner paths', t => {
  const root = fixture(t), temp = join(root, 'container-temp'), hostTemp = join(root, 'host-only-temp')
  mkdirSync(temp)
  // A host path is deliberately unusable inside the simulated container.
  // A regular file makes the old mkdir fail even when tests run as root.
  writeFileSync(hostTemp, 'not a mapped container directory')
  const workflow = readFileSync(join(root, '.github/workflows/release-docker.yml'), 'utf8')
  const step = name => {
    const block = workflow.split(`      - name: ${name}\n`)[1]?.split('\n      - name: ')[0]
    assert.ok(block, `missing workflow step ${name}`)
    const source = block.split('        run: |\n')[1]
    assert.ok(source, `missing executable body for ${name}`)
    return source.split('\n').map(line => line.startsWith('          ') ? line.slice(10) : line).join('\n')
      .replace(/\$\{\{\s*runner\.temp\s*\}\}/g, hostTemp)
  }
  const digest = `sha256:${'a'.repeat(64)}`, second = 'b'.repeat(64)
  const run = (source, extra = {}) => spawnSync('bash', ['-c', source], {
    env: { ...process.env, RUNNER_TEMP: temp, DIGEST: digest, IMAGE: 'ghcr.io/example/app', VERSION: '2.6.0',
      REHEARSAL: 'false', ...extra }, cwd: root, encoding: 'utf8', timeout: 5000,
  })
  const allocate = (name, output) => {
    const result = run(step(name), { GITHUB_OUTPUT: output })
    assert.equal(result.status, 0, result.stderr)
    const path = readFileSync(output, 'utf8').trim().replace(/^path=/, '')
    assert.ok(path.startsWith(temp + '/'))
    return path
  }
  const exported = allocate('Export scanned digest', join(root, 'export-output'))
  assert.deepEqual(readdirSync(exported), ['a'.repeat(64)])
  assert.notEqual(run(step('Export scanned digest'), { DIGEST: 'not-a-sha256', GITHUB_OUTPUT: join(root, 'invalid-output') }).status, 0)
  const downloaded = allocate('Allocate scanned digest directory', join(root, 'download-output'))
  assert.notEqual(downloaded, exported)
  copyFileSync(join(exported, 'a'.repeat(64)), join(downloaded, 'a'.repeat(64)))
  writeFileSync(join(downloaded, second), '')
  for (const name of ['Stage candidate index', 'Publish verified release tags']) {
    const source = step(name)
    const selection = source.slice(0, source.indexOf('docker buildx imagetools create'))
    assert.ok(selection.includes('sources+='), 'Must exercise the actual source selection before Docker publishing')
    const selected = run(selection + '\nprintf \'%s\\n\' "${sources[@]}"', { DIGEST_DIRECTORY: downloaded })
    assert.equal(selected.status, 0, selected.stderr)
    assert.deepEqual(selected.stdout.trim().split('\n').sort(), [`ghcr.io/example/app@${digest}`, `ghcr.io/example/app@sha256:${second}`])
    writeFileSync(join(downloaded, 'invalid-digest'), '')
    assert.notEqual(run(selection, { DIGEST_DIRECTORY: downloaded }).status, 0, 'Unexpected artifact must block publication')
    rmSync(join(downloaded, 'invalid-digest'))
    rmSync(join(downloaded, second))
    assert.notEqual(run(selection, { DIGEST_DIRECTORY: downloaded }).status, 0, 'A missing architecture must block publication')
    writeFileSync(join(downloaded, second), '')
  }
  assert.match(workflow, /path: \$\{\{ steps\.digest-output\.outputs\.path \}\}\/\*/)
  assert.match(workflow, /path: \$\{\{ steps\.digest-input\.outputs\.path \}\}\n/)
  assert.equal([...workflow.matchAll(/DIGEST_DIRECTORY: \$\{\{ steps\.digest-input\.outputs\.path \}\}/g)].length, 2)
  assert.doesNotMatch(workflow, /\$\{\{\s*runner\.temp\s*\}\}/, 'The host runner path must not enter a container job')
})

test('Docker and the OCI attestation reader share one job-local credential file', t => {
  const root = fixture(t), home = join(root, 'job-home'), temp = join(root, 'job-temp')
  mkdirSync(home); mkdirSync(temp)
  const setup = readFileSync(new URL('../scripts/setup-fixed-docker.sh', import.meta.url), 'utf8')
  const allocation = setup.slice(setup.indexOf('dest='), setup.indexOf('\npython3 -'))
  const exported = setup.match(/^export PATH=.* DOCKER_CONFIG=.*$/m)?.[0]
  assert.ok(allocation && exported, 'Missing fixed Docker configuration setup')
  const env = { ...process.env, HOME: home, RUNNER_TEMP: temp, DOCKER_CONFIG: join(root, 'inherited-foreign-config') }
  const configured = spawnSync('bash', ['-c', allocation + '\n' + exported + '\nprintf \'%s\' "$DOCKER_CONFIG"'], { env, encoding: 'utf8' })
  assert.equal(configured.status, 0, configured.stderr)
  assert.equal(configured.stdout, join(home, '.docker'))
  const config = join(configured.stdout, 'config.json')
  writeFileSync(config, JSON.stringify({ auths: { 'ghcr.io': { auth: Buffer.from('fixture:fake-token').toString('base64') } } }), { mode: 0o600 })
  // This is the upstream @sigstore/oci contract: os.homedir()/.docker/config.json.
  // The inherited DOCKER_CONFIG points elsewhere to reproduce the old mismatch.
  const consumer = spawnSync(process.execPath, ['--input-type=module', '-e', `
    import assert from 'node:assert/strict'; import { readFileSync, realpathSync } from 'node:fs';
    import { homedir } from 'node:os'; import { join } from 'node:path';
    const defaults = join(homedir(), '.docker/config.json');
    assert.equal(realpathSync(defaults), realpathSync(join(process.env.DOCKER_CONFIG, 'config.json')));
    assert.equal(JSON.parse(readFileSync(defaults)).auths['ghcr.io'].auth, Buffer.from('fixture:fake-token').toString('base64'));
  `], { env: { ...env, DOCKER_CONFIG: configured.stdout }, encoding: 'utf8' })
  assert.equal(consumer.status, 0, consumer.stderr)
  const workflow = readFileSync(join(root, '.github/workflows/release-docker.yml'), 'utf8')
  const guard = workflow.split('      - name: Verify shared registry credential location\n')[1]?.split('\n      - name: ')[0]?.split('        run: |\n')[1]
  assert.ok(guard, 'The publication job must verify the shared credential location')
  const run = overrides => spawnSync('bash', ['-c', guard], { env: { ...env, DOCKER_CONFIG: configured.stdout, ...overrides }, encoding: 'utf8' })
  assert.equal(run({}).status, 0)
  assert.notEqual(run({ DOCKER_CONFIG: join(root, 'foreign') }).status, 0, 'Different credential locations must fail')
  rmSync(config)
  assert.notEqual(run({}).status, 0, 'Missing credentials must fail before signing or publishing')
})
