import { expect, test } from '@playwright/test'

// Real Chromium WebAuthn + application UI + hardened server. No credential,
// fetch, or API response is mocked. The authenticator is scoped to this page.
export async function verifyPasskeyRoundTrip(page) {
  const cdp = await page.context().newCDPSession(page)
  await cdp.send('WebAuthn.enable')
  const { authenticatorId } = await cdp.send('WebAuthn.addVirtualAuthenticator', {
    options: {
      protocol: 'ctap2', transport: 'internal',
      hasResidentKey: true, hasUserVerification: true,
      isUserVerified: true, automaticPresenceSimulation: true,
      hasPrf: false, defaultBackupEligibility: false, defaultBackupState: false,
    },
  })
  try {
    await test.step('Register a resident passkey without PRF through the application', async () => {
      await page.locator('.hamburger-menu').click()
      await page.locator('#side-menu').getByRole('button', { name: 'パスキー設定', exact: true }).click()
      const modal = page.getByRole('dialog', { name: 'パスキー設定', exact: true })
      await expect(modal).toBeVisible()
      await modal.getByRole('textbox', { name: 'パスキー名', exact: true }).fill('CI Chromium passkey')
      const finishPromise = page.waitForResponse(response => response.url().endsWith('/api/auth/passkeys/register/finish'))
      await modal.getByRole('button', { name: 'パスキーを登録', exact: true }).click()
      const finish = await finishPromise
      expect(finish.ok()).toBe(true)
      const { passkey } = await finish.json()
      expect(passkey.password_required).toBe(false)
      await expect(modal.getByRole('status')).toContainText('パスキーを登録しました')
      await expect(modal.getByText('CI Chromium passkey', { exact: true })).toBeVisible()
      const stored = await cdp.send('WebAuthn.getCredentials', { authenticatorId })
      expect(stored.credentials.length).toBe(1)
      expect(stored.credentials[0].isResidentCredential).toBe(true)
      await modal.getByRole('button', { name: '閉じる', exact: true }).click()
    })

    await test.step('Log out and discover the saved passkey without an email or password', async () => {
      await page.locator('.hamburger-menu').click()
      await page.locator('#side-menu').getByRole('button', { name: 'ログアウト', exact: true }).click()
      await page.waitForURL(/\/login/)
      await expect(page.locator('#email')).toHaveValue('')
      await expect(page.locator('#password')).toHaveValue('')
      const finishPromise = page.waitForResponse(response => response.url().endsWith('/api/auth/passkeys/discover/finish'))
      await page.getByRole('button', { name: 'パスキーでログイン', exact: true }).click()
      const finish = await finishPromise
      expect(finish.ok()).toBe(true)
      expect(finish.request().postDataJSON().password_b64).toBeUndefined()
      expect(finish.request().postDataJSON().prf_result_b64).toBe('')
      await page.waitForURL('/')
      await expect(page.locator('.hamburger-menu')).toBeVisible()
    })

    await test.step('Complete passkey reauthentication, rotate CSRF, and reject replay', async () => {
      const before = await page.evaluate(async () => (await fetch('/api/auth/status')).json())
      expect(before.authenticated).toBe(true)
      await page.evaluate(() => {
        window.__passkeyReauthResolved = false
        window.dispatchEvent(new CustomEvent('omni-money:reauth-required', {
          detail: {
            resolve: () => { window.__passkeyReauthResolved = true },
            reject: () => { window.__passkeyReauthResolved = false },
          },
        }))
      })
      const modal = page.getByRole('dialog', { name: '重要な操作の確認', exact: true })
      await expect(modal).toBeVisible()
      await expect(modal.locator('#reauth-password')).toHaveValue('')
      const finishPromise = page.waitForResponse(response => response.url().endsWith('/api/auth/passkeys/reauth/finish'))
      await modal.getByRole('button', { name: 'パスキーで確認', exact: true }).click()
      const finish = await finishPromise
      expect(finish.ok()).toBe(true)
      const after = await finish.json()
      expect(after.authenticated).toBe(true)
      expect(typeof after.csrf_token).toBe('string')
      expect(after.csrf_token.length).toBeGreaterThan(0)
      expect(after.csrf_token !== before.csrf_token).toBe(true)
      await expect(modal).toBeHidden()
      await expect.poll(() => page.evaluate(() => window.__passkeyReauthResolved)).toBe(true)
      const replay = await page.evaluate(async ({ body, csrf }) => {
        const response = await fetch('/api/auth/passkeys/reauth/finish', {
          method: 'POST', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf }, body,
        })
        return { status: response.status, authenticated: (await (await fetch('/api/auth/status')).json()).authenticated }
      }, { body: finish.request().postData(), csrf: after.csrf_token })
      expect(replay.status).toBe(401)
      expect(replay.authenticated).toBe(true)
    })
  } finally {
    await cdp.send('WebAuthn.removeVirtualAuthenticator', { authenticatorId })
    await cdp.detach()
  }
}
