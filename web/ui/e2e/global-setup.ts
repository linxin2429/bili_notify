import { execFile } from 'node:child_process'
import { mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { promisify } from 'node:util'

const run = promisify(execFile)

export default async function globalSetup() {
  const buildDirectory = await mkdtemp(join(tmpdir(), 'bili-notify-e2e-harness-'))
  const repository = resolve(import.meta.dirname, '../../..')
  const binary = join(buildDirectory, process.platform === 'win32' ? 'harness.exe' : 'harness')

  try {
    await run('go', ['build', '-trimpath', '-o', binary, './e2e/harness'], {
      cwd: repository,
      env: { ...process.env, CGO_ENABLED: '0' },
    })
    process.env.BILI_NOTIFY_E2E_HARNESS = binary
  } catch (error) {
    await rm(buildDirectory, { recursive: true, force: true })
    throw error
  }

  return async () => {
    delete process.env.BILI_NOTIFY_E2E_HARNESS
    await rm(buildDirectory, { recursive: true, force: true })
  }
}
