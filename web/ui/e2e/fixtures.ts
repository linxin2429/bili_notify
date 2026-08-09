import { expect, request, test as base, type APIRequestContext } from '@playwright/test'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { spawn, type ChildProcessWithoutNullStreams } from 'node:child_process'

export interface HarnessManifest {
  event: 'e2e_ready'
  admin_url: string
  observe_url: string
  webhook_url: string
  control_url: string
  control_token: string
  setup_code: string
}

export interface HarnessState {
  counts: Record<string, number>
  messages: Array<Record<string, unknown>>
  unexpected: string[] | null
  new_dynamic: boolean
  new_comment: boolean
  webhook_mode: 'success' | 'permanent_failure'
  generation: number
}

export interface Harness {
  manifest: HarnessManifest
  state: () => Promise<HarnessState>
  setFeed: (newDynamic: boolean) => Promise<void>
  setComments: (newComment: boolean) => Promise<void>
  setWebhook: (mode: HarnessState['webhook_mode']) => Promise<void>
  restart: () => Promise<void>
}

interface HarnessFixture {
  harness: Harness
}

export const test = base.extend<HarnessFixture>({
  harness: async ({}, use, testInfo) => {
    const dataDir = mkdtempSync(join(tmpdir(), 'bili-notify-e2e-'))
    const repository = resolve(import.meta.dirname, '../../..')
    let server: ChildProcessWithoutNullStreams | undefined
    let control: APIRequestContext | undefined
    let logs = ''

    try {
      const started = await startHarness(repository, dataDir, text => {
        logs += text
      })
      server = started.server
      control = await request.newContext({
        baseURL: started.manifest.control_url,
        ignoreHTTPSErrors: true,
        extraHTTPHeaders: { Authorization: `Bearer ${started.manifest.control_token}` },
      })

      const harness: Harness = {
        manifest: started.manifest,
        state: async () => {
          const response = await control!.get(`${started.manifest.control_url}/state`)
          expect(response.ok()).toBeTruthy()
          return response.json() as Promise<HarnessState>
        },
        setFeed: async newDynamic => {
          const response = await control!.put(`${started.manifest.control_url}/feed`, { data: { new_dynamic: newDynamic } })
          expect(response.status()).toBe(204)
        },
        setComments: async newComment => {
          const response = await control!.put(`${started.manifest.control_url}/comments`, { data: { new_comment: newComment } })
          expect(response.status()).toBe(204)
        },
        setWebhook: async mode => {
          const response = await control!.put(`${started.manifest.control_url}/webhook`, { data: { mode } })
          expect(response.status()).toBe(204)
        },
        restart: async () => {
          const response = await control!.post(`${started.manifest.control_url}/restart`)
          expect(response.ok()).toBeTruthy()
        },
      }
      await use(harness)
    } finally {
      if (testInfo.status !== testInfo.expectedStatus && logs !== '') {
        await testInfo.attach('harness.log', { body: logs, contentType: 'text/plain' })
      }
      try {
        await control?.dispose()
      } finally {
        try {
          await stopHarness(server)
        } finally {
          rmSync(dataDir, { recursive: true, force: true })
        }
      }
    }
  },
})

export { expect }

async function startHarness(repository: string, directory: string, capture: (text: string) => void) {
  const server = spawn('go', ['run', '-trimpath', './e2e/harness', '--data-dir', directory], {
    cwd: repository,
    detached: true,
    env: { ...process.env, CGO_ENABLED: '0' },
  })
  try {
    const manifest = await new Promise<HarnessManifest>((resolveReady, reject) => {
      let stdout = ''
      const timer = setTimeout(() => reject(new Error('harness startup timed out')), 30_000)
      const consume = (chunk: Buffer, source: 'stdout' | 'stderr') => {
        const text = chunk.toString()
        capture(text)
        if (source !== 'stdout') return
        stdout += text
        const lines = stdout.split('\n')
        stdout = lines.pop() || ''
        for (const line of lines) {
          try {
            const value = JSON.parse(line) as Partial<HarnessManifest>
            if (value.event === 'e2e_ready') {
              clearTimeout(timer)
              resolveReady(value as HarnessManifest)
            }
          } catch {
            // Non-manifest output remains available in the failure attachment.
          }
        }
      }
      server.stdout.on('data', chunk => consume(chunk, 'stdout'))
      server.stderr.on('data', chunk => consume(chunk, 'stderr'))
      server.once('exit', code => {
        clearTimeout(timer)
        reject(new Error(`harness exited with ${code}`))
      })
    })
    return { server, manifest }
  } catch (error) {
    await stopHarness(server)
    throw error
  }
}

async function stopHarness(server?: ChildProcessWithoutNullStreams) {
  if (!server || server.exitCode !== null) return
  const exited = new Promise<void>(resolveExit => server.once('exit', () => resolveExit()))
  if (server.pid !== undefined) {
    try {
      process.kill(-server.pid, 'SIGTERM')
    } catch (error) {
      if ((error as { code?: string }).code !== 'ESRCH') throw error
    }
  }
  await Promise.race([
    exited,
    new Promise<void>((_, reject) => setTimeout(() => reject(new Error('harness shutdown timed out')), 25_000)),
  ])
}
