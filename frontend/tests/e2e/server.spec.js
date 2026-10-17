import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { expect, test } from '@playwright/test'

// Minimal server/browser E2E. Runs only against the disposable hardened
// container started by tests/e2e/run-server-e2e.sh (E2E_BASE_URL is unset
// everywhere else, so this file never runs under `npm test`).
const baseURL = process.env.E2E_BASE_URL
const setupToken = process.env.E2E_SETUP_TOKEN
const adminEmail = process.env.E2E_ADMIN_EMAIL
const adminPassword = process.env.E2E_ADMIN_PASSWORD

test.skip(!baseURL, 'E2E_BASE_URL is not set; start the suite via tests/e2e/run-server-e2e.sh')
test.use({ actionTimeout: 10_000, navigationTimeout: 30_000 })

// Exact CSV v3 header from backend/core/service_csv_full.go (csvV3Headers).
const CSV_V3_HEADER =
  'omni_money_csv_version,record_type,id,transaction_id,parent_id,child_id,tag_id,account,date,item,type,amount,balance,memo,filename,mime_type,data_base64,tag_name,tag_parent_id,tag_level,setting_key,setting_value,created_at'

const ACCOUNT_NAME = 'E2E資金項目'
const ITEM_NAME = 'E2E検証取引'
const AMOUNT = '1500'

// RFC 4180 single-line parser. Only used to read the manifest row's quoted
// setting_value field; no digest is recomputed here (the server verifies it).
function parseCsvLine(line) {
  const fields = []
  let field = ''
  let inQuotes = false
  for (let i = 0; i < line.length; i += 1) {
    const ch = line[i]
    if (inQuotes) {
      if (ch === '"') {
        if (line[i + 1] === '"') {
          field += '"'
          i += 1
        } else {
          inQuotes = false
        }
      } else {
        field += ch
      }
    } else if (ch === '"') {
      inQuotes = true
    } else if (ch === ',') {
      fields.push(field)
      field = ''
    } else {
      field += ch
    }
  }
  fields.push(field)
  return fields
}

test('server round trip: bootstrap admin, persist a transaction, CSV v3 export and replace import, stored XSS stays inert', async ({ page }) => {
  if (!setupToken || !adminEmail || !adminPassword) {
    throw new Error('E2E_SETUP_TOKEN, E2E_ADMIN_EMAIL and E2E_ADMIN_PASSWORD must be set')
  }

  // Auto-accept the CSV export warning dialog (App.vue backupToCSV uses
  // window.confirm) and force the Blob/anchor download fallback by disabling
  // the File System Access API, which Chromium would otherwise use instead of
  // firing a Playwright download event.
  page.on('dialog', dialog => dialog.accept())
  await page.addInitScript(() => {
    window.__omniSecurityXSS = 0
    Object.defineProperty(window, 'showSaveFilePicker', {
      value: undefined,
      configurable: true,
    })
  })

  // 1. Fresh server: "/" serves the SPA and redirects to the login view in
  //    first-run setup mode.
  await page.goto('/')
  await page.waitForURL(/\/login/)
  await expect(page.getByText('最初の管理者を作成')).toBeVisible()

  // 2. Create the initial admin; the UI auto-logins and navigates to "/".
  await page.locator('#setup-token').fill(setupToken)
  await page.locator('#email').fill(adminEmail)
  await page.locator('#display-name').fill('E2E管理者')
  await page.locator('#password').fill(adminPassword)
  await page.locator('#password-confirmation').fill(adminPassword)
  await page.locator('.confirmation-label input[type="checkbox"]').check()
  await page.locator('.login-button').click()
  await page.waitForURL('/')
  // On the desktop viewport only the header-search variant of the add button
  // is visible (.header-add-btn is display:none above 768px).
  const addBtn = page.locator('.add-btn.add-btn-desktop')
  await expect(addBtn).toBeVisible()

  // 3. Add one transaction through the modal.
  await addBtn.click()
  const modal = page.locator('.transaction-modal')
  await expect(modal).toBeVisible()
  await modal.locator('input[type="date"]').fill('2026-12-31')
  await modal.locator('input[type="time"]').fill('23:59')
  await modal.locator('input[placeholder="資金項目名を入力または選択"]').fill(ACCOUNT_NAME)
  await modal.locator('input[placeholder="例: 給与、食費、交通費"]').fill(ITEM_NAME)
  await modal.locator('input[inputmode="numeric"]').fill(AMOUNT)
  // Failed mutations preserve the draft and may be retried explicitly.
  await page.route('**/api/transaction-saves', async route => {
    if (route.request().method() !== 'POST') return route.continue()
    await route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ error: 'E2E一時停止', code: 'temporarily_unavailable' }) })
  })
  await modal.locator('.ok-btn').click()
  await expect(page.getByRole('alert')).toContainText('E2E一時停止')
  await expect(modal).toBeVisible()
  await expect(modal.locator('input[placeholder="例: 給与、食費、交通費"]')).toHaveValue(ITEM_NAME)
  await page.unroute('**/api/transaction-saves')
  await modal.locator('.ok-btn').click()
  await expect(modal).toBeHidden()
  const transactionRow = page
    .locator('.transaction-table tbody tr[tabindex="0"]')
    .filter({ hasText: ITEM_NAME })
  await expect(transactionRow).toHaveCount(1)

  // 4. The transaction survives a full page reload (server persistence).
  await page.reload()
  await expect(transactionRow).toHaveCount(1)

  await page.locator('.hamburger-menu').click()
  await page.locator('#side-menu').getByRole('button', { name: 'クレジットカード設定', exact: true }).click()
  const settings = page.locator('.cc-settings-modal')
  await expect(settings).toBeVisible()
  await settings.locator('.select-button').click()
  await settings.locator('input[type="checkbox"]').check()
  await settings.locator('.select-button').click()
  await page.route('**/api/credit_card_settings', route => route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ error: 'E2E設定停止', code: 'temporarily_unavailable' }) }))
  await settings.locator('.ok-btn').click()
  await expect(settings.getByRole('alert')).toContainText('E2E設定停止')
  await expect(settings.locator('.select-button')).toContainText(ACCOUNT_NAME)
  await page.unroute('**/api/credit_card_settings')
  await settings.locator('.delete-btn').click()
  await expect(settings).toBeHidden()

  // 5. Export CSV v3 through the side menu and verify the official header and
  //    the manifest row.
  const downloadDir = fs.mkdtempSync(path.join(os.tmpdir(), 'omni-e2e-download-'))
  let csvPath = null
  try {
    await page.locator('.hamburger-menu').click()
    const exportPromise = page.waitForEvent('download')
    await page.locator('#side-menu').getByRole('button', { name: 'CSVバックアップ' }).click()
    const download = await exportPromise
    csvPath = path.join(downloadDir, download.suggestedFilename())
    await download.saveAs(csvPath)

    const csvText = fs.readFileSync(csvPath, 'utf8')
    const lines = csvText.replace(/^\uFEFF/, '').split(/\r?\n/).filter(line => line.length > 0)
    expect(lines[0]).toBe(CSV_V3_HEADER)
    expect(csvText).toContain(ITEM_NAME)

    const headerColumns = CSV_V3_HEADER.split(',')
    const manifestFields = parseCsvLine(lines[lines.length - 1])
    expect(manifestFields).toHaveLength(headerColumns.length)
    const manifestRecord = Object.fromEntries(headerColumns.map((column, i) => [column, manifestFields[i]]))
    expect(manifestRecord.record_type).toBe('manifest')
    expect(manifestRecord.setting_key).toBe('omni_money_csv_v3_manifest')
    const manifest = JSON.parse(manifestRecord.setting_value)
    expect(manifest.format).toBe('omni-money-csv-v3')
    expect(manifest.version).toBe(3)
    expect(manifest.digest).toMatch(/^[0-9a-f]{64}$/)
    expect(manifest.counts.manifest).toBe(1)
    expect(manifest.counts.transaction).toBeGreaterThanOrEqual(1)

    // 6. Re-import the same CSV in replace mode through the UI.
    await page.locator('.hamburger-menu').click()
    await page.locator('#side-menu').getByRole('button', { name: 'CSVインポート' }).click()
    const importModal = page.locator('.csv-import-modal')
    await expect(importModal).toBeVisible()
    await importModal.locator('input[type="file"]').setInputFiles(csvPath)
    await importModal.locator('input[type="radio"][value="replace"]').check()
    await expect(importModal.locator('.ok-btn')).toBeDisabled()
    await importModal.locator('.preview-btn').click()
    await expect(importModal.locator('.import-preview')).toBeVisible()
    await expect(importModal.locator('.preview-impact')).toContainText('取引 1件')
    await expect(importModal.locator('.ok-btn')).toBeDisabled()
    await importModal.locator('.replace-confirmation input[type="checkbox"]').check()
    await importModal.locator('.ok-btn').click()
    await expect(importModal.locator('.status-success')).toContainText('置換しました')
    await expect(importModal).toBeHidden()

    // 7. The imported ledger still holds the transaction, before and after a
    //    refresh.
    await expect(transactionRow).toHaveCount(1)
    await page.reload()
    await expect(transactionRow).toHaveCount(1)
  } finally {
    fs.rmSync(downloadDir, { recursive: true, force: true })
  }

  await test.step('Persisted XSS rendering and transaction details', () => verifyPersistedXSSRendering(page, addBtn))
  await test.step('Responsive transaction table columns', () => verifyTransactionTableLayout(page))
})

// Reuse the existing bootstrap within its single-worker scenario. A second
// bootstrap test against the same disposable server would race first setup.
async function verifyPersistedXSSRendering(page, addBtn) {
  const account = 'E2E"><img data-omni-xss src=x onerror="window.__omniSecurityXSS=1">'
  const item = '<svg data-omni-xss onload="window.__omniSecurityXSS=1">E2E-XSS'
  const memo = '<script data-omni-xss>window.__omniSecurityXSS=1</script>'
  const tag = '<img data-omni-xss src=x onerror="window.__omniSecurityXSS=1">'
  const assertInert = async () => {
    // CSP can prevent execution while HTML injection is still present. Check
    // both the DOM and execution sentinel, preserving legitimate icons/images.
    await expect(page.locator('[data-omni-xss]')).toHaveCount(0)
    await expect(page.locator('[onerror*="__omniSecurityXSS"], [onload*="__omniSecurityXSS"], [onfocus*="__omniSecurityXSS"]')).toHaveCount(0)
    expect(await page.evaluate(() => window.__omniSecurityXSS)).toBe(0)
  }

  await addBtn.click()
  const modal = page.locator('.transaction-modal')
  await modal.locator('input[placeholder="資金項目名を入力または選択"]').fill(account)
  await modal.locator('input[placeholder="例: 給与、食費、交通費"]').fill(item)
  await modal.locator('input[inputmode="numeric"]').fill('37')
  await modal.locator('input[placeholder="メモを入力"]').fill(memo)
  await modal.locator('.new-tag-input').fill(tag)
  await modal.locator('.new-tag-row .add-tag-btn').click()
  await expect(modal.locator('.tag-badge')).toContainText(tag)
  await assertInert()
  await modal.locator('.ok-btn').click()
  await expect(modal).toBeHidden()

  // Existing account selections survive a write. Select the new account via
  // the real dropdown before asserting the row, also testing that rendering.
  await page.locator('.project-selector-button').click()
  const accountOption = page.locator('.fund-item-checkbox').filter({ hasText: account })
  await expect(accountOption.locator('.fund-item-name')).toHaveText(account)
  await assertInert()
  // The native input sits below the custom checkmark. Use its visible label,
  // as a user does, and verify the resulting state without forced clicks.
  const accountCheckbox = accountOption.locator('input[type="checkbox"]')
  if (!(await accountCheckbox.isChecked())) await accountOption.click()
  await expect(accountCheckbox).toBeChecked()
  await page.locator('.project-selector-button').click()

  const row = page.locator('.transaction-table tbody tr[tabindex="0"]').filter({ hasText: item })
  await expect(row).toHaveCount(1)
  await assertInert()
  await page.reload()
  await expect(row).toHaveCount(1)
  await expect(row).toContainText(account)
  await assertInert()

  await row.click()
  const details = page.locator('.transaction-details')
  await expect(details).toBeVisible()
  await expect(details.locator('.details-item')).toHaveText(item)
  await expect(details.locator('.details-memo')).toHaveText(memo)
  await expect(details.locator('.details-fields dd').nth(1)).toHaveText(account)
  await expect(details.locator('.details-fields dd').nth(4)).toHaveText(tag)
  await assertInert()

  await details.getByRole('button', { name: '取引を編集', exact: true }).click()
  await expect(modal).toBeVisible()
  await expect(modal.locator('input[placeholder="資金項目名を入力または選択"]')).toHaveValue(account)
  await expect(modal.locator('input[placeholder="例: 給与、食費、交通費"]')).toHaveValue(item)
  await expect(modal.locator('input[placeholder="メモを入力"]')).toHaveValue(memo)
  await expect(modal.locator('.tag-badge')).toContainText(tag)
  await assertInert()
  await modal.locator('.cancel-btn').click()
  await expect(modal).toBeHidden()
  // Cancelling an edit returns to the transaction details. Close that dialog
  // through its real button before the next scenario uses the table controls.
  await expect(details).toBeVisible()
  await assertInert()
  await details.getByRole('button', { name: '閉じる', exact: true }).click()
  await expect(details).toBeHidden()
}

async function verifyTransactionTableLayout(page) {
  await expect(page.locator('.modal-overlay')).toHaveCount(0)
  const table = page.locator('.transaction-table')
  const timedRow = table.locator('tbody tr[tabindex="0"]').filter({ hasText: ITEM_NAME })
  await expect(timedRow.locator('.transaction-time')).toHaveText('23:59:00')

  for (const columns of [5, 4]) {
    if (columns === 4) {
      await page.locator('.project-selector-button').click()
      for (const option of await page.locator('.fund-item-checkbox').all()) {
        const selected = await option.locator('.fund-item-name').textContent() === ACCOUNT_NAME
        if (await option.locator('input[type="checkbox"]').isChecked() !== selected) await option.click()
      }
      await page.locator('.project-selector-button').click()
    }
    await expect(table.locator('thead th')).toHaveCount(columns)
    await expect(table.locator('tbody tr[tabindex="0"]')).toHaveCount(columns === 5 ? 2 : 1)

    for (const width of [1440, 900, 390]) {
      await page.setViewportSize({ width, height: 900 })
      await expect.poll(async () => timedRow.locator('.transaction-time').evaluate(element => getComputedStyle(element).display))
        .toBe(width === 390 ? 'block' : 'inline-block')
      const geometry = await table.evaluate(element => {
        const bounds = cell => {
          const rect = cell.getBoundingClientRect()
          return { left: rect.left, right: rect.right, width: rect.width, fits: cell.scrollWidth <= cell.clientWidth }
        }
        return {
          headers: [...element.tHead.rows[0].cells].map(bounds),
          rows: [...element.tBodies[0].rows].map(row => [...row.cells].map(bounds)),
          pageFits: document.documentElement.scrollWidth <= window.innerWidth,
        }
      })
      expect(geometry.pageFits).toBe(true)
      for (const cells of geometry.rows) {
        for (const [index, cell] of cells.entries()) {
          expect(Math.abs(cell.left - geometry.headers[index].left)).toBeLessThan(1)
          expect(Math.abs(cell.right - geometry.headers[index].right)).toBeLessThan(1)
        }
        expect(cells.at(-2).fits).toBe(true)
        expect(cells.at(-1).fits).toBe(true)
      }
      if (columns === 4 && width === 1440) {
        expect(geometry.headers[1].width).toBeGreaterThan(geometry.headers[0].width * 2)
        expect(geometry.headers[1].width).toBeGreaterThan(geometry.headers[2].width * 2)
        expect(geometry.headers[1].width).toBeGreaterThan(geometry.headers[3].width * 2)
      }
    }
  }

  // A narrow table inside a wide window must also wrap its time. This guards
  // container-based behavior independently of viewport/device breakpoints.
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.locator('.transaction-section').evaluate(element => { element.style.width = '560px' })
  await expect.poll(async () => timedRow.locator('.transaction-time').evaluate(element => getComputedStyle(element).display)).toBe('block')
  await page.locator('.transaction-section').evaluate(element => { element.style.removeProperty('width') })
  await expect.poll(async () => timedRow.locator('.transaction-time').evaluate(element => getComputedStyle(element).display)).toBe('inline-block')
}
