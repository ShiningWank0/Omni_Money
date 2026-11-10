import { spawn } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { resolveDisplayTarget } from './oss-catalog.mjs'

const command = process.argv[2]
if (!['build', 'dev'].includes(command)) throw new Error('Expected desktop frontend build or dev')
const env = { ...process.env, OMNI_OSS_MODE: 'desktop' }
env.VITE_OSS_TARGET = resolveDisplayTarget(env)
const child = spawn(process.execPath, [fileURLToPath(new URL('../node_modules/vite/bin/vite.js', import.meta.url)), ...(command === 'dev' ? [] : ['build'])], { stdio: 'inherit', env })
for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => child.kill(signal))
child.once('error', error => { console.error(error.message); process.exitCode = 1 })
child.once('exit', (code, signal) => { process.exitCode = code ?? (signal ? 1 : 0) })
