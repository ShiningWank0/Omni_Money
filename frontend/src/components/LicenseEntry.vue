<template>
  <button ref="trigger" type="button" :class="menu ? 'menu-btn menu-group-start' : 'license-link'" @click="open = true">
    オープンソースライセンス
  </button>
  <LicenseDialog v-if="open" @close="close" />
</template>

<script setup>
import { defineAsyncComponent, nextTick, ref } from 'vue'
const LicenseDialog = defineAsyncComponent(() => import('./LicenseDialog.vue'))
defineProps({ menu: { type: Boolean, default: false } })
const open = ref(false)
const trigger = ref(null)
async function close() {
  open.value = false
  await nextTick()
  trigger.value?.focus()
}
</script>

<style scoped>
.license-link {
  display: block;
  margin: 0.4rem auto 0;
  padding: 0.4rem;
  border: 0;
  background: transparent;
  background-image: none;
  box-shadow: none;
  appearance: none;
  --omni-button-shadow-rest: none;
  --omni-button-shadow-hover: none;
  --omni-button-shadow-pressed: none;
  color: var(--omni-text-muted, #526b61);
  font: inherit;
  font-size: 0.75rem;
  text-decoration: underline;
  text-underline-offset: 0.2em;
  cursor: pointer;
}
.license-link:focus-visible { outline: 3px solid #12866a; outline-offset: 2px; border-radius: 4px; }
</style>
