import { expect, test } from '@playwright/test'

test.skip(!process.env.E2E_BASE_URL, 'Run via tests/e2e/run-server-e2e.sh')

async function mockPanelAPIs(page) {
  const unexpected = []
  const replies = {
    '/api/auth/status': { authenticated: true, idle_timeout_seconds: 0, user: { id: 'layout-admin', role: 'admin' }, features: { admin: true, passkeys: true, snapshots: true, ai: false } },
    '/api/accounts': ['現金', ...Array.from({ length: 23 }, (_, i) => `確認用資金${i + 1}`)],
    '/api/items': [],
    '/api/transactions': [],
    '/api/transaction-saves': { saves: [] },
    '/api/credit_card_settings': [],
    '/api/bank_account_settings': [],
    '/api/balance_history_filtered': { accounts: [], dates: [], balances: {} },
    '/api/tags': Array.from({ length: 24 }, (_, i) => ({ id: i + 1, name: `確認用タグ${i + 1}`, level: 1, children: [] })),
    '/api/tags/summary': [],
    '/api/snapshots': Array.from({ length: 30 }, (_, i) => `omni_money_20261020_1200${String(i).padStart(2, '0')}.db`),
    '/api/admin/users': { users: [] },
    '/api/admin/invitations': { invitations: [] },
    '/api/admin/password-resets': { password_resets: [] },
    '/api/auth/passkeys': { passkeys: Array.from({ length: 24 }, (_, i) => ({ id: `key-${i}`, name: `動作確認用パスキー${i + 1}`, created_at: '2026-10-20T00:00:00Z' })) },
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

async function expectCloseWhileScrolling(panel, close) {
  // Measure scrolling after the existing entrance animation finishes.
  await panel.evaluate(element => Promise.all(element.getAnimations({ subtree: true })
    .filter(animation => animation.effect?.getComputedTiming().iterations !== Infinity)
    .map(animation => animation.finished.catch(() => {}))))
  const body = panel.locator(':scope > .panel-scroll-content')
  if (!(await body.count())) {
    // These panels already scroll their list/form below the close button.
    for (const scroller of await panel.locator('.snapshot-list, .form-container, .graph-scroll, .fund-item-list').all()) {
      const initial = await close.boundingBox()
      await scroller.evaluate(element => { element.scrollTop = element.scrollHeight })
      await expectUsableClose(close)
      const current = await close.boundingBox()
      expect(Math.abs(current.y - initial.y)).toBeLessThan(1)
      const content = await scroller.boundingBox()
      expect(content.y).toBeGreaterThanOrEqual(current.y + current.height - 1)
    }
    return
  }
  await body.evaluate(element => { element.scrollTop = 0 })
  const initial = await close.boundingBox()
  const heading = await body.locator('h2, h3').first().boundingBox()
  expect(Math.abs(heading.y - initial.y)).toBeLessThan(1)
  expect(heading.x + heading.width).toBeLessThanOrEqual(initial.x - 8 + 1)
  const header = await body.locator(':scope > :first-child').boundingBox()
  expect(header.y + header.height).toBeGreaterThanOrEqual(initial.y + initial.height - 1)
  const panelFrame = await panel.boundingBox()
  const insets = await panel.evaluate(element => {
    const style = getComputedStyle(element)
    return {
      left: parseFloat(style.paddingLeft) + parseFloat(style.borderLeftWidth),
      right: parseFloat(style.paddingRight) + parseFloat(style.borderRightWidth),
      top: parseFloat(style.paddingTop) + parseFloat(style.borderTopWidth),
    }
  })
  expect(Math.abs(initial.y - panelFrame.y - insets.top)).toBeLessThan(1)
  expect(Math.abs(initial.x + initial.width - panelFrame.x - panelFrame.width + insets.right)).toBeLessThan(1)
  for (const fraction of [0, 0.5, 1]) {
    await body.evaluate((element, value) => { element.scrollTop = (element.scrollHeight - element.clientHeight) * value }, fraction)
    await expectUsableClose(close)
    const current = await close.boundingBox()
    expect(Math.abs(current.y - initial.y)).toBeLessThan(1)
    const content = await body.boundingBox()
    // The body fills the panel, including the space below the overlaid button.
    expect(Math.abs(content.x - panelFrame.x - insets.left)).toBeLessThan(1)
    expect(Math.abs(content.x + content.width - current.x - current.width)).toBeLessThan(1)
    // The close target stays in front and receives clicks after scrolling.
    expect(await close.evaluate(element => {
      const rect = element.getBoundingClientRect()
      return element.contains(document.elementFromPoint(rect.x + rect.width / 2, rect.y + rect.height / 2))
    })).toBe(true)
  }
  const dimensions = await body.evaluate(element => ({ width: element.clientWidth, scrollWidth: element.scrollWidth }))
  expect(dimensions.scrollWidth).toBeLessThanOrEqual(dimensions.width + 1)
  const last = await body.locator(':scope > :last-child').boundingBox()
  const frame = await body.boundingBox()
  // Scroll offsets are rounded to whole pixels, while rects can be fractional.
  expect(last.y + last.height).toBeLessThanOrEqual(frame.y + frame.height + 1)
}

for (const viewport of [{ width: 320, height: 568 }, { width: 390, height: 430 }, { width: 844, height: 390 }, { width: 768, height: 1024 }, { width: 1280, height: 900 }]) {
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
    await expectCloseWhileScrolling(page.locator('#account-dropdown'), accountClose)
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
      if (selector === '.cc-settings-modal') {
        await panel.locator('.select-button').click()
        await panel.locator('.select-dropdown').evaluate(element => element.scrollIntoView({ block: 'center' }))
        await expect(panel.locator('.select-dropdown')).toBeInViewport({ ratio: 1 })
      }
      await expectCloseWhileScrolling(panel, close)
      await close.click()
      await expect(panel).toHaveCount(0)
    }

    await page.getByRole('button', { name: '新しい取引を追加', exact: true }).filter({ visible: true }).click()
    const transaction = page.locator('.transaction-modal')
    const close = transaction.getByRole('button', { name: '閉じる', exact: true })
    await expectUsableClose(close)
    await expectCloseWhileScrolling(transaction, close)
    await close.click()
    await expect(transaction).toHaveCount(0)
    expect(unexpected).toEqual([])
  })
}

test('floating close follows the panel corner when the viewport is resized', async ({ page }) => {
  const unexpected = await mockPanelAPIs(page)
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto('/')
  await page.getByRole('button', { name: 'メニューを開く', exact: true }).click()
  await page.locator('#side-menu').getByRole('button', { name: 'クレジットカード設定', exact: true }).click()
  const panel = page.locator('.cc-settings-modal')
  const close = panel.locator(':scope > .panel-floating-close')
  for (const viewport of [{ width: 320, height: 568 }, { width: 1280, height: 900 }, { width: 390, height: 430 }]) {
    await page.setViewportSize(viewport)
    await expectCloseWhileScrolling(panel, close)
  }
  await close.click()
  await expect(panel).toHaveCount(0)
  expect(unexpected).toEqual([])
})
