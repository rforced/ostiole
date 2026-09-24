import { createServer } from 'node:net'

import { expect, test } from '@playwright/test'

import { applyAndConfirm, login, shot } from './helpers.js'

test.describe.configure({ mode: 'serial' })

// A mail server on this machine that keeps what it is sent, so the test
// mail is read here and nothing leaves for the internet.
let smtp
const mails = []

/** Speaks as much SMTP as a client needs to hand over a message. */
function session(socket) {
  socket.setEncoding('utf8')
  socket.on('error', () => {})
  const reply = (line) => socket.write(`${line}\r\n`)
  let mail = { from: '', to: [] }
  let data = null
  let pending = ''
  reply('220 mail.example.test ESMTP')
  socket.on('data', (chunk) => {
    pending += chunk
    for (let end = pending.indexOf('\r\n'); end !== -1; end = pending.indexOf('\r\n')) {
      const line = pending.slice(0, end)
      pending = pending.slice(end + 2)
      if (data) {
        if (line !== '.') {
          data.push(line.startsWith('.') ? line.slice(1) : line)
          continue
        }
        mails.push({ ...mail, text: data.join('\n') })
        mail = { from: '', to: [] }
        data = null
        reply('250 queued')
        continue
      }
      switch (line.slice(0, 4).toUpperCase()) {
        case 'EHLO':
        case 'HELO':
          reply('250 mail.example.test')
          break
        case 'MAIL':
          mail.from = line
          reply('250 ok')
          break
        case 'RCPT':
          mail.to.push(line)
          reply('250 ok')
          break
        case 'DATA':
          data = []
          reply('354 end with a dot')
          break
        case 'QUIT':
          reply('221 bye')
          socket.end()
          break
        default:
          reply('250 ok')
      }
    }
  })
}

/** A port nothing listens on: taken from the kernel and let go. */
async function closedPort() {
  const s = createServer()
  await new Promise((resolve) => s.listen(0, '127.0.0.1', resolve))
  const { port } = s.address()
  await new Promise((resolve) => s.close(resolve))
  return port
}

test.beforeAll(async () => {
  smtp = createServer(session)
  await new Promise((resolve) => smtp.listen(0, '127.0.0.1', resolve))
})

test.afterAll(() => smtp?.close())

// A test sends the draft, so settings are tried before they are applied.
// The webhook posts to a port that refuses; its error must not repeat the
// URL, which for a chat service is the key to the channel.
test('a test mail arrives before anything is applied', async ({ page }) => {
  await login(page)
  await page.goto('/system/notifications')
  const send = page.getByRole('button', { name: 'Send a test' })
  await expect(send).toBeDisabled()

  const server = `127.0.0.1:${smtp.address().port}`
  await page.getByLabel('Send mail').check()
  await page.getByLabel('Mail server').fill(server)
  await page.getByLabel('Security', { exact: true }).selectOption('none')
  await page.getByLabel('From', { exact: true }).fill('Router <router@example.net>')
  await page.getByLabel('To', { exact: true }).fill('ops@example.net, noc@example.net')

  await page.getByLabel('Post to a webhook').check()
  const url = page.getByLabel('URL', { exact: true })
  await url.fill(`https://127.0.0.1:${await closedPort()}/hooks/T0PSECRET`)
  await url.blur()

  await send.click()
  await expect(page.getByText('Mail: sent. Check that it arrived.')).toBeVisible()
  const hook = page.getByText(/^Webhook: /)
  await expect(hook).toContainText('refused')
  await expect(hook).not.toContainText('T0PSECRET')
  // A test is not a notice, so nothing is listed as sent.
  await expect(page.getByText('Nothing sent.')).toBeVisible()
  await page.screenshot({ path: shot('130-notifications'), fullPage: true })

  await expect.poll(() => mails.length).toBe(1)
  const [mail] = mails
  const { system } = await (await page.request.get('/api/v1/config')).json()
  const name = system.hostname
  expect(mail.from).toBe('MAIL FROM:<router@example.net>')
  expect(mail.to).toEqual(['RCPT TO:<ops@example.net>', 'RCPT TO:<noc@example.net>'])
  expect(mail.text).toContain(`Subject: ${name}: Notifications from ${name} arrive here`)
  expect(mail.text).toContain('Auto-Submitted: auto-generated')

  // Switched on with mail alone, it applies and comes back as it was left.
  await page.getByLabel('Post to a webhook').uncheck()
  await page.getByLabel('Notifications enabled').check()
  await applyAndConfirm(page)
  const cfg = await (await page.request.get('/api/v1/config')).json()
  expect(cfg.notifications).toMatchObject({
    enabled: true,
    email: {
      enabled: true,
      server,
      security: 'none',
      from: 'Router <router@example.net>',
      to: ['ops@example.net', 'noc@example.net'],
    },
  })
  expect(cfg.notifications.webhook?.enabled).toBeUndefined()

  await page.reload()
  await expect(page.getByLabel('Notifications enabled')).toBeChecked()
  await expect(page.getByLabel('Mail server')).toHaveValue(server)
  await expect(page.getByLabel('To', { exact: true })).toHaveValue(
    'ops@example.net, noc@example.net',
  )

  // Put it back: off is the default, and the rest of the suite should see
  // the router it expects.
  await page.getByLabel('Notifications enabled').uncheck()
  await applyAndConfirm(page)
})
