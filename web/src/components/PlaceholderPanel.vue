<script setup>
defineProps({
  title: { type: String, default: '' },
  description: { type: String, default: '' },
  items: { type: Array, default: () => [] }
})
</script>

<template>
  <section class="card placeholder-panel">
    <span class="badge">规划中</span>
    <h2>{{ title }}</h2>
    <p class="lead">{{ description }}</p>

    <div v-if="items.length" class="placeholder-list">
      <div v-for="item in items" :key="item" class="placeholder-item">{{ item }}</div>
    </div>

    <p class="hint">该功能界面已就绪，后端接口暂未提供。</p>
  </section>
</template>
