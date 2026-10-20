import { expect, test } from '@playwright/test'

test.skip(!process.env.E2E_BASE_URL, 'Run via tests/e2e/run-server-e2e.sh')

async function mockPanelAPIs(page) {
  const unexpected = []
  const replies = {
    '/api/auth/status': { authenticated: true, idle_timeout_seconds: 0, user: { id: 'layout-admin', role: 'admin' }, features: { admin: true, passkeys: true, snapshots: true, ai: false } },
    '/api/accounts': ['現金'],
    '/api/items': [],
    '/api/transactions': [],
    '/api/transaction-saves': { saves: [] },
    '/api/credit_card_settings': [],
    '/api/bank_account_settings': [],
    '/api/balance_history_filtered': { accounts: [], dates: [], balances: {} },
    '/api/tags': [],
    '/api/tags/summary': [],
    '/api/snapshots': [],
    '/api/admin/users': { users: [] },
    '/api/admin/invitations': { invitations: [] },
    '/api/admin/password-resets': { password_resets: [] },
    '/api/auth/passkeys': { passkeys: [] },
    '/api/auth/credentials': { password: {}, recovery: {}, passkeys: [] },
  }
  await page.route('**/api/**', route => {
    const path = new URL(route.request().url()).pathname
    if (path === '/api/auth/keepalive' && route.request().method() === 'POST') return route.fulfill({ status: 204 })
    if (route.request().method() !== 'GET' || !(path in replies)) {
      unexpected.push(`${route.request().method()} ${path}`)
      return route.fulfill({ status: 503, body: '{}' })
    }
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(replies[path]) })
  })
  return unexpected
}

async function expectUsableClose(button) {
  await expect(button).toBeVisible()
  await expect(button).toBeEnabled()
  await expect(button).toBeInViewport({ ratio: 1 })
  const { width, height } = await button.boundingBox()
  // Transforms can introduce tiny floating-point differences in the rect.
  expect(Math.round(width * 100) / 100).toBeGreaterThanOrEqual(44)
  expect(Math.round(height * 100) / 100).toBeGreaterThanOrEqual(44)
  const contrast = await button.evaluate(element => {
    const rgb = value => value.match(/[\d.]+/g).map(Number)
    const luminance = channels => channels.slice(0, 3).map(v => {
      const x = v / 255
      return x <= 0.04045 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4
    }).reduce((sum, x, i) => sum + x * [0.2126, 0.7152, 0.0722][i], 0)
    const foreground = rgb(getComputedStyle(element).color)
    let background = [255, 255, 255]
    for (let parent = element; parent; parent = parent.parentElement) {
      const candidate = rgb(getComputedStyle(parent).backgroundColor)
      if (candidate.length === 3 || candidate[3] === 1) { background = candidate; break }
    }
    const a = luminance(foreground), b = luminance(background)
    return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05)
  })
  expect(contrast).toBeGreaterThanOrEqual(4.5)
}

for (const viewport of [{ width: 320, height: 568 }, { width: 390, height: 430 }, { width: 1280, height: 900 }]) {
  test(`opened panels close through visible touch targets at ${viewport.width}x${viewport.height}`, async ({ page }) => {
    const unexpected = await mockPanelAPIs(page)
    await page.setViewportSize(viewport)
    await page.goto('/')
    await page.getByRole('button', { name: 'メニューを開く', exact: true }).click()
    const menu = page.locator('#side-menu')
    await expect(menu.getByRole('button', { name: /閉じる/ })).toHaveCount(0)
    await page.locator('.side-menu-overlay').click({ position: { x: viewport.width - 1, y: 1 } })
    await expect(menu).toHaveCount(0)
    await expect(page.locator('.hamburger-menu')).toHaveAttribute('aria-expanded', 'false')

    await page.locator('.project-selector-button').click()
    const accountClose = page.getByRole('button', { name: '資金項目の選択を閉じる', exact: true })
    await expectUsableClose(accountClose)
    await accountClose.click()
    await expect(page.locator('#account-dropdown')).toHaveCount(0)

    for (const [label, selector] of [
      ['CSVインポート', '.csv-import-modal'],
      ['クレジットカード設定', '.cc-settings-modal'],
      ['銀行口座設定', '.cc-settings-modal'],
      ['残高推移グラフ表示', '.graph-modal'],
      ['タグ別分析', '.tag-chart-modal'],
      ['タグ管理', '.tag-manager'],
      ['スナップショット管理', '.snapshot-modal'],
      ['認証情報の管理', '.credential-card'],
      ['パスキー設定', '.passkey-card'],
      ['サーバーユーザー管理', '.admin-card'],
    ]) {
      await page.getByRole('button', { name: 'メニューを開く', exact: true }).click()
      await menu.getByRole('button', { name: label, exact: true }).click()
      const panel = page.locator(selector)
      const close = panel.getByRole('button', { name: '閉じる', exact: true }).filter({ has: page.locator('[aria-hidden="true"]') })
      await expectUsableClose(close)
      await close.click()
      await expect(panel).toHaveCount(0)
    }

    await page.getByRole('button', { name: '新しい取引を追加', exact: true }).filter({ visible: true }).click()
    const transaction = page.locator('.transaction-modal')
    const close = transaction.getByRole('button', { name: '閉じる', exact: true })
    await expectUsableClose(close)
    await close.click()
    await expect(transaction).toHaveCount(0)
    expect(unexpected).toEqual([])
  })
}
