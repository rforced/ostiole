import { readFile, writeFile } from 'node:fs/promises'
import { join } from 'node:path'

import { expect, test } from './fixtures.js'
import { applyAndConfirm, login } from './helpers.js'

// After an update the router runs what the last apply rendered until the
// next one. A saved ruleset one line longer stands in for an earlier
// release's: the bar offers what this version would change, lists it, and
// its apply brings it in.
test('the bar offers what this version would apply differently', async ({ page, server }) => {
  const path = join(server.dir, 'ruleset.nft')
  const saved = await readFile(path, 'utf8')
  // After the note naming the configuration the ruleset belongs to.
  const at = saved.indexOf('\n') + 1
  await writeFile(path, `${saved.slice(0, at)}# an older release's line\n${saved.slice(at)}`)

  await login(page)
  const bar = page.getByText('Not applied with this version: Firewall.')
  await expect(bar).toBeVisible()
  await expect(page.getByRole('button', { name: 'Discard' })).toHaveCount(0)
  await page.getByRole('button', { name: 'Show 1 change' }).click()
  await expect(page.getByText("firewall: # an older release's line")).toBeVisible()

  await applyAndConfirm(page)
  await expect(bar).toHaveCount(0)
  expect(await readFile(path, 'utf8')).not.toContain("an older release's line")
})
