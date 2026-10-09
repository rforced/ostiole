import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { request } from '@playwright/test'

import { PASSWORD } from './helpers.js'
import { startServer } from './server.js'

/**
 * Makes the router every file but 01-first-run.spec.js starts from, with
 * the requests the wizard makes there: an admin, then edge with the first
 * wired link as LAN and the second as WAN. The fixtures copy its directory
 * for each file; it goes when the run ends. The WAN's gateway answers at
 * a made-up next hop, through the probes file serve.sh hands the server.
 */
export default async function globalSetup() {
  const dir = mkdtempSync(join(tmpdir(), 'ostiole-e2e-seed-'))
  const remove = () => rmSync(dir, { recursive: true, force: true })
  try {
    const server = await startServer({ port: 18099, dir })
    try {
      const wan = await configure(server.url)
      writeFileSync(
        join(dir, 'probes.json'),
        JSON.stringify({ hops: { [wan]: { ipv4: '192.0.2.1' } } }),
      )
    } finally {
      await server.stop()
    }
  } catch (e) {
    remove()
    throw e
  }
  process.env.E2E_SEED = dir
  return remove
}

async function configure(baseURL) {
  const api = await request.newContext({
    baseURL,
    extraHTTPHeaders: { 'X-Requested-With': 'ostiole' },
  })
  const call = async (method, path, data) => {
    const res = await api.fetch(`/api/v1${path}`, { method, data })
    if (!res.ok()) throw new Error(`${method} ${path}: ${res.status()} ${await res.text()}`)
    const body = await res.text()
    return body ? JSON.parse(body) : null
  }
  await call('POST', '/setup', { username: 'admin', password: PASSWORD })
  const wired = (await call('GET', '/interfaces/live')).filter(
    (l) => l.kind !== 'loopback' && !l.wireless,
  )
  if (wired.length < 2) throw new Error('the wizard needs two wired links, a LAN and a WAN')
  const config = await call('POST', '/config/starter', {
    hostname: 'edge',
    lan: wired[0].name,
    lanAddress: '192.168.50.1/24',
    wan: wired[1].name,
    managementFromWan: true,
    services: true,
  })
  await call('POST', '/apply', { config, confirmTimeoutSeconds: 90, baseRevision: 'none' })
  await call('POST', '/apply/confirm')
  await api.dispose()
  return wired[1].name
}
