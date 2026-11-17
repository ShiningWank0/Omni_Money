// Runtime and generated service bindings come from the same pinned Go module.
export async function initializeDesktopBridge({ document: page = document, window: target = window,
  bindings = () => import('../../bindings/omni_money/app.js') } = {}) {
  if (page.documentElement.dataset.omniDesktop !== 'wails3') return false
  const service = await bindings()
  if (typeof service.GetDesktopVaultStatus !== 'function') throw new Error('Desktop binding is unavailable')
  target.go = Object.freeze({ main: Object.freeze({ App: Object.freeze({ ...service }) }) })
  return true
}
