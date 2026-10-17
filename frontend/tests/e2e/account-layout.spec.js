import { expect, test } from '@playwright/test'

// Runs in the existing disposable Actions E2E environment. These UI scenarios
// stub account APIs so they neither consume real tokens nor change the server
// bootstrap/session state used by server.spec.js.
test.skip(!process.env.E2E_BASE_URL, 'Run via tests/e2e/run-server-e2e.sh')

const password = 'Account-layout-test-2026!'
const oldRecovery = Buffer.alloc(32, 7)
const oldCode = oldRecovery.toString('base64url')
const viewports = [
  { name: 'desktop', width: 1440, height: 900, compact: false },
  { name: 'tablet', width: 820, height: 1000, compact: true },
  { name: 'phone', width: 390, height: 844, compact: true },
]

async function mockAccountAPIs(page, mode) {
  const submissions = []
  await page.route('**/api/auth/status', route => route.fulfill({
    status: 200, contentType: 'application/json',
    body: JSON.stringify({ authenticated: false, setup_required: false }),
  }))
  const endpoint = mode === 'invite' ? 'invitations/accept' : 'password-reset/complete'
  await page.route('**/api/auth/' + endpoint, route => {
    submissions.push(route.request().postDataJSON())
    return route.fulfill({
      status: 200, contentType: 'application/json',
      body: JSON.stringify(mode === 'invite'
        ? { user: { id: 'layout-user', email: 'layout@example.invalid' } }
        : { password_reset: { id: 'layout-reset' } }),
    })
  })
  return submissions
}

async function fillCredentials(page, mode) {
  await page.locator('#operation-token').fill('layout-test-token')
  if (mode === 'invite') await page.locator('#display-name').fill('表示テスト')
  else await page.locator('#current-recovery').fill(oldCode)
  await page.locator('#password').fill(password)
  await page.locator('#password-confirmation').fill(password)
}

async function assertFitsHorizontally(page) {
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1)).toBe(true)
}

for (const mode of ['invite', 'reset']) {
  for (const viewport of viewports) {
    test(mode + ' layout and final submission on ' + viewport.name, async ({ page }) => {
      const submissions = await mockAccountAPIs(page, mode)
      await page.setViewportSize({ width: viewport.width, height: viewport.height })
      await page.goto('/login?mode=' + mode)
      const credentials = page.locator('.credentials-panel')
      const recovery = page.locator('.recovery-panel')
      const submit = page.locator('.login-button')
      await expect(credentials).toBeVisible()
      await expect(page.locator('.recovery-code')).toHaveValue(/^[A-Za-z0-9_-]{43}$/)
      const code = await page.locator('.recovery-code').inputValue()
      await assertFitsHorizontally(page)
      await fillCredentials(page, mode)

      if (viewport.compact) {
        await expect(recovery).toBeHidden()
        await expect(submit).toHaveText('次へ')
        // A real native form submit must not be blocked by the hidden,
        // unchecked required checkbox in the next stage.
        await submit.click()
        await expect(recovery).toBeVisible()
        await expect(credentials).toBeHidden()
        expect(submissions).toHaveLength(0)
        await expect(submit).toBeDisabled()
        await page.locator('.confirmation-label input').check()
        await page.getByRole('button', { name: '戻る', exact: true }).click()
        await expect(page.locator('#password')).toHaveValue(password)
        await expect(page.locator('#operation-token')).toHaveValue('layout-test-token')
        // Switching between the two layouts must retain the same generated
        // code, entered credentials, and save acknowledgement.
        await page.setViewportSize({ width: 1280, height: 900 })
        await expect(recovery).toBeVisible()
        await expect(credentials).toBeVisible()
        await expect(page.locator('.recovery-code')).toHaveValue(code)
        await page.setViewportSize({ width: viewport.width, height: viewport.height })
        await expect(recovery).toBeHidden()
        await submit.click()
        await expect(recovery).toBeVisible()
        await expect(page.locator('.confirmation-label input')).toBeChecked()
        expect(submissions).toHaveLength(0)
      } else {
        await expect(recovery).toBeVisible()
        const left = await credentials.boundingBox()
        const right = await recovery.boundingBox()
        expect(right.x).toBeGreaterThan(left.x + left.width)
        expect(Math.abs(left.y - right.y)).toBeLessThan(2)
        const card = await page.locator('.login-card').boundingBox()
        expect(card.width).toBeGreaterThan(700)
        await expect(submit).toBeDisabled()
        await page.locator('.confirmation-label input').check()
      }

      if (viewport.name === 'phone') {
        // A short viewport (such as with an on-screen keyboard) has one
        // document scrollbar; the final action must remain reachable.
        await page.setViewportSize({ width: viewport.width, height: 430 })
        await expect(recovery).toBeVisible()
        expect(await page.locator('#app').evaluate(el => getComputedStyle(el).overflowY)).toBe('visible')
        expect(await page.locator('.login-root').evaluate(el => getComputedStyle(el).overflowY)).toBe('visible')
        await submit.scrollIntoViewIfNeeded()
        await expect(submit).toBeInViewport()
      }
      await assertFitsHorizontally(page)
      await submit.click()
      await expect(page.getByRole('status')).toContainText('ログインしてください')
      expect(submissions).toHaveLength(1)
      const encodedPassword = Buffer.from(password).toString('base64')
      const newSecret = Buffer.from(code, 'base64url').toString('base64')
      expect(submissions[0]).toEqual(mode === 'invite'
        ? {
            token_b64: Buffer.from('layout-test-token').toString('base64'),
            display_name: '表示テスト', password_b64: encodedPassword,
            recovery_secret_b64: newSecret,
          }
        : {
            token_b64: Buffer.from('layout-test-token').toString('base64'),
            recovery_secret_b64: oldRecovery.toString('base64'),
            new_password_b64: encodedPassword, new_recovery_secret_b64: newSecret,
          })
      await expect(page.locator('.account-flow')).toHaveCount(0)
      await expect(page.locator('#password')).toHaveValue('')
      await expect(page.getByRole('button', { name: 'パスキーでログイン', exact: true })).toBeVisible()
      expect(new URL(page.url()).search).toBe('')
    })
  }
}
