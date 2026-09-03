import assert from 'node:assert/strict'
import test from 'node:test'

const calls = []
global.window = {
  go: {
    main: {
      App: {
        RotateDesktopVaultRecovery(currentPassword, recoveryCode) {
          calls.push({ currentPassword, recoveryCode })
          return Promise.resolve({ configured: true, unlocked: true })
        }
      }
    }
  },
  location: { origin: 'wails://wails', pathname: '/' }
}

const { rotateDesktopVaultRecovery } = await import('../src/utils/api.js?desktop-recovery')

test('Desktop recovery rotation forwards the exact saved client candidate', async () => {
  const candidate = 'WlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlo'
  const status = await rotateDesktopVaultRecovery('current-password', candidate)

  assert.deepEqual(calls, [{ currentPassword: 'current-password', recoveryCode: candidate }])
  assert.equal(status.unlocked, true)
})
