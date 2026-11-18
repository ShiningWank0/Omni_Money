// Vite removes this branch from server builds. Native code and runtime chunks
// are distributed only in desktop artifacts, matching the OSS target inventory.
const generatedBindings = () => {
  if (__OMNI_DESKTOP_BUILD__) return import('../../bindings/omni_money/app.js')
  throw new Error('Desktop bindings are unavailable in this build')
}

// Runtime and generated service bindings come from the same pinned Go module.
export async function initializeDesktopBridge({ document: page = document, window: target = window,
  bindings = generatedBindings } = {}) {
  if (page.documentElement.dataset.omniDesktop !== 'wails3') return false
  const service = await bindings()
  if (typeof service.GetDesktopVaultStatus !== 'function') throw new Error('Desktop binding is unavailable')
  target.go = Object.freeze({ main: Object.freeze({ App: Object.freeze({ ...service }) }) })
  return true
}
