import { spawn } from 'node:child_process'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { createInterface } from 'node:readline'
import { fileURLToPath } from 'node:url'

const SERVE = fileURLToPath(new URL('./serve.sh', import.meta.url))

const answers = (url: string) =>
  fetch(`${url}/api/v1/health`).then(
    (res) => res.ok,
    () => false,
  )

export interface RunningServer {
  url: string
  dir: string
  stop: () => Promise<void>
}

/**
 * Starts serve.sh on a port and waits until the server answers. A port
 * something already answers on is refused, so a server left over from an
 * earlier run is never the one tested.
 *
 * Seed is a directory to start from, dir one to use and keep. Without one
 * the server gets a directory of its own, gone once it stops; a test reads
 * and writes its files there.
 */
export async function startServer({
  port,
  seed = '',
  dir = '',
}: {
  port: number
  seed?: string
  dir?: string
}): Promise<RunningServer> {
  const url = `http://127.0.0.1:${port}`
  if (await answers(url)) throw new Error(`something already answers on ${url}`)
  const kept = dir !== ''
  if (!kept) dir = mkdtempSync(join(tmpdir(), 'ostiole-e2e-'))
  const child = spawn('sh', [SERVE], {
    env: { ...process.env, E2E_PORT: String(port), E2E_SEED: seed, E2E_DIR: dir },
    stdio: ['ignore', 'ignore', 'pipe'],
  })
  createInterface({ input: child.stderr }).on('line', (line) =>
    process.stderr.write(`[ostiole :${port}] ${line}\n`),
  )
  const exited = new Promise((resolve) => child.once('exit', resolve))
  const stop = async () => {
    child.kill('SIGTERM')
    await exited
    if (!kept) rmSync(dir, { recursive: true, force: true })
  }
  const deadline = Date.now() + 30_000
  while (!(await answers(url))) {
    if (child.exitCode !== null) throw new Error(`the server on ${url} exited`)
    if (Date.now() > deadline) {
      await stop()
      throw new Error(`the server on ${url} did not start`)
    }
    await new Promise((resolve) => setTimeout(resolve, 50))
  }
  return { url, dir, stop }
}
