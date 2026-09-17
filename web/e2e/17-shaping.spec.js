import { expect, test } from '@playwright/test'

import { applyAndConfirm, confirmDialog, login, shot, sidebar } from './helpers.js'

test.describe.configure({ mode: 'serial' })

/** The interface the wizard put in the wan zone, whatever it is called here. */
async function wanInterface(select) {
  const option = select.locator('option', { hasText: '(wan)' }).first()
  await expect(option).toHaveCount(1)
  return option.getAttribute('value')
}

test('a line speed and a priority reach the kernel', async ({ page }) => {
  await login(page)
  await page.goto('/firewall/shaping')

  // Nothing is shaped yet, and the page says what that means rather than
  // showing an empty table.
  await expect(page.getByText('No interface has a speed.')).toBeVisible()

  await page.getByRole('button', { name: 'Set a speed' }).click()
  const dialog = page.getByRole('dialog')
  const select = dialog.getByLabel('Interface')
  const wan = await wanInterface(select)
  await select.selectOption(wan)
  // The hints change with where the interface faces.
  await expect(dialog.getByText('what the line really delivers')).toBeVisible()
  await dialog.getByLabel('Download', { exact: true }).fill('200')
  await dialog.getByLabel('Upload', { exact: true }).fill('20')
  await dialog.getByLabel('Link type').selectOption('pppoe-ptm')
  await page.screenshot({ path: shot('80-bandwidth-dialog'), fullPage: true })
  await dialog.getByRole('button', { name: 'Save to draft' }).click()

  const row = page.getByRole('row').filter({ hasText: wan })
  await expect(row).toContainText('200 Mbit/s')
  await expect(row).toContainText('20 Mbit/s')
  await expect(row).toContainText('VDSL with PPPoE')
  await page.screenshot({ path: shot('81-bandwidth'), fullPage: true })

  // A priority belongs to the rule that admits the traffic, so the tab
  // that lists them is empty until a rule sets one.
  await page.getByRole('tab', { name: 'Priorities' }).click()
  await expect(page.getByText('No rule sets a priority. All traffic is Normal.')).toBeVisible()

  // In-app, because the speed is only in the draft and a page load would
  // discard it.
  await sidebar(page, 'Rules')
  await page.getByRole('group', { name: 'Zone' }).getByRole('button', { name: 'lan' }).click()
  await page.getByRole('button', { name: 'Add rule' }).click()
  const rule = page.getByRole('dialog')
  await rule.getByLabel('Description').fill('Calls go first')
  await rule.getByLabel('Protocol').selectOption('udp')
  await rule.getByLabel('Priority').selectOption('realtime')
  await rule.getByRole('button', { name: 'Save to draft' }).click()
  await expect(page.getByRole('row').filter({ hasText: 'Calls go first' })).toContainText(
    'Realtime',
  )

  await applyAndConfirm(page)

  // The tier is set in the verdict of the rule that admits the flow, and
  // put back on every later packet of it from the connection.
  await page.goto('/system/ruleset')
  await page.getByRole('button', { name: 'Show confirmed ruleset' }).click()
  const ruleset = page.locator('pre')
  await expect(ruleset).toContainText('meta mark set meta mark & 0xf8ffffff | 0x04000000')
  await expect(ruleset).toContainText('ct mark & 0x07000000 != 0x0 meta mark set ct mark')

  // And the priorities tab now names the rule that did it.
  await page.goto('/firewall/shaping#priorities')
  const priority = page.getByRole('row').filter({ hasText: 'Calls go first' })
  await expect(priority).toContainText('Realtime')
  await expect(priority).toContainText('udp')
  await page.screenshot({ path: shot('82-priorities'), fullPage: true })
})

test('the live tab reports the queues', async ({ page }) => {
  await login(page)
  await page.goto('/firewall/shaping#live')

  const card = page.getByRole('region').filter({ hasText: 'Upload' }).first()
  await expect(card).toContainText('of 20 Mbit/s')
  // The stub reports the queue that hangs off the device, which on a line
  // facing the internet is the upload.
  await expect(card.getByRole('row').filter({ hasText: 'Bulk' })).toContainText('21 ms')
  await expect(card.getByRole('row').filter({ hasText: 'Realtime' })).toContainText('0.9 ms')
  await expect(card.getByRole('row').filter({ hasText: 'Bulk' })).toContainText('12')
  // The download hangs off a helper device the stub does not pretend to
  // have, so it honestly says nothing is installed.
  await expect(card).toContainText('of 200 Mbit/s')
  await page.screenshot({ path: shot('83-shaping-live'), fullPage: true })
})

test('removing the speed takes the priority field with it', async ({ page }) => {
  await login(page)
  await page.goto('/firewall/rules')
  await page.getByRole('group', { name: 'Zone' }).getByRole('button', { name: 'lan' }).click()
  await page.getByRole('button', { name: 'Add rule' }).click()
  let dialog = page.getByRole('dialog')
  const priority = dialog.getByLabel('Priority')
  await expect(priority).toBeVisible()
  // A dropped connection has no traffic to prioritise.
  await priority.selectOption('high')
  await dialog.getByLabel('Action').selectOption('drop')
  await expect(priority).toBeDisabled()
  await expect(priority).toHaveValue('')
  await dialog.getByRole('button', { name: 'Cancel' }).click()

  await page.goto('/firewall/shaping')
  await page
    .getByRole('row')
    .filter({ hasText: 'Mbit/s' })
    .getByRole('button', { name: 'Remove' })
    .click()
  await confirmDialog(page, { confirm: 'Remove' })
  await expect(page.getByText('No interface has a speed.')).toBeVisible()

  // With nothing shaped, offering a priority would be a choice with no
  // consequence, so the field goes away.
  await sidebar(page, 'Rules')
  await page.getByRole('button', { name: 'Add rule' }).click()
  dialog = page.getByRole('dialog')
  await expect(dialog.getByLabel('Priority')).toHaveCount(0)
  await dialog.getByRole('button', { name: 'Cancel' }).click()

  await page.getByRole('button', { name: 'Discard' }).click()
})
