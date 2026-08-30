<template>
  <div class="modal-overlay" @click="$emit('close')">
    <div class="modal-content tag-manager" @click.stop>
      <div class="tag-manager-header">
        <h3>タグ管理</h3>
        <button type="button" class="modal-close" aria-label="閉じる" @click="$emit('close')">×</button>
      </div>
      <p class="tag-manager-help">タグ名を変更したり、不要なタグ（子タグを含む）を削除できます。</p>
      <div v-if="errorMessage" class="form-error" role="alert">{{ errorMessage }}</div>
      <div v-if="rows.length === 0" class="tag-empty">タグはまだありません。</div>
      <div v-for="row in rows" :key="row.id" class="tag-manager-row" :style="{ paddingLeft: `${row.depth * 20 + 8}px` }">
        <input
          v-model="drafts[row.id]"
          class="tag-name-input"
          :aria-label="`${row.name}の名前`"
          maxlength="255"
          @keyup.enter="save(row)"
        >
        <span class="tag-level">L{{ row.level }}</span>
        <button type="button" class="tag-save-btn" :disabled="busy" @click="save(row)">保存</button>
        <button type="button" class="tag-delete-btn" :disabled="busy" @click="remove(row)">削除</button>
      </div>
      <div class="tag-manager-actions">
        <button type="button" class="cancel-btn" @click="$emit('close')">閉じる</button>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, ref } from 'vue'
import { deleteTag, getTagDeleteImpact, getTags, updateTag } from '../utils/api'
import { formatTagDeleteImpact, syncTagDrafts } from '../utils/tagManagerSafety.js'

const props = defineProps({ tags: { type: Array, default: () => [] } })
const emit = defineEmits(['close', 'changed'])
const busy = ref(false)
const errorMessage = ref('')
const drafts = ref({})

function flatten(nodes, depth = 0, result = []) {
  for (const node of nodes || []) {
    result.push({ ...node, depth })
    flatten(node.children, depth + 1, result)
  }
  return result
}

const rows = computed(() => {
  const flattened = flatten(props.tags)
  for (const row of flattened) {
    if (drafts.value[row.id] === undefined) drafts.value[row.id] = row.name
  }
  return flattened
})

async function reload() {
  const fresh = await getTags()
  drafts.value = syncTagDrafts(fresh)
  emit('changed', fresh)
}

async function save(row) {
  const value = String(drafts.value[row.id] ?? '').trim()
  if (!value || value === row.name) return
  busy.value = true
  errorMessage.value = ''
  try {
    await updateTag(row.id, value)
    await reload()
  } catch (error) {
    errorMessage.value = error?.message || 'タグ名の変更に失敗しました'
  } finally {
    busy.value = false
  }
}

async function remove(row) {
  busy.value = true
  errorMessage.value = ''
  try {
    const impact = await getTagDeleteImpact(row.id)
    const confirmation = `${formatTagDeleteImpact(impact, row.name)}\nこの操作は元に戻せません。続行しますか？`
    if (!window.confirm(confirmation)) return
    await deleteTag(row.id)
    await reload()
  } catch (error) {
    errorMessage.value = error?.message || 'タグの削除に失敗しました'
  } finally {
    busy.value = false
  }
}
</script>

<style scoped>
.tag-manager { max-width: 760px; width: min(94vw, 760px); }
.tag-manager-header { display: flex; align-items: center; justify-content: space-between; }
.tag-manager-header h3 { margin: 0; }
.modal-close { border: 0; background: transparent; font-size: 1.5rem; cursor: pointer; }
.tag-manager-help { color: #5d6570; font-size: 0.9rem; }
.tag-manager-row { display: flex; align-items: center; gap: 8px; min-height: 44px; border-bottom: 1px solid #e6e9ef; }
.tag-name-input { flex: 1; min-width: 0; padding: 7px 9px; border: 1px solid #cfd5dd; border-radius: 5px; }
.tag-level { color: #687386; font-size: 0.8rem; width: 28px; }
.tag-save-btn, .tag-delete-btn { border: 0; border-radius: 5px; padding: 7px 10px; cursor: pointer; }
.tag-save-btn { background: #e8f0ff; color: #1f55a2; }
.tag-delete-btn { background: #ffebeb; color: #a62929; }
.tag-save-btn:disabled, .tag-delete-btn:disabled { opacity: .55; cursor: wait; }
.tag-empty { padding: 28px 0; text-align: center; color: #687386; }
.tag-manager-actions { display: flex; justify-content: flex-end; margin-top: 18px; }
</style>
