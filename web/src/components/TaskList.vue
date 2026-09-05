<script setup>
function formatTime(value) {
  if (!value) return '-'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleString('zh-CN', { hour12: false })
}

function statusClass(status) {
  return {
    pending: 'status-pending',
    processing: 'status-processing',
    completed: 'status-completed',
    failed: 'status-failed',
    cancelled: 'status-cancelled'
  }[status] || 'status-default'
}

defineProps({
  tasks: {
    type: Array,
    default: () => []
  },
  loading: {
    type: Boolean,
    default: false
  }
})
</script>

<template>
  <section class="card">
    <div class="section-header">
      <div>
        <h2>任务列表</h2>
        <p>这里集中展示当前账号的任务状态，并通过 SSE 自动接收任务更新。</p>
      </div>
      <slot name="actions" />
    </div>

    <div v-if="loading" class="empty-state">任务加载中...</div>
    <div v-else-if="tasks.length === 0" class="empty-state">当前暂无任务记录</div>

    <div v-else class="task-list">
      <article v-for="task in tasks" :key="task.id" class="task-card">
        <div class="task-head">
          <code>{{ task.id }}</code>
          <span class="status-pill" :class="statusClass(task.status)">
            {{ task.status }}
          </span>
        </div>

        <div class="task-meta">
          <span>类型：{{ task.type }}</span>
          <span>优先级：{{ task.priority }}</span>
          <span>重试：{{ task.retries }}/{{ task.max_retry }}</span>
        </div>

        <div class="task-block">
          <strong>输入</strong>
          <p>{{ task.payload || '-' }}</p>
        </div>

        <div v-if="task.result" class="task-block task-result">
          <strong>结果</strong>
          <p>{{ task.result }}</p>
        </div>

        <div v-if="task.error" class="task-block task-error">
          <strong>错误</strong>
          <p>{{ task.error }}</p>
        </div>

        <div class="task-foot">
          <span>创建时间：{{ formatTime(task.created_at) }}</span>
          <span>更新时间：{{ formatTime(task.updated_at) }}</span>
        </div>
      </article>
    </div>
  </section>
</template>
