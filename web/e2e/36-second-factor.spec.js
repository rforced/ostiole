import { execFileSync } from 'node:child_process'
import { createHmac } from 'node:crypto'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { expect, test } from './fixtures.js'
import { PASSWORD, confirmDialog, login } from './helpers.js'

// After the password an account may be asked for a code from an
// authenticator app, or for a passkey. The codes are worked out here from the
// secret the dialog shows, as the app on a phone does. Every test leaves
// the admin without a second factor, for the next.

const headers = { 'X-Requested-With': 'ostiole' }

// The binary serve.sh runs, for the commands meant for a shell.
const BIN = resolve(
  fileURLToPath(new URL('..', import.meta.url)),
  process.env.OSTIOLE_BIN ?? '../bin/ostiole',
)

const BASE32 = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'

/** The code an authenticator app shows for a base32 secret at a 30 second step (RFC 6238). */
function code(secret, step) {
  const bits = [...secret].map((c) => BASE32.indexOf(c).toString(2).padStart(5, '0')).join('')
  const key = Buffer.from(bits.match(/.{8}/g).map((byte) => parseInt(byte, 2)))
  const counter = Buffer.alloc(8)
  counter.writeBigUInt64BE(BigInt(step))
  const mac = createHmac('sha1', key).update(counter).digest()
  const at = mac[mac.length - 1] & 0x0f
  return String((mac.readUInt32BE(at) & 0x7fffffff) % 1_000_000).padStart(6, '0')
}

const thisStep = () => Math.floor(Date.now() / 30_000)

/**
 * A code to sign in with after the app was set up. Setting it up spent the
 * code of its step, and the router allows a step of skew either way, so the
 * next step's code is good at once.
 */
const nextCode = (secret) => code(secret, thisStep() + 1)

const card = (page) => page.getByRole('region', { name: 'Second factor', exact: true })

/** Fills in the sign-in page; an account with a second factor is left at its code step. */
async function signIn(page, username) {
  await page.goto('/login')
  await page.getByLabel('Username').fill(username)
  await page.getByLabel('Password', { exact: true }).fill(PASSWORD)
  await page.getByRole('button', { name: 'Sign in' }).click()
}

async function signOut(page) {
  await page.getByRole('button', { name: 'Sign out' }).click()
  await expect(page.getByLabel('Username')).toBeVisible()
}

async function enterCode(page, digits) {
  await page.getByLabel('Code from your authenticator app').fill(digits)
  await page.getByRole('button', { name: 'Sign in' }).click()
}

/** Sets up the authenticator app from the Accounts page, and returns its secret. */
async function setUpAuthenticator(page) {
  await page.goto('/system/accounts')
  await card(page).getByRole('button', { name: 'Set up authenticator app' }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByRole('img', { name: 'QR code for the authenticator app' })).toBeVisible()
  const field = dialog.getByLabel('Secret', { exact: true })
  await expect(field).toHaveValue(/^[A-Z2-7]{32}$/)
  const secret = await field.inputValue()
  await dialog.getByLabel('Code', { exact: true }).fill(code(secret, thisStep()))
  await dialog.getByRole('button', { name: 'Confirm' }).click()
  await expect(dialog).toHaveCount(0)
  await expect(card(page)).toContainText('On. Sign-in asks for a code from your authenticator app')
  return secret
}

/** Opens Turn off, types the password and presses it. Returns the dialog. */
async function turnOff(page, password) {
  await card(page).getByRole('button', { name: 'Turn off', exact: true }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Password', { exact: true }).fill(password)
  await dialog.getByRole('button', { name: 'Turn off', exact: true }).click()
  return dialog
}

/** Runs `ostiole second-factor` on the configuration directory the server uses. */
const secondFactor = (server, ...args) =>
  execFileSync(BIN, ['--config-dir', server.dir, 'second-factor', ...args], {
    encoding: 'utf8',
    stdio: 'pipe',
  })

test('sign-in asks for the code of an authenticator app, and takes each code once', async ({
  page,
  browser,
  baseURL,
}) => {
  await login(page)
  const secret = await setUpAuthenticator(page)
  // The address bar holds an IP address here, which no passkey can be made for.
  await expect(card(page).getByRole('button', { name: 'Add a passkey' })).toBeDisabled()
  await expect(card(page)).toContainText('Passkeys need a name in the address bar, not an address.')

  await signOut(page)
  await signIn(page, 'admin')
  await enterCode(page, '000000')
  await expect(page.getByRole('alert')).toContainText('Invalid code.')
  const next = nextCode(secret)
  await enterCode(page, next)
  await expect(page).toHaveURL(/\/$/)
  await expect(page.getByRole('heading', { name: 'Dashboard', level: 1 })).toBeVisible()

  // Another browser with the same code is turned away.
  const context = await browser.newContext({ baseURL })
  const other = await context.newPage()
  await signIn(other, 'admin')
  await enterCode(other, next)
  await expect(other.getByRole('alert')).toContainText('That code was used already.')
  await context.close()

  // Turning it off asks for the password, and a wrong one signs nobody out.
  await page.goto('/system/accounts')
  const dialog = await turnOff(page, 'not the password')
  await expect(dialog.getByRole('alert')).toContainText('Password is wrong.')
  await dialog.getByLabel('Password', { exact: true }).fill(PASSWORD)
  await dialog.getByRole('button', { name: 'Turn off', exact: true }).click()
  await expect(dialog).toHaveCount(0)
  await expect(card(page)).toContainText('Off. Sign-in asks for your password alone.')

  await signOut(page)
  await signIn(page, 'admin')
  await expect(page).toHaveURL(/\/$/)
})

test('a passkey made at a host name signs in without a code, and can be removed', async ({
  browser,
  baseURL,
}) => {
  // A passkey is made for a name, so this one is reached as localhost.
  const context = await browser.newContext({ baseURL: baseURL.replace('127.0.0.1', 'localhost') })
  const page = await context.newPage()
  const cdp = await context.newCDPSession(page)
  await cdp.send('WebAuthn.enable')
  await cdp.send('WebAuthn.addVirtualAuthenticator', {
    options: {
      protocol: 'ctap2',
      transport: 'internal',
      hasResidentKey: true,
      hasUserVerification: true,
      isUserVerified: true,
    },
  })

  await login(page)
  await setUpAuthenticator(page)
  await card(page).getByRole('button', { name: 'Add a passkey' }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name', { exact: true }).fill('laptop')
  await dialog.getByRole('button', { name: 'Add passkey' }).click()
  await expect(dialog).toHaveCount(0)
  const row = card(page).getByRole('row').filter({ hasText: 'laptop' })
  await expect(row).toContainText('localhost')
  await expect(row).toContainText('never')

  // The passkey takes the place of the code. Twice, as its counter must rise.
  for (let i = 0; i < 2; i++) {
    await signOut(page)
    await signIn(page, 'admin')
    await page.getByRole('button', { name: 'Use a passkey' }).click()
    await expect(page).toHaveURL(/\/$/)
  }

  // Opened by address, the same account is asked for the code alone.
  const byAddress = await browser.newContext({ baseURL })
  const there = await byAddress.newPage()
  await signIn(there, 'admin')
  await expect(there.getByLabel('Code from your authenticator app')).toBeVisible()
  await expect(there.getByRole('button', { name: 'Use a passkey' })).toHaveCount(0)
  await byAddress.close()

  await page.goto('/system/accounts')
  await expect(row).toBeVisible()
  await expect(row).not.toContainText('never')
  await row.getByRole('button', { name: 'Remove' }).click()
  await confirmDialog(page, { confirm: 'Remove' })
  await expect(card(page)).toContainText('No passkeys.')

  await turnOff(page, PASSWORD)
  await expect(card(page)).toContainText('Off. Sign-in asks for your password alone.')
  await context.close()
})

test('an admin clears the second factor of another account', async ({ page, browser, baseURL }) => {
  await login(page)
  const created = await page.request.post('/api/v1/users', {
    data: { username: 'viewer2', password: PASSWORD, role: 'viewer' },
    headers,
  })
  expect(created.ok(), await created.text()).toBe(true)

  // Any role may set an authenticator app up for itself.
  const context = await browser.newContext({ baseURL })
  const other = await context.newPage()
  await signIn(other, 'viewer2')
  await expect(other).toHaveURL(/\/$/)
  await setUpAuthenticator(other)

  await page.goto('/system/accounts')
  const accounts = page.getByRole('region', { name: 'Accounts', exact: true })
  const row = accounts.getByRole('row').filter({ hasText: 'viewer2' })
  await expect(row.getByText('2FA')).toBeVisible()
  await row.getByRole('button', { name: 'Clear second factor' }).click()
  await confirmDialog(page, { confirm: 'Clear second factor' })
  await expect(row.getByText('2FA')).toHaveCount(0)
  await expect(row.getByRole('button', { name: 'Clear second factor' })).toHaveCount(0)

  await signOut(other)
  await signIn(other, 'viewer2')
  await expect(other).toHaveURL(/\/$/)
  await context.close()
})

test('a bypass from the command line lets the password alone in until it ends', async ({
  page,
  server,
}) => {
  await login(page)
  await setUpAuthenticator(page)

  secondFactor(server, 'bypass', '--for', '1m')
  await signOut(page)
  await signIn(page, 'admin')
  await expect(page).toHaveURL(/\/$/)
  await page.goto('/system/accounts')
  await expect(card(page)).toContainText(/bypassed until .* from the command line\./)

  secondFactor(server, 'bypass', '--end')
  await signOut(page)
  await signIn(page, 'admin')
  await expect(page.getByLabel('Code from your authenticator app')).toBeVisible()

  // Reset is the way back in without a code, and leaves the admin as it was.
  secondFactor(server, 'reset', 'admin')
  await signIn(page, 'admin')
  await expect(page).toHaveURL(/\/$/)
})
