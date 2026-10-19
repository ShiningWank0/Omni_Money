import { expect } from '@playwright/test'

// Exercise real logout responses while a separate, HttpOnly cookie gates
// access like an authenticating reverse proxy. Run only in the disposable CI
// server/browser suite; never contact a deployed Pangolin instance.
export async function verifyLogoutBehindProxy(page) {
  const origin = new URL(page.url()).origin
  const proxyCookie = 'p_session_token_s.e2e'
  const proxyValue = 'disposable-proxy-session'
  await page.context().addCookies([{
    name: proxyCookie, value: proxyValue, url: origin,
    httpOnly: true, sameSite: 'Lax'
  }])
  expect(await page.evaluate(() => window.isSecureContext)).toBe(true)
  const gate = async route => {
    const request = route.request()
    const url = new URL(request.url())
    if (url.origin !== origin) return route.continue()
    const cookies = (await request.allHeaders()).cookie || ''
    const admitted = cookies.split(';').some(cookie => cookie.trim() === `${proxyCookie}=${proxyValue}`)
    if (admitted) return route.continue()
    // A request rejected here cannot reach the application to revoke or
    // confirm its session, matching the failure caused by a deleted SSO cookie.
    return route.fulfill({
      status: 401, contentType: 'text/html',
      body: '<!doctype html><title>Proxy authentication required</title><h1>Proxy authentication required</h1>'
    })
  }
  await page.route('**/*', gate)
  try {
    const before = await page.context().cookies(origin)
    const applicationCookies = before.filter(cookie => ['omni_money_session', '__Host-omni_money_session'].includes(cookie.name))
    expect(applicationCookies.length).toBeGreaterThan(0)
    await page.locator('.hamburger-menu').click()
    const responsePromise = page.waitForResponse(response =>
      new URL(response.url()).pathname === '/api/auth/logout' && response.request().method() === 'POST')
    await page.locator('#side-menu').getByRole('button', { name: 'ログアウト', exact: true }).click()
    const response = await responsePromise
    expect(response.status()).toBe(200)
    // headers() omits security-related headers; read the complete response.
    expect(await response.headerValue('clear-site-data')).toBe('"cache", "storage"')
    await page.waitForURL(url => url.pathname === '/login')
    await expect(page.locator('#email')).toBeVisible()
    await expect(page.getByRole('button', { name: 'ログアウトを再試行' })).toHaveCount(0)
    const after = await page.context().cookies(origin)
    expect(after.some(cookie => cookie.name === proxyCookie && cookie.value === proxyValue)).toBe(true)
    expect(after.some(cookie => applicationCookies.some(previous => cookie.name === previous.name))).toBe(false)
    const status = await page.evaluate(async () => {
      const response = await fetch('/api/auth/status', { credentials: 'include', cache: 'no-store' })
      return { status: response.status, body: await response.json() }
    })
    expect(status.status).toBe(200)
    expect(status.body.authenticated).toBe(false)
  } finally {
    await page.unroute('**/*', gate)
  }
}
