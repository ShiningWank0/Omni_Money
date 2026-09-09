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

test('server round trip: bootstrap admin, persist a transaction, CSV v3 export and replace import', async ({ page }) => {
  if (!setupToken || !adminEmail || !adminPassword) {
    throw new Error('E2E_SETUP_TOKEN, E2E_ADMIN_EMAIL and E2E_ADMIN_PASSWORD must be set')
  }

  // Auto-accept the CSV export warning dialog (App.vue backupToCSV uses
  // window.confirm) and force the Blob/anchor download fallback by disabling
  // the File System Access API, which Chromium would otherwise use instead of
  // firing a Playwright download event.
  page.on('dialog', dialog => dialog.accept())
  await page.addInitScript(() => {
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
  await modal.locator('input[placeholder="資金項目名を入力または選択"]').fill(ACCOUNT_NAME)
  await modal.locator('input[placeholder="例: 給与、食費、交通費"]').fill(ITEM_NAME)
  await modal.locator('input[inputmode="numeric"]').fill(AMOUNT)
  await modal.locator('.ok-btn').click()
  await expect(modal).toBeHidden()
  const transactionRow = page
    .locator('.transaction-table tbody tr[tabindex="0"]')
    .filter({ hasText: ITEM_NAME })
  await expect(transactionRow).toHaveCount(1)

  // 4. The transaction survives a full page reload (server persistence).
  await page.reload()
  await expect(transactionRow).toHaveCount(1)

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
})
