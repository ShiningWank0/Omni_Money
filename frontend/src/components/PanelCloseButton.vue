<template>
  <button type="button" class="panel-close-button" :aria-label="label" :title="label" :disabled="disabled" @click="$emit('click', $event)">
    <span aria-hidden="true">×</span>
  </button>
</template>

<script setup>
defineProps({
  label: { type: String, default: '閉じる' },
  disabled: { type: Boolean, default: false }
})
defineEmits(['click'])
</script>

<style scoped>
.panel-close-button {
  display: inline-flex;
  flex: 0 0 44px;
  align-items: center;
  justify-content: center;
  box-sizing: border-box;
  width: 44px;
  min-width: 44px;
  height: 44px;
  min-height: 44px;
  margin: 0;
  padding: 0;
  border: 1px solid var(--omni-border, #d7e2dd);
  border-radius: 10px;
  background: #fff;
  color: var(--omni-text, #233c32);
  font-size: 2rem;
  line-height: 1;
  cursor: pointer;
}
.panel-close-button:not(:disabled):hover { background: var(--omni-surface-muted, #edf5f0); }
.panel-close-button > span { color: inherit; font: inherit; line-height: 1; }
.panel-close-button:focus-visible { outline: 3px solid #12866a; outline-offset: 2px; }
.panel-close-button:disabled { opacity: 0.5; cursor: not-allowed; }
</style>
