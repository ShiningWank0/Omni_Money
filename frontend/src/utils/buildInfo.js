const FALLBACK_VERSION = 'dev'
const MAX_VERSION_LENGTH = 64

/**
 * Resolve the build-time version without allowing malformed environment values
 * to affect the surrounding UI. Release values are normalized by the workflow,
 * while local and CI builds may use labels such as "dev" or "ci".
 */
export function resolveBuildVersion(value) {
  if (typeof value !== 'string') return FALLBACK_VERSION

  const normalized = value.trim()
  if (
    normalized.length === 0 ||
    normalized.length > MAX_VERSION_LENGTH ||
    /[\u0000-\u001f\u007f]/.test(normalized)
  ) {
    return FALLBACK_VERSION
  }

  // VERSION accepts an optional SemVer "v" prefix. Release workflows strip it,
  // but keeping this normalization here prevents a direct Docker/Wails build
  // from displaying the prefix twice ("vv1.1.0").
  return /^v\d/.test(normalized) ? normalized.slice(1) : normalized
}

export function formatBuildInfo(value) {
  const version = resolveBuildVersion(value)
  return version === FALLBACK_VERSION ? `Omni Money ${FALLBACK_VERSION}` : `Omni Money v${version}`
}

export const buildVersion = resolveBuildVersion(import.meta.env?.VITE_APP_VERSION)
export const buildInfoLabel = formatBuildInfo(buildVersion)
