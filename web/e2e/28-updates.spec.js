import { createHash, randomBytes } from 'node:crypto'
import { createServer } from 'node:http'

import { expect, test } from './fixtures.js'
import { login, shot } from './helpers.js'

test.describe.configure({ mode: 'serial' })

// A stand-in for GitHub's releases API, where serve.sh points the updater.
// It offers a release whose checksums carry a signature from nobody.
const PORT = 18096
const BASE = `http://127.0.0.1:${PORT}`
const ARCH = { x64: 'amd64', arm64: 'arm64' }[process.arch] ?? process.arch
const TARBALL = `ostiole_99.0.0_linux_${ARCH}.tar.gz`
let api
const asked = []

test.beforeAll(async () => {
  const tarball = randomBytes(64)
  const files = {
    [`/download/${TARBALL}`]: tarball,
    '/download/checksums.txt': Buffer.from(
      `${createHash('sha256').update(tarball).digest('hex')}  ${TARBALL}\n`,
    ),
    '/download/checksums.txt.sig': Buffer.from(randomBytes(64).toString('base64')),
  }
  const release = {
    tag_name: 'v99.0.0',
    draft: false,
    prerelease: false,
    published_at: '2026-09-26T12:00:00Z',
    body: 'A release nobody signed.',
    html_url: `${BASE}/release`,
    assets: Object.entries(files).map(([path, body]) => ({
      name: path.split('/').pop(),
      browser_download_url: BASE + path,
      size: body.length,
    })),
  }
  api = createServer((req, res) => {
    const path = new URL(req.url, BASE).pathname
    asked.push(path)
    if (path === '/repos/rforced/ostiole/releases') {
      res.writeHead(200, { 'Content-Type': 'application/json' })
      res.end(JSON.stringify([release]))
      return
    }
    const body = files[path]
    res.writeHead(body ? 200 : 404, { 'Content-Type': 'application/octet-stream' })
    res.end(body)
  })
  await new Promise((resolve) => api.listen(PORT, '127.0.0.1', resolve))
})

test.afterAll(() => api?.close())

// Check asks for the releases and shows the newest. Installing it verifies
// the checksums before anything else, so a release the embedded key did not
// sign is refused before its binary is fetched, and the router keeps running
// the version it had.
test('a release the key did not sign is refused before its binary is fetched', async ({ page }) => {
  await login(page)
  await page.goto('/system/updates')
  const updates = page.getByRole('region', { name: 'Ostiole updates' })
  // The field says … until the status arrives; read it after.
  const field = updates.locator('dt:text-is("Installed") + dd')
  await expect(field).not.toHaveText('…')
  const installed = await field.textContent()

  await updates.getByRole('button', { name: 'Check now' }).click()
  await expect(updates).toContainText('v99.0.0')
  await expect(updates).toContainText('A release nobody signed.')
  expect(asked).toContain('/repos/rforced/ostiole/releases')

  await updates.getByRole('button', { name: 'Install 99.0.0' }).click()
  const dialog = page.getByRole('dialog', { name: 'Install 99.0.0?' })
  await dialog.getByRole('button', { name: 'Install', exact: true }).click()
  await expect(updates).toContainText(
    'Update failed: checksums.txt signature does not verify; refusing the update',
    { timeout: 15_000 },
  )
  await page.screenshot({ path: shot('95-update-refused'), fullPage: true })
  expect(asked).toContain('/download/checksums.txt.sig')
  expect(asked).not.toContain(`/download/${TARBALL}`)
  await expect(field).toHaveText(installed)
})
