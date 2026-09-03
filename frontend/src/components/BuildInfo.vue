<template>
  <p class="build-info" aria-label="アプリケーションバージョン">{{ buildInfoLabel }}</p>
</template>

<script setup>
import { buildInfoLabel } from '../utils/buildInfo'
</script>

<style scoped>
.build-info {
  margin: 0;
  color: var(--omni-text-muted, #66708f);
  font-size: 0.75rem;
  font-weight: 650;
  letter-spacing: 0.02em;
  line-height: 1.4;
  text-align: center;
}
</style>
