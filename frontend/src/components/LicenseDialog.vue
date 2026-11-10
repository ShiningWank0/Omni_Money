<template>
  <Teleport to="body">
    <div class="license-overlay" @click.self="$emit('close')">
      <section ref="dialog" class="license-dialog" role="dialog" aria-modal="true" aria-labelledby="license-title" :data-oss-target="catalog.target.id" tabindex="-1" @keydown="handleKeydown">
        <header class="license-header">
          <h2 id="license-title">オープンソースライセンス</h2>
          <PanelCloseButton label="ライセンスを閉じる" @click="$emit('close')" />
        </header>
        <div class="license-content">
          <p>Omni Moneyは、以下のオープンソースソフトウェアを利用しています。</p>
          <p>{{ targetLabel }}で使用するソフトウェアを掲載しています。各ソフトウェアの条件は原文をご確認ください。</p>
          <details class="license-item">
            <summary>Omni Money — Apache-2.0</summary>
            <pre>{{ catalog.applicationLicense }}</pre>
          </details>
          <details v-for="component in catalog.components" :key="component.id" class="license-item" :data-oss-component="component.id" :data-oss-version="component.version">
            <summary>{{ component.name }} <span>{{ component.version }} · {{ component.license }}</span></summary>
            <a :href="component.url" target="_blank" rel="noopener noreferrer">公式情報・配布元</a>
            <template v-for="file in component.files" :key="file.path">
              <p class="license-filename">{{ file.path }}</p>
              <pre>{{ file.text }}</pre>
            </template>
          </details>
        </div>
      </section>
    </div>
  </Teleport>
</template>

<script setup>
import { onMounted, ref } from 'vue'
import PanelCloseButton from './PanelCloseButton.vue'
import catalog from 'virtual:omni-oss-notices'
const targetLabel = `${catalog.target.mode === 'desktop' ? 'Desktop版' : 'サーバー版'}（${{ darwin: 'macOS', windows: 'Windows', linux: 'Linux' }[catalog.target.goos]} / ${catalog.target.goarch}）`
const emit = defineEmits(['close'])
const dialog = ref(null)
onMounted(() => dialog.value?.focus())
function handleKeydown(event) {
  if (event.key === 'Escape') {
    event.preventDefault()
    event.stopPropagation()
    emit('close')
  } else if (event.key === 'Tab') {
    const elements = [...dialog.value.querySelectorAll('button, summary, a[href]')].filter(element => !element.closest('details:not([open])') || element.tagName === 'SUMMARY')
    const first = elements[0], last = elements[elements.length - 1]
    if (event.shiftKey && (document.activeElement === first || document.activeElement === dialog.value)) {
      event.preventDefault(); last?.focus()
    } else if (!event.shiftKey && (document.activeElement === last || document.activeElement === dialog.value)) {
      event.preventDefault(); first?.focus()
    }
  }
}
</script>

<style scoped>
.license-overlay { position: fixed; inset: 0; z-index: 10000; display: flex; align-items: center; justify-content: center; padding: 1rem; background: rgb(15 35 27 / 55%); box-sizing: border-box; }
.license-dialog { display: flex; flex-direction: column; width: min(54rem, 100%); min-width: 0; max-height: calc(100dvh - 2rem); overflow: hidden; border-radius: 16px; background: var(--omni-surface, #fff); color: var(--omni-text, #233c32); box-shadow: 0 20px 60px rgb(0 0 0 / 20%); outline: none; }
.license-header { display: flex; flex-shrink: 0; align-items: center; justify-content: space-between; gap: 0.75rem; padding: 1rem; border-bottom: 1px solid #d7e2dd; }
.license-header h2 { min-width: 0; margin: 0; font-size: 1.1rem; overflow-wrap: anywhere; }
.license-content { overflow-y: auto; overflow-x: hidden; overscroll-behavior: contain; padding: 0 1rem 1rem; min-height: 0; min-width: 0; }
.license-content > p { font-size: 0.9rem; line-height: 1.6; }
.license-item { min-width: 0; margin-top: 0.75rem; border: 1px solid #d7e2dd; border-radius: 8px; padding: 0.75rem; overflow-wrap: anywhere; }
.license-item summary { cursor: pointer; overflow-wrap: anywhere; font-weight: 650; line-height: 1.5; }
.license-item summary span { display: block; font-size: 0.8rem; font-weight: 400; }
.license-item pre { max-width: 100%; white-space: pre-wrap; overflow-wrap: anywhere; word-break: break-word; tab-size: 2; font-size: 0.75rem; line-height: 1.6; font-family: ui-monospace, monospace; }
.license-item a { color: #08634e; }
.license-scope, .license-filename { font-size: 0.8rem; overflow-wrap: anywhere; }
</style>
