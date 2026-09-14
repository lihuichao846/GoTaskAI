<script setup>
import { reactive, ref } from 'vue'

const props = defineProps({
  loading: {
    type: Boolean,
    default: false
  }
})

const emit = defineEmits(['login', 'register'])

const isLoginMode = ref(true)
const form = reactive({
  username: '',
  password: ''
})

function submit() {
  const payload = {
    username: form.username.trim(),
    password: form.password
  }

  if (!payload.username || !payload.password) {
    return
  }

  emit(isLoginMode.value ? 'login' : 'register', payload)
}

// 切换登录/注册模式时清空密码，避免上一次输入残留导致登录失败。
function toggleMode() {
  isLoginMode.value = !isLoginMode.value
  form.password = ''
}
</script>

<template>
  <section class="card auth-card">
    <h2>{{ isLoginMode ? '用户登录' : '注册账号' }}</h2>
    <p class="lead">
      这是从旧版单文件页面迁移出的第一批能力，当前已接入真实后端认证接口。
    </p>

    <form class="auth-form" @submit.prevent="submit">
      <label>
        <span>用户名</span>
        <input
          v-model="form.username"
          type="text"
          minlength="3"
          maxlength="20"
          placeholder="请输入用户名"
        />
      </label>

      <label>
        <span>密码</span>
        <input
          v-model="form.password"
          type="password"
          minlength="6"
          placeholder="请输入密码"
        />
      </label>

      <button class="primary-btn" type="submit" :disabled="loading">
        {{ loading ? '处理中...' : (isLoginMode ? '登录' : '注册') }}
      </button>
    </form>

    <button class="link-btn" type="button" @click="toggleMode">
      {{ isLoginMode ? '没有账号？立即注册' : '已有账号？返回登录' }}
    </button>
  </section>
</template>
