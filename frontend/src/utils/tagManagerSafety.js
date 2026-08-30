// Keep the editable draft map aligned with the server's authoritative tag
// tree after a rename/delete. Returning a new map also drops deleted IDs, so
// an old draft can never be submitted later and roll a rename back.
export function syncTagDrafts(tags) {
  const drafts = {}
  function visit(nodes) {
    for (const tag of nodes || []) {
      drafts[tag.id] = tag.name
      visit(tag.children)
    }
  }
  visit(tags)
  return drafts
}

export function formatTagDeleteImpact(impact, fallbackName = 'タグ') {
  const name = impact?.tag_name || fallbackName
  const descendants = Number(impact?.descendant_count || 0)
  const transactions = Number(impact?.transaction_count || 0)
  return `「${name}」を削除します。子タグ ${descendants}件、紐付いた取引 ${transactions}件のタグ情報も削除されます。`
}
