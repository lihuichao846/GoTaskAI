<script setup>
import { onMounted, ref } from 'vue'
import { probeBackend } from '../api/client'
import { apiBaseUrl } from '../config/env'

const loading = ref(true)
const result = ref(null)

async function runProbe() {
  loading.value = true
  result.value = await probeBackend()
  loading.value = false
}

onMounted(runProbe)
</script>

<template>
  <section class="card">
    <div class="section-header">
      <div>
        <h2>后端连通性</h2>
        <p>用于验证独立前端是否可以跨域访问当前 Go API 服务。</p>
      </div>
      <button class="secondary-btn" @click="runProbe">重新探测</button>
    </div>

    <div class="kv-list">
      <div class="kv-row">
        <span>API Base URL</span>
        <code>{{ apiBaseUrl }}</code>
      </div>
      <div class="kv-row">
        <span>探测结果</span>
        <span v-if="loading">检测中...</span>
        <span v-else-if="result?.reachable" class="ok">
          可访问，HTTP {{ result.status }}
        </span>
        <span v-else class="error">
          不可访问{{ result?.message ? `：${result.message}` : '' }}
        </span>
      </div>
    </div>

    <p class="hint">
      说明：这里请求的是受保护接口 `/api/tasks`。如果返回 `401`，通常说明 API 服务已可达，只是当前没有携带登录 Token。
    </p>
  </section>
</template>
