<script setup>
import { reactive } from 'vue'

const props = defineProps({
  loading: {
    type: Boolean,
    default: false
  }
})

const emit = defineEmits(['submit'])

const form = reactive({
  type: 'custom',
  priority: 2,
  system_prompt: '你是一个乐于助人的 AI 助手。',
  payload: ''
})

function handleSubmit() {
  const payload = form.payload.trim()
  if (!payload) return

  emit('submit', {
    type: form.type,
    priority: form.priority,
    system_prompt: form.system_prompt,
    payload
  })

  form.payload = ''
  form.priority = 2
}
</script>

<template>
  <section class="card">
    <h2>提交新任务</h2>
    <p class="lead">
      当前已接入真实提交接口。提交后，任务会进入 Redis 队列，由 Worker 异步处理。
    </p>

    <form class="auth-form" @submit.prevent="handleSubmit">
      <label>
        <span>任务类型</span>
        <select v-model="form.type">
          <option value="custom">通用任务</option>
          <option value="chat">对话</option>
          <option value="summary">摘要</option>
          <option value="generation">生成</option>
          <option value="ocr">OCR</option>
        </select>
      </label>

      <label>
        <span>系统设定</span>
        <input v-model="form.system_prompt" type="text" placeholder="输入 system prompt" />
      </label>

      <label>
        <span>任务优先级</span>
        <select v-model.number="form.priority">
          <option :value="1">低优先级</option>
          <option :value="2">普通优先级</option>
          <option :value="3">高优先级</option>
        </select>
      </label>

      <label>
        <span>任务内容</span>
        <textarea v-model="form.payload" rows="5" placeholder="输入你要提交给 AI 的内容" />
      </label>

      <button class="primary-btn" type="submit" :disabled="loading">
        {{ loading ? '提交中...' : '提交任务' }}
      </button>
    </form>
  </section>
</template>
