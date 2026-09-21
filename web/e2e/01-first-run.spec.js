import { expect, test } from '@playwright/test'

const PASSWORD = 'correct horse battery'
const shot = (name) => `e2e/screenshots/${name}.png`

test.describe.configure({ mode: 'serial' })

test('a fresh router asks for an admin account, then runs the wizard', async ({ page }) => {
  await page.goto('/')
  await expect(page).toHaveURL(/\/setup$/)
  await page.screenshot({ path: shot('01-setup'), fullPage: true })

  await page.getByLabel('Username').fill('admin')
  await page.getByLabel('Password', { exact: true }).fill(PASSWORD)
  await page.getByLabel('Repeat password').fill(PASSWORD)
  await page.getByRole('button', { name: 'Create account and sign in' }).click()

  await expect(page).toHaveURL(/\/wizard$/)
  await expect(page.getByRole('heading', { name: 'Set up this firewall' })).toBeVisible()
  await page.screenshot({ path: shot('02-wizard'), fullPage: true })

  await page.getByLabel('Hostname').fill('edge')
  const lan = page.getByLabel('LAN interface')
  await lan.selectOption({ index: 1 })
  await page.getByLabel('LAN address').fill('192.168.50.1/24')
  await page.getByLabel('WAN interface', { exact: true }).selectOption({ index: 2 })
  await page.getByRole('checkbox', { name: /Allow management from the WAN/ }).check()
  await page.getByRole('button', { name: 'Preview' }).click()

  await expect(page.getByRole('heading', { name: 'What will be applied' })).toBeVisible()
  await expect(page.getByText('192.168.50.1/24')).toBeVisible()
  // Management from the WAN is the wan zone's anti-lockout now, ticked and
  // unticked on the zone like any other, rather than rules of its own.
  await expect(page.getByRole('listitem').filter({ hasText: /Zone wan/ })).toContainText(
    'anti-lockout on',
  )
  await expect(page.getByRole('listitem').filter({ hasText: 'Web UI from wan' })).toHaveCount(0)
  await expect(page.getByRole('listitem').filter({ hasText: 'SSH from wan' })).toHaveCount(0)
  await page.screenshot({ path: shot('03-wizard-preview'), fullPage: true })

  await page.getByRole('button', { name: /Apply with 90s confirmation/ }).click()
  const pending = page.getByRole('status')
  await expect(pending).toContainText('awaiting confirmation')
  await expect(pending).toContainText(/\d+s/)
  await page.screenshot({ path: shot('04-pending'), fullPage: true })

  await pending.getByRole('button', { name: 'Confirm' }).click()
  await expect(page).toHaveURL(/\/$/)
  await expect(page.getByRole('heading', { name: 'Dashboard', level: 1 })).toBeVisible()
  // The Router card is where the applied configuration shows up: the
  // hostname the wizard was given, and a ruleset the kernel has.
  const router = page.getByRole('region', { name: 'Router' })
  await expect(router).toContainText('edge')
  await expect(router.getByText('loaded', { exact: true })).toBeVisible()
  await page.screenshot({ path: shot('05-dashboard-light'), fullPage: true })
})

test('sign out, wrong password, sign in again, deep link redirect', async ({ page }) => {
  await page.goto('/login')
  await page.getByLabel('Username').fill('admin')
  await page.getByLabel('Password', { exact: true }).fill('definitely wrong!')
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByRole('alert')).toContainText('Invalid username or password')
  await page.screenshot({ path: shot('06-login-error'), fullPage: true })

  await page.getByLabel('Password', { exact: true }).fill(PASSWORD)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page).toHaveURL(/\/$/)

  await page.getByRole('button', { name: 'Sign out' }).click()
  await expect(page).toHaveURL(/\/login$/)

  await page.goto('/interfaces')
  await expect(page).toHaveURL(/\/login\?redirect=(%2F|\/)interfaces$/)
  await page.getByLabel('Username').fill('admin')
  await page.getByLabel('Password', { exact: true }).fill(PASSWORD)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page).toHaveURL(/\/interfaces$/)
})

test('a theme choice survives a reload, and System clears it', async ({ page }) => {
  await page.goto('/login')
  const html = page.locator('html')
  await page.getByRole('button', { name: 'Dark' }).click()
  await expect(html).toHaveClass(/dark/)
  await page.screenshot({ path: shot('07-login-dark'), fullPage: true })
  await page.reload()
  await expect(html).toHaveClass(/dark/)
  await page.getByRole('button', { name: 'Light' }).click()
  await expect(html).not.toHaveClass(/dark/)
  await page.getByRole('button', { name: 'System' }).click()
  expect(await page.evaluate(() => localStorage.getItem('ostiole.theme'))).toBeNull()
})
