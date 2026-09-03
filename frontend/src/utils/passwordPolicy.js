export const minimumPasswordBytes = 12
export const maximumPasswordBytes = 1024
export const passwordInputMaxLength = 1024

export function passwordByteLength(password) {
  return new TextEncoder().encode(password).length
}

export function validatePasswordBytes(password) {
  const length = passwordByteLength(password)
  if (length < minimumPasswordBytes || length > maximumPasswordBytes) {
    throw new Error('パスワードはUTF-8で12〜1024 bytesにしてください')
  }
}

export function validateNewPassword(password, confirmation) {
  validatePasswordBytes(password)
  if (password !== confirmation) throw new Error('パスワードが一致しません')
}
