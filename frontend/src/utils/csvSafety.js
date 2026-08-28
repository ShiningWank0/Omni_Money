export function csvExportWarning(desktop) {
  const destination = desktop
    ? '次の画面で、FileVault・BitLocker・LUKS等で保護された保存先ディレクトリを選択してください。'
    : 'ブラウザのダウンロード先が、FileVault・BitLocker・LUKS等で保護されたボリューム上にあることを確認してください。'
  return [
    'CSVバックアップは暗号化されていない平文で、取引内容をそのまま読み取れます。',
    destination,
    '不要になったら速やかに削除してください。SSDやブラウザを介して保存したデータは、削除しても完全に消去されたことを保証できません。',
    'このリスクを理解したうえでCSVを書き出しますか？'
  ].join('\n\n')
}

export function canStartCSVImport({ hasFile, importing, mode, replaceConfirmed }) {
  return Boolean(hasFile) && !importing && (mode !== 'replace' || replaceConfirmed)
}
