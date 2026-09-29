import { spawn } from 'node:child_process'
import { createInterface } from 'node:readline'
import { fileURLToPath } from 'node:url'

const SERVE = fileURLToPath(new URL('./serve.sh', import.meta.url))

const answers = (url) =>
  fetch(`${url}/api/v1/health`).then(
    (res) => res.ok,
    () => false,
  )

/**
 * Starts serve.sh on a port and waits until the server answers. A port
 * something already answers on is refused, so a server left over from an
 * earlier run is never the one tested.
 *
 * @param {{port: number, seed?: string, dir?: string}} opts seed is a
 *   directory to start from, dir one to use and keep
 * @returns {Promise<{url: string, stop: () => Promise<void>}>}
 */
export async function startServer({ port, seed = '', dir = '' }) {
  const url = `http://127.0.0.1:${port}`
  if (await answers(url)) throw new Error(`something already answers on ${url}`)
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
  return { url, stop }
}
