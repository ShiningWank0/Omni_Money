import { devices, expect, test } from '@playwright/test'

// Layout fixtures only: all API requests are intercepted, so these scenarios
// do not change the disposable server's bootstrap, users, or transaction data.
test.skip(!process.env.E2E_BASE_URL, 'Run via tests/e2e/run-server-e2e.sh')

const profiles = [
  { name: 'Pixel 10', ...devices['Pixel 10'] },
  { name: '375px phone', ...devices['iPhone 13'], viewport: { width: 375, height: 667 } },
  { name: 'iPhone 13', ...devices['iPhone 13'] },
  { name: 'iPhone 16', ...devices['iPhone 16'] },
  { name: 'iPhone 17', ...devices['iPhone 17'] },
  { name: 'Pixel 7', ...devices['Pixel 7'] },
]
const account = '普段使いの銀行口座と長い資金項目名'
const otherAccount = 'もう一つの口座'
const item = '食品・日用品のまとめ買いと長い項目名abcdefghijklmnopqrstuvwxyz'

async function mockLedger(page, columns, amount, empty = false) {
  const accounts = columns === 5 ? [account, otherAccount] : [account]
  const transactions = empty ? [] : [{
    id: 1, account, item, date: '2026-10-19 23:59:00', type: 'expense',
    amount, balance: -amount, amount_exact: String(amount), balance_exact: String(-amount), memo: '',
  }]
  await page.route('**/api/**', route => {
    const url = new URL(route.request().url())
    const responses = {
      '/api/auth/status': {
        authenticated: true, user: { id: 'mobile-layout-user' }, csrf_token: 'layout-csrf',
        features: { admin: false, ai: false, snapshots: false, passkeys: false },
      },
      '/api/accounts': accounts,
      '/api/transaction-saves': { saves: [] },
      '/api/transactions': transactions,
      '/api/credit_card_settings': [],
      '/api/bank_account_settings': accounts,
      '/api/items': [item],
    }
    return route.fulfill({
      status: Object.hasOwn(responses, url.pathname) ? 200 : 501,
      contentType: 'application/json',
      body: JSON.stringify(responses[url.pathname] ?? { error: 'Unexpected layout fixture request' }),
    })
  })
}

async function assertLedgerFits(page, columns) {
  const section = page.locator('.transaction-section')
  const table = section.locator('.transaction-table')
  await expect(table.locator('thead th')).toHaveCount(columns)
  await expect(section).toHaveAttribute('aria-busy', 'false')
  const geometry = await table.evaluate(element => {
    const section = element.closest('.transaction-section')
    const viewport = section.getBoundingClientRect()
    const bounds = cell => {
      const rect = cell.getBoundingClientRect()
      const range = document.createRange()
      range.selectNodeContents(cell)
      const text = range.getBoundingClientRect()
      const textRects = [...range.getClientRects()].filter(rect => rect.width > 0 && rect.height > 0)
      return {
        left: rect.left, right: rect.right, width: rect.width,
        fits: cell.scrollWidth <= cell.clientWidth + 1,
        textFits: text.left >= rect.left - 1 && text.right <= rect.right + 1,
        singleLine: text.height <= Math.max(...textRects.map(rect => rect.height)) + 1,
      }
    }
    return {
      left: viewport.left, right: viewport.left + section.clientWidth,
      sectionFits: section.scrollWidth <= section.clientWidth + 1,
      pageFits: document.documentElement.scrollWidth <= window.innerWidth + 1,
      headers: [...element.tHead.rows[0].cells].map(bounds),
      rows: [...element.querySelectorAll('tbody tr[tabindex="0"]')].map(row => [...row.cells].map(bounds)),
    }
  })
  // A page can fit while its table silently scrolls. Check the inner viewport
  // and the rightmost cell as well, which is the original phone regression.
  expect(geometry.pageFits).toBe(true)
  expect(geometry.sectionFits).toBe(true)
  for (const header of geometry.headers) {
    expect(header.fits).toBe(true)
    expect(header.textFits).toBe(true)
    expect(header.singleLine).toBe(true)
  }
  for (const cells of geometry.rows) {
    expect(cells[0].left).toBeGreaterThanOrEqual(geometry.left - 1)
    expect(cells.at(-1).right).toBeLessThanOrEqual(geometry.right + 1)
    for (const [index, cell] of cells.entries()) {
      expect(Math.abs(cell.left - geometry.headers[index].left)).toBeLessThan(1)
      expect(Math.abs(cell.right - geometry.headers[index].right)).toBeLessThan(1)
    }
    for (const numeric of cells.slice(-2)) {
      expect(numeric.fits).toBe(true)
      expect(numeric.textFits).toBe(true)
    }
  }
}

for (const profile of profiles) {
  test.describe(profile.name + ' portrait ledger', () => {
    test.use({
      viewport: profile.viewport, deviceScaleFactor: profile.deviceScaleFactor,
      isMobile: true, hasTouch: true, userAgent: profile.userAgent,
    })
    for (const columns of [4, 5]) {
      test(`${columns} columns show amounts through one billion yen`, async ({ page }) => {
        await mockLedger(page, columns, 1_000_000_000)
        await page.goto('/')
        const row = page.locator('.transaction-table tbody tr[tabindex="0"]')
        await expect(row).toHaveCount(1)
        await expect(row.locator('.item-cell')).toHaveText(item)
        await expect(row.locator('.numeric-cell')).toHaveText(['-¥1,000,000,000', '¥-1,000,000,000'])
        await assertLedgerFits(page, columns)
      })
    }
    test('empty five-column ledger fits without vertical headings', async ({ page }) => {
      await mockLedger(page, 5, 0, true)
      await page.goto('/')
      await expect(page.locator('.table-status-row')).toHaveText('取引はまだありません')
      await assertLedgerFits(page, 5)
    })
  })
}
