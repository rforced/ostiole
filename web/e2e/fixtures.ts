import { test as base } from '@playwright/test'

import { startServer, type RunningServer } from './server'

interface Worker {
  port: number
  file: string
  server: RunningServer | null
}

export { expect } from '@playwright/test'

/**
 * The tests of a file run in order against a server of their own. Each
 * worker keeps one on a port of its own and starts it afresh whenever a new
 * file begins, so no file sees what another left behind and files run side
 * by side.
 */
export const test = base.extend<{ seeded: boolean; server: RunningServer }, { worker: Worker }>({
  // Every file starts from the router global-setup.ts configured, except
  // 01-first-run.spec.ts, which makes one.
  seeded: [true, { option: true }],
  worker: [
    // eslint-disable-next-line no-empty-pattern
    async ({}, use, workerInfo) => {
      const worker: Worker = { port: 18100 + workerInfo.parallelIndex, file: '', server: null }
      await use(worker)
      await worker.server?.stop()
    },
    { scope: 'worker' },
  ],
  server: [
    async ({ worker, seeded }, use, testInfo) => {
      if (worker.file !== testInfo.file) {
        await worker.server?.stop()
        worker.server = null
        worker.server = await startServer({
          port: worker.port,
          seed: seeded ? process.env.E2E_SEED : '',
        })
        worker.file = testInfo.file
      }
      if (!worker.server) throw new Error('no server started')
      await use(worker.server)
    },
    { auto: true },
  ],
  baseURL: async ({ server }, use) => {
    await use(server.url)
  },
})
