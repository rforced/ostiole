import { execFileSync } from 'node:child_process'
import { createHmac } from 'node:crypto'
import { mkdirSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { expect, test } from './fixtures'
import { PASSWORD, confirmDialog, enterUsername, login, signOut } from './helpers'

// After the password an account may be asked for a code from an
// authenticator app, or for a passkey. The codes are worked out here from the
// secret the dialog shows, as the app on a phone does. Every test leaves
// the admin without MFA, for the next.

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

const card = (page) =>
  page.getByRole('region', { name: 'Multi-factor authentication', exact: true })

const OFF = 'Sign-in asks for your password alone.'
const ON = 'Sign-in asks for a code from your authenticator app after your password.'
const KEYS =
  'Sign-in takes a passkey on its own, or your password and then a code from your authenticator app or a passkey.'

// Pictures of the account page for a person to look over.
const SHOTS = '/var/tmp/account-plan/shots'
const review = (name) => {
  mkdirSync(SHOTS, { recursive: true })
  return `${SHOTS}/${name}.png`
}

/** Fills in the sign-in page; an account with MFA is left at its code step. */
async function signIn(page, username) {
  await page.goto('/login')
  await enterUsername(page, username)
  await page.getByLabel('Password', { exact: true }).fill(PASSWORD)
  await page.getByRole('button', { name: 'Sign in' }).click()
}

async function enterCode(page, digits) {
  await page.getByLabel('Code from your authenticator app').fill(digits)
  await page.getByRole('button', { name: 'Sign in' }).click()
}

/** Sets up the authenticator app from the Account page, and returns its secret. */
async function setUpAuthenticator(page) {
  await page.goto('/account')
  await card(page).getByRole('button', { name: 'Set up authenticator app' }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByRole('img', { name: 'QR code for the authenticator app' })).toBeVisible()
  const field = dialog.getByLabel('Secret', { exact: true })
  await expect(field).toHaveValue(/^[A-Z2-7]{32}$/)
  const secret = await field.inputValue()
  await dialog.getByLabel('Code', { exact: true }).fill(code(secret, thisStep()))
  await dialog.getByRole('button', { name: 'Confirm' }).click()
  await expect(dialog).toHaveCount(0)
  await expect(card(page)).toContainText(ON)
  return secret
}

/** Opens Remove for the authenticator app and types the password. Returns the dialog. */
async function openRemoveApp(page, password) {
  const app = card(page).getByRole('group', { name: 'Authenticator app' })
  await app.getByRole('button', { name: 'Remove', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: 'Remove the authenticator app' })
  await dialog.getByLabel('Password', { exact: true }).fill(password)
  return dialog
}

/** Removes the authenticator app, and with it every passkey. */
async function removeApp(page) {
  const dialog = await openRemoveApp(page, PASSWORD)
  await dialog.getByRole('button', { name: 'Remove', exact: true }).click()
  await expect(dialog).toHaveCount(0)
  await expect(card(page)).toContainText(OFF)
  await expect(card(page)).toContainText('Not set up.')
}

/** Runs `ostiole mfa` on the configuration directory the server uses. */
const mfa = (server, ...args) =>
  execFileSync(BIN, ['--config-dir', server.dir, 'mfa', ...args], {
    encoding: 'utf8',
    stdio: 'pipe',
  })

test('sign-in asks for the code of an authenticator app, and takes each code once', async ({
  page,
  browser,
  baseURL,
}) => {
  await login(page)
  await page.goto('/account')
  await expect(card(page)).toContainText(OFF)
  await expect(card(page).getByRole('button', { name: 'Add passkey' })).toBeDisabled()
  await expect(card(page).locator('#passkey-block')).toHaveText(
    'Set up an authenticator app first.',
  )

  const secret = await setUpAuthenticator(page)
  // The address bar holds an IP address here, which no passkey can be made for.
  await expect(card(page).getByRole('button', { name: 'Add passkey' })).toBeDisabled()
  await expect(card(page).locator('#passkey-block')).toHaveText(
    'Open the UI by its host name to add a passkey.',
  )

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

  // Removing it asks for the password, and a wrong one changes nothing.
  await page.goto('/account')
  await expect(card(page)).toContainText(ON)
  const dialog = await openRemoveApp(page, 'not the password')
  await dialog.getByRole('button', { name: 'Remove', exact: true }).click()
  await expect(dialog.getByRole('alert')).toContainText('Password is wrong.')
  await dialog.getByLabel('Password', { exact: true }).fill(PASSWORD)
  await dialog.getByRole('button', { name: 'Remove', exact: true }).click()
  await expect(dialog).toHaveCount(0)
  await expect(card(page)).toContainText(OFF)

  await signOut(page)
  await signIn(page, 'admin')
  await expect(page).toHaveURL(/\/$/)
})

test('a passkey made at a host name signs in on its own, or after the password', async ({
  browser,
  baseURL,
}) => {
  // A passkey is made for a name, so this one is reached as localhost.
  const localhost = baseURL.replace('127.0.0.1', 'localhost')
  const context = await browser.newContext({
    baseURL: localhost,
    viewport: { width: 1440, height: 900 },
  })
  const page = await context.newPage()
  const cdp = await context.newCDPSession(page)
  await cdp.send('WebAuthn.enable')
  const { authenticatorId } = await cdp.send('WebAuthn.addVirtualAuthenticator', {
    options: {
      protocol: 'ctap2',
      transport: 'internal',
      hasResidentKey: true,
      hasUserVerification: true,
      isUserVerified: true,
    },
  })
  const credentials = async () =>
    (await cdp.send('WebAuthn.getCredentials', { authenticatorId })).credentials

  await login(page)
  await setUpAuthenticator(page)
  await expect(card(page)).toContainText('No passkeys.')

  // The password is checked before the browser is asked for a passkey.
  await card(page).getByRole('button', { name: 'Add passkey' }).click()
  const dialog = page.getByRole('dialog', { name: 'Add passkey' })
  await expect(dialog).toContainText('Made for localhost. It signs in only there.')
  await dialog.getByLabel('Name', { exact: true }).fill('laptop')
  await dialog.getByLabel('Password', { exact: true }).fill('not the password')
  await dialog.getByRole('button', { name: 'Add passkey' }).click()
  await expect(dialog.getByRole('alert')).toContainText('Password is wrong.')
  expect(await credentials()).toHaveLength(0)
  await dialog.getByLabel('Password', { exact: true }).fill(PASSWORD)
  await dialog.getByRole('button', { name: 'Add passkey' }).click()
  await expect(dialog).toHaveCount(0)
  // The device keeps it, so it can be offered before a name is typed.
  const made = await credentials()
  expect(made).toHaveLength(1)
  expect(made[0].isResidentCredential).toBe(true)
  const row = card(page).getByRole('row').filter({ hasText: 'laptop' })
  await expect(row).toContainText('localhost')
  await expect(row).toContainText('never')
  await expect(card(page)).toContainText(KEYS)

  await row.getByRole('button', { name: 'Rename' }).click()
  const rename = page.getByRole('dialog', { name: 'Rename laptop' })
  await rename.getByLabel('Name', { exact: true }).fill('desk laptop')
  await rename.getByRole('button', { name: 'Rename' }).click()
  await expect(rename).toHaveCount(0)
  const renamed = card(page).getByRole('row').filter({ hasText: 'desk laptop' })
  await expect(renamed).toContainText('localhost')
  await page.screenshot({ path: review('account-admin'), fullPage: true })

  // The first page signs in with the passkey alone, nothing typed.
  await signOut(page)
  const alone = page.getByRole('button', { name: 'Sign in with a passkey' })
  await expect(alone).toBeVisible()
  await page.screenshot({ path: review('login-step1'), fullPage: true })
  await alone.click()
  await expect(page).toHaveURL(/\/$/)
  await expect(page.getByRole('heading', { name: 'Dashboard', level: 1 })).toBeVisible()

  // So does the password step, with the passkey in place of the password.
  await signOut(page)
  await enterUsername(page, 'admin')
  await page.getByRole('button', { name: 'Use a passkey' }).click()
  await expect(page).toHaveURL(/\/$/)

  // After the password the passkey comes first, before the code.
  await signOut(page)
  await signIn(page, 'admin')
  const usePasskey = page.getByRole('button', { name: 'Use a passkey' })
  await expect(usePasskey).toBeFocused()
  await expect(page.getByLabel('Code from your authenticator app')).toBeVisible()
  await page.screenshot({ path: review('login-step3'), fullPage: true })
  await usePasskey.click()
  await expect(page).toHaveURL(/\/$/)

  // Opened by address, the same account is offered no passkey at all.
  const byAddress = await browser.newContext({ baseURL })
  const there = await byAddress.newPage()
  const asked = there.waitForResponse(
    (r) => r.url().endsWith('/api/v1/auth/login') && r.request().method() === 'GET',
  )
  await there.goto('/login')
  await asked
  await expect(there.getByRole('button', { name: 'Sign in with a passkey' })).toHaveCount(0)
  await enterUsername(there, 'admin')
  await expect(there.getByRole('button', { name: 'Use a passkey' })).toHaveCount(0)
  await there.getByLabel('Password', { exact: true }).fill(PASSWORD)
  await there.getByRole('button', { name: 'Sign in' }).click()
  await expect(there.getByLabel('Code from your authenticator app')).toBeVisible()
  await expect(there.getByRole('button', { name: 'Use a passkey' })).toHaveCount(0)
  await byAddress.close()

  await page.goto('/account')
  await expect(renamed).not.toContainText('never')

  // A device that cannot check who holds it fails in the browser, which
  // sends the router nothing and says nothing.
  const sent = []
  page.on('request', (r) => {
    if (r.url().endsWith('/api/v1/auth/login/passkey')) sent.push(r.url())
  })
  await cdp.send('WebAuthn.setUserVerified', { authenticatorId, isUserVerified: false })
  await signOut(page)
  const started = page.waitForResponse('**/api/v1/auth/login/start')
  await alone.click()
  await started
  await expect(alone).toBeEnabled()
  await expect(page.getByRole('alert')).toHaveCount(0)
  await expect(page).toHaveURL(/\/login$/)
  expect(sent).toEqual([])
  await cdp.send('WebAuthn.setUserVerified', { authenticatorId, isUserVerified: true })
  await alone.click()
  await expect(page).toHaveURL(/\/$/)

  // Removing the app lists the passkeys that go with it.
  await page.goto('/account')
  const remove = await openRemoveApp(page, '')
  await expect(remove).toContainText('Passkeys removed')
  await expect(remove.getByRole('listitem')).toHaveText(['desk laptop'])
  await page.waitForFunction(() => document.getAnimations().every((a) => a.playState !== 'running'))
  await page.screenshot({ path: review('remove-app'), fullPage: true })
  await remove.getByRole('button', { name: 'Cancel' }).click()
  await expect(remove).toHaveCount(0)

  await renamed.getByRole('button', { name: 'Remove' }).click()
  await confirmDialog(page, { confirm: 'Remove' })
  await expect(card(page)).toContainText('No passkeys.')
  await expect(card(page)).toContainText(ON)

  await removeApp(page)
  await context.close()
})

test('an admin clears MFA for another account', async ({ page, browser, baseURL }) => {
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
  await expect(row.getByRole('cell', { name: 'App', exact: true })).toBeVisible()
  await row.getByRole('button', { name: 'Clear MFA' }).click()
  await confirmDialog(page, { confirm: 'Clear MFA' })
  await expect(row.getByRole('cell', { name: 'Off', exact: true })).toBeVisible()
  await expect(row.getByRole('button', { name: 'Clear MFA' })).toHaveCount(0)

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

  mfa(server, 'bypass', '--for', '1m')
  await signOut(page)
  await signIn(page, 'admin')
  await expect(page).toHaveURL(/\/$/)
  await expect(page.getByText(/^MFA is bypassed until .* from the command line\.$/)).toBeVisible()

  mfa(server, 'bypass', '--end')
  await page.goto('/account')
  await signOut(page)
  await signIn(page, 'admin')
  await expect(page.getByLabel('Code from your authenticator app')).toBeVisible()

  // Reset is the way back in without a code, and leaves the admin as it was.
  mfa(server, 'reset', 'admin')
  await signIn(page, 'admin')
  await expect(page).toHaveURL(/\/$/)
})
