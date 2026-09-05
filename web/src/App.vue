<script setup>
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import BackendProbe from './components/BackendProbe.vue'
import AuthPanel from './components/AuthPanel.vue'
import AgentPanel from './components/AgentPanel.vue'
import PlaceholderPanel from './components/PlaceholderPanel.vue'
import {
  batchSubmitTasks,
  createTaskStream,
  deleteSession,
  getTaskStatus,
  getTasks,
  login,
  register,
  submitTask
} from './api/client'
import { apiBaseUrl } from './config/env'

const legacyUrl = '/legacy.html'
const tokenKey = 'gotaskai_token'
const usernameKey = 'gotaskai_username'
const defaultSystemPrompt = '你是一个乐于助人的 AI 助手。'

const authToken = ref(localStorage.getItem(tokenKey) || '')
const username = ref(localStorage.getItem(usernameKey) || '')
const authLoading = ref(false)
const tasksLoading = ref(false)
const submitLoading = ref(false)
const tasks = ref([])
const message = ref('')
const messageType = ref('info')
const activeSessionId = ref('')
const chatInput = ref('')
const chatSystemPrompt = ref(defaultSystemPrompt)
const chatMessagesRef = ref(null)
const isBatchMode = ref(false)
const searchId = ref('')
const searchLoading = ref(false)
const searchResult = ref(null)
const showChatModal = ref(false)
const taskForm = ref({
  type: 'chat',
  priority: 2,
  systemPrompt: defaultSystemPrompt,
  payload: ''
})
let taskEventSource = null
const activeView = ref('workspace')

const taskTypeOptions = [
  { label: '对话对话 (Chat)', value: 'chat' },
  { label: '文本摘要 (Summary)', value: 'summary' },
  { label: '内容生成 (Generation)', value: 'generation' },
  { label: '图像分析 (OCR)', value: 'ocr' },
  { label: '自定义任务 (Custom)', value: 'custom' }
]

const priorityOptions = [
  { label: '低优先级', value: 1 },
  { label: '普通优先级 (默认)', value: 2 },
  { label: '高优先级', value: 3 }
]

const isLoggedIn = computed(() => Boolean(authToken.value))

function formatTime(value) {
  if (!value) return '-'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleString('zh-CN', { hour12: false })
}

function createSessionId() {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID()
  }
  return `sess_${Date.now()}`
}

function normalizeTextPreview(text, fallback = '未命名对话') {
  const normalized = String(text || '')
    .replace(/\s+/g, ' ')
    .trim()

  if (!normalized) return fallback
  return normalized.length > 30 ? `${normalized.slice(0, 30)}...` : normalized
}

const taskStats = computed(() => {
  const summary = {
    total: tasks.value.length,
    pending: 0,
    processing: 0,
    completed: 0,
    failed: 0,
    cancelled: 0
  }

  for (const task of tasks.value) {
    if (task?.status && summary[task.status] !== undefined) {
      summary[task.status] += 1
    }
  }

  return summary
})

const latestTask = computed(() => tasks.value[0] || null)

const taskGroups = computed(() =>
  groupedConversations.value.map((conversation) => ({
    ...conversation,
    firstTask: conversation.tasks[0] || null,
    latestResult: assistantStatusText(conversation.latestTask)
  }))
)

const groupedConversations = computed(() => {
  const groups = new Map()

  for (const task of tasks.value) {
    const key = task.session_id || task.id
    if (!groups.has(key)) {
      groups.set(key, {
        id: key,
        tasks: [],
        updatedAt: task.created_at,
        latestTask: task,
        title: normalizeTextPreview(task.payload, '新对话')
      })
    }

    const group = groups.get(key)
    group.tasks.push(task)

    const currentCreatedAt = new Date(task.created_at).getTime()
    const latestUpdatedAt = new Date(group.updatedAt).getTime()
    if (!Number.isNaN(currentCreatedAt) && (Number.isNaN(latestUpdatedAt) || currentCreatedAt > latestUpdatedAt)) {
      group.updatedAt = task.created_at
      group.latestTask = task
    }
  }

  return Array.from(groups.values())
    .map((group) => {
      const sortedTasks = [...group.tasks].sort(
        (a, b) => new Date(a.created_at).getTime() - new Date(b.created_at).getTime()
      )
      return {
        ...group,
        tasks: sortedTasks,
        title: normalizeTextPreview(sortedTasks[0]?.payload, '新对话')
      }
    })
    .sort((a, b) => new Date(b.updatedAt).getTime() - new Date(a.updatedAt).getTime())
})

const activeConversation = computed(() => {
  if (!activeSessionId.value) return null
  return groupedConversations.value.find((conversation) => conversation.id === activeSessionId.value) || null
})

const activeConversationTasks = computed(() => activeConversation.value?.tasks || [])

const activeConversationTitle = computed(() => {
  if (activeConversation.value) return activeConversation.value.title
  return activeSessionId.value.startsWith('new_') ? '新对话' : '选择一个对话'
})

const conversationSummary = computed(() => {
  if (activeConversation.value) {
    return `共 ${activeConversation.value.tasks.length} 条消息任务`
  }
  if (groupedConversations.value.length === 0) {
    return '当前还没有历史会话'
  }
  return '从左侧列表选择一个会话，或新建对话'
})

const canSendMessage = computed(() => chatInput.value.trim().length > 0 && !submitLoading.value)

const taskSubmitButtonText = computed(() => {
  if (submitLoading.value) return isBatchMode.value ? '批量提交中...' : '提交中...'
  return isBatchMode.value ? '批量提交任务' : '提交任务'
})

const workspaceSummaryItems = computed(() => [
  { key: 'total', label: '总任务', value: taskStats.value.total },
  { key: 'sessions', label: '会话数', value: groupedConversations.value.length },
  { key: 'processing', label: '处理中', value: taskStats.value.processing },
  { key: 'completed', label: '已完成', value: taskStats.value.completed }
])

function setMessage(text, type = 'info') {
  message.value = text
  messageType.value = type
}

function persistAuth(token, currentUsername) {
  authToken.value = token
  username.value = currentUsername
  localStorage.setItem(tokenKey, token)
  localStorage.setItem(usernameKey, currentUsername)
}

function clearAuth() {
  closeTaskStream()
  authToken.value = ''
  username.value = ''
  tasks.value = []
  activeSessionId.value = ''
  chatInput.value = ''
  chatSystemPrompt.value = defaultSystemPrompt
  showChatModal.value = false
  searchId.value = ''
  searchResult.value = null
  taskForm.value = {
    type: 'chat',
    priority: 2,
    systemPrompt: defaultSystemPrompt,
    payload: ''
  }
  localStorage.removeItem(tokenKey)
  localStorage.removeItem(usernameKey)
}

function upsertTask(updatedTask) {
  const index = tasks.value.findIndex((task) => task.id === updatedTask.id)
  if (index >= 0) {
    tasks.value[index] = updatedTask
    return
  }
  tasks.value.unshift(updatedTask)
}

function closeTaskStream() {
  if (taskEventSource) {
    taskEventSource.close()
    taskEventSource = null
  }
}

function applyConversationPrompt(sessionId) {
  const conversation = groupedConversations.value.find((item) => item.id === sessionId)
  const latestPrompt = conversation?.tasks?.[conversation.tasks.length - 1]?.system_prompt?.trim()
  chatSystemPrompt.value = latestPrompt || defaultSystemPrompt
}

function startNewChat(options = {}) {
  const { open = true } = options
  activeSessionId.value = `new_${Date.now()}`
  chatInput.value = ''
  chatSystemPrompt.value = defaultSystemPrompt
  showChatModal.value = open
}

function openConversation(sessionId) {
  activeSessionId.value = sessionId
  applyConversationPrompt(sessionId)
  showChatModal.value = true
}

function scrollChatToBottom() {
  nextTick(() => {
    const container = chatMessagesRef.value
    if (container) {
      container.scrollTo({ top: container.scrollHeight, behavior: 'smooth' })
    }
  })
}

function assistantStatusText(task) {
  switch (task.status) {
    case 'pending':
      return '消息已进入队列，等待处理...'
    case 'processing':
      return 'AI 正在生成回复...'
    case 'failed':
      return task.error || '本轮对话处理失败'
    case 'cancelled':
      return task.error || '本轮对话已取消'
    default:
      return task.result || ''
  }
}

function initTaskStream() {
  closeTaskStream()
  if (!authToken.value) return

  taskEventSource = createTaskStream(authToken.value)

  taskEventSource.addEventListener('task_update', (event) => {
    try {
      const updatedTask = JSON.parse(event.data)
      upsertTask(updatedTask)
      const sessionKey = updatedTask.session_id || updatedTask.id
      if (!activeSessionId.value) {
        activeSessionId.value = sessionKey
      }
      if (sessionKey === activeSessionId.value) {
        scrollChatToBottom()
      }
    } catch {
      // ignore malformed payload
    }
  })

  taskEventSource.onerror = () => {
    closeTaskStream()
    setTimeout(() => {
      if (authToken.value) {
        initTaskStream()
      }
    }, 5000)
  }
}

async function refreshTasks() {
  if (!authToken.value) return

  tasksLoading.value = true
  try {
    const data = await getTasks(authToken.value)
    tasks.value = Array.isArray(data) ? data : []
    setMessage(`任务列表已刷新，共 ${tasks.value.length} 条记录`, 'success')
  } catch (error) {
    if (error?.status === 401) {
      clearAuth()
      setMessage('登录状态已失效，请重新登录', 'error')
    } else {
      setMessage(`任务列表拉取失败：${error.message}`, 'error')
    }
  } finally {
    tasksLoading.value = false
  }
}

async function handleLogin(payload) {
  authLoading.value = true
  try {
    const data = await login(payload)
    persistAuth(data.token, data.username)
    initTaskStream()
    setMessage(`登录成功，欢迎你 ${data.username}`, 'success')
    await refreshTasks()
  } catch (error) {
    setMessage(`登录失败：${error.message}`, 'error')
  } finally {
    authLoading.value = false
  }
}

async function handleRegister(payload) {
  authLoading.value = true
  try {
    const data = await register(payload)
    setMessage(data?.message || '注册成功，请继续登录', 'success')
  } catch (error) {
    setMessage(`注册失败：${error.message}`, 'error')
  } finally {
    authLoading.value = false
  }
}

function handleLogout() {
  clearAuth()
  setMessage('你已退出登录', 'info')
}

function handleAgentRun() {
  activeView.value = 'workspace'
  refreshTasks()
}

async function handleDeleteConversation(sessionId) {
  if (!authToken.value || !sessionId || sessionId.startsWith('new_')) return
  if (!window.confirm('确定要删除这个对话及其全部历史消息吗？')) return

  try {
    await deleteSession(authToken.value, sessionId)
    tasks.value = tasks.value.filter((task) => (task.session_id || task.id) !== sessionId)
    if (activeSessionId.value === sessionId) {
      startNewChat()
    }
    setMessage('对话已删除', 'success')
  } catch (error) {
    setMessage(`删除对话失败：${error.message}`, 'error')
  }
}

async function handleChatSubmit() {
  if (!authToken.value) return
  const content = chatInput.value.trim()
  if (!content) return

  let sessionId = activeSessionId.value
  if (!sessionId || sessionId.startsWith('new_')) {
    sessionId = createSessionId()
    activeSessionId.value = sessionId
  }

  submitLoading.value = true
  try {
    const data = await submitTask(authToken.value, {
      type: 'chat',
      priority: 2,
      session_id: sessionId,
      system_prompt: chatSystemPrompt.value.trim() || defaultSystemPrompt,
      payload: content
    })
    chatInput.value = ''
    setMessage(`任务已提交，状态：${data.status}，ID：${data.id}`, 'success')
    await refreshTasks()
    scrollChatToBottom()
  } catch (error) {
    setMessage(`任务提交失败：${error.message}`, 'error')
  } finally {
    submitLoading.value = false
  }
}

function splitBatchPayload(raw) {
  return String(raw || '')
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter(Boolean)
}

async function handleTaskSubmit() {
  if (!authToken.value) return
  const payloadText = taskForm.value.payload.trim()
  if (!payloadText) {
    setMessage('请输入任务内容', 'error')
    return
  }

  submitLoading.value = true
  try {
    if (isBatchMode.value) {
      const payloads = splitBatchPayload(payloadText)
      if (payloads.length === 0) {
        setMessage('批量模式下请至少输入一条任务内容', 'error')
        return
      }

      const data = await batchSubmitTasks(authToken.value, {
        tasks: payloads.map((payload) => ({
          type: taskForm.value.type,
          priority: Number(taskForm.value.priority) || 2,
          system_prompt: taskForm.value.systemPrompt.trim() || defaultSystemPrompt,
          payload
        }))
      })

      taskForm.value.payload = ''
      setMessage(data?.message || `批量任务已提交，共 ${payloads.length} 条`, 'success')
    } else {
      const data = await submitTask(authToken.value, {
        type: taskForm.value.type,
        priority: Number(taskForm.value.priority) || 2,
        system_prompt: taskForm.value.systemPrompt.trim() || defaultSystemPrompt,
        payload: payloadText
      })

      taskForm.value.payload = ''
      setMessage(`任务已提交，状态：${data.status}，ID：${data.id}`, 'success')
    }

    await refreshTasks()
  } catch (error) {
    setMessage(`${isBatchMode.value ? '批量提交失败' : '任务提交失败'}：${error.message}`, 'error')
  } finally {
    submitLoading.value = false
  }
}

async function handleSearchTask() {
  if (!authToken.value) return
  const taskId = searchId.value.trim()
  if (!taskId) return

  searchLoading.value = true
  searchResult.value = null
  try {
    const data = await getTaskStatus(authToken.value, taskId)
    searchResult.value = data
    setMessage(`已查询到任务 ${taskId} 的最新状态`, 'success')
  } catch (error) {
    searchResult.value = null
    setMessage(`查询失败：${error.message}`, 'error')
  } finally {
    searchLoading.value = false
  }
}

function closeChatModal() {
  showChatModal.value = false
}

watch(
  groupedConversations,
  (conversations) => {
    if (!isLoggedIn.value) return

    if (conversations.length === 0) {
      if (!activeSessionId.value) {
        startNewChat()
      }
      return
    }

    if (!activeSessionId.value || activeSessionId.value.startsWith('new_')) {
      if (activeSessionId.value.startsWith('new_')) return
      activeSessionId.value = conversations[0].id
      applyConversationPrompt(conversations[0].id)
      return
    }

    const exists = conversations.some((conversation) => conversation.id === activeSessionId.value)
    if (!exists) {
      activeSessionId.value = conversations[0].id
      applyConversationPrompt(conversations[0].id)
    }
  },
  { immediate: true }
)

watch(
  () => [activeSessionId.value, activeConversationTasks.value.length],
  () => {
    scrollChatToBottom()
  }
)

onMounted(() => {
  if (authToken.value) {
    initTaskStream()
    refreshTasks()
  } else {
    startNewChat({ open: false })
  }
})

onUnmounted(() => {
  closeTaskStream()
})
</script>

<template>
  <div class="page-shell">
    <main class="container" :class="{ 'container-chat-mode': isLoggedIn }">
      <section v-if="!isLoggedIn" class="hero-grid">
        <section class="hero card hero-main">
          <div class="badge">GoTaskAI Web</div>
          <h1>前后端分离后的独立前端工作台</h1>
          <p class="lead">
            新版 `web` 前端已接入认证、任务提交、任务列表与 SSE 更新，适合作为后续继续迁移会话、运维和知识库能力的新入口。
          </p>

          <div class="cta-row">
            <a class="primary-btn" :href="legacyUrl" target="_blank" rel="noreferrer">
              打开旧版页面
            </a>
            <a class="secondary-btn" :href="`${apiBaseUrl}/admin/tasks`" target="_blank" rel="noreferrer">
              打开 Asynq 面板
            </a>
          </div>
        </section>

        <section class="card hero-side">
          <h2>接入概况</h2>
          <div class="kv-list">
            <div class="kv-row">
              <span>API 地址</span>
              <code>{{ apiBaseUrl }}</code>
            </div>
            <div class="kv-row">
              <span>认证状态</span>
              <span :class="isLoggedIn ? 'ok' : 'warning'">
                {{ isLoggedIn ? `已登录：${username}` : '未登录' }}
              </span>
            </div>
            <div class="kv-row">
              <span>任务同步</span>
              <span class="ok">{{ isLoggedIn ? 'SSE 已启用' : '登录后启用 SSE' }}</span>
            </div>
          </div>
          <p class="hint">
            当前页面只作用于 `web/` 独立前端工程，旧版静态页面入口仍保持不变。
          </p>
        </section>
      </section>

      <section
        v-if="message"
        class="card message-card"
        :class="{
          'message-info': messageType === 'info',
          'message-success': messageType === 'success',
          'message-error': messageType === 'error'
        }"
      >
        {{ message }}
      </section>

      <section v-if="!isLoggedIn" class="grid auth-layout auth-layout-wide">
        <div class="auth-main">
          <AuthPanel
            :loading="authLoading"
            @login="handleLogin"
            @register="handleRegister"
          />
        </div>
        <div class="info-stack">
          <BackendProbe />
          <section class="card">
            <h2>新版入口说明</h2>
            <ul class="bullet-list">
              <li>保留独立部署能力，前端默认通过 `VITE_API_BASE_URL` 对接后端。</li>
              <li>页面结构已转为模块化组件，便于继续迁移会话与管理页面。</li>
              <li>登录成功后会自动建立 SSE 长连接并实时更新任务状态。</li>
            </ul>
          </section>
        </div>
      </section>

      <template v-else>
        <header class="workspace-header">
          <div class="workspace-title">
            <h1>GoTaskAI Agent 平台</h1>
            <p>构建、配置并运行你的 AI Agent，统一管理工具调用、知识库与任务调度。</p>
          </div>
          <div class="workspace-toolbar">
            <button class="toolbar-link" @click="startNewChat">新建对话</button>
            <a class="toolbar-link" :href="legacyUrl" target="_blank" rel="noreferrer">旧版页面</a>
            <a class="toolbar-link" :href="`${apiBaseUrl}/admin/tasks`" target="_blank" rel="noreferrer">Asynq 面板</a>
            <span class="toolbar-user">你好, {{ username }}</span>
            <button class="toolbar-logout" @click="handleLogout">退出登录</button>
            <div class="service-status">
              <span class="service-dot"></span>
              服务运行中
            </div>
          </div>
        </header>

        <nav class="view-tabs">
          <button
            class="view-tab"
            :class="{ 'view-tab-active': activeView === 'workspace' }"
            @click="activeView = 'workspace'"
          >工作台</button>
          <button
            class="view-tab"
            :class="{ 'view-tab-active': activeView === 'agents' }"
            @click="activeView = 'agents'"
          >Agent</button>
          <button
            class="view-tab"
            :class="{ 'view-tab-active': activeView === 'tools' }"
            @click="activeView = 'tools'"
          >工具</button>
          <button
            class="view-tab"
            :class="{ 'view-tab-active': activeView === 'kb' }"
            @click="activeView = 'kb'"
          >知识库</button>
        </nav>

        <template v-if="activeView === 'workspace'">
        <section class="workspace-overview">
          <div v-for="item in workspaceSummaryItems" :key="item.key" class="overview-card">
            <span>{{ item.label }}</span>
            <strong>{{ item.value }}</strong>
          </div>
        </section>

        <section class="legacy-layout-shell">
          <aside class="legacy-side-column">
            <section class="card form-panel">
              <div class="panel-head">
                <div class="panel-title-block">
                  <h2>{{ isBatchMode ? '批量提交任务' : '提交新任务' }}</h2>
                  <p>{{ isBatchMode ? '每行一条内容，适合批量入队处理。' : '从左侧快速创建新任务并进入调度队列。' }}</p>
                </div>
                <button
                  class="batch-toggle"
                  type="button"
                  :class="{ 'batch-toggle-active': isBatchMode }"
                  @click="isBatchMode = !isBatchMode"
                >
                  <span>批量模式</span>
                  <span class="batch-toggle-track">
                    <span class="batch-toggle-thumb"></span>
                  </span>
                </button>
              </div>

              <form class="task-submit-form" @submit.prevent="handleTaskSubmit">
                <label class="prompt-field">
                  <span>任务类型</span>
                  <select v-model="taskForm.type">
                    <option v-for="option in taskTypeOptions" :key="option.value" :value="option.value">
                      {{ option.label }}
                    </option>
                  </select>
                </label>

                <label class="prompt-field">
                  <span>系统设定 (System Prompt)</span>
                  <input
                    v-model="taskForm.systemPrompt"
                    type="text"
                    placeholder="你是一个乐于助人的 AI 助手。"
                  />
                </label>

                <label class="prompt-field">
                  <span>请求内容 (Payload)</span>
                  <textarea
                    v-model="taskForm.payload"
                    rows="7"
                    :placeholder="isBatchMode ? '每行输入一条任务内容，将按多任务批量提交' : '输入要处理的文本或对话内容'"
                  />
                </label>

                <label class="prompt-field">
                  <span>任务优先级</span>
                  <select v-model="taskForm.priority">
                    <option v-for="option in priorityOptions" :key="option.value" :value="option.value">
                      {{ option.label }}
                    </option>
                  </select>
                </label>

                <button class="primary-btn submit-task-btn" type="submit" :disabled="submitLoading">
                  {{ taskSubmitButtonText }}
                </button>
              </form>
            </section>

            <section class="card query-panel">
              <div class="panel-title-block panel-title-block-compact">
                <h2>精确查询</h2>
                <p>按任务 ID 快速查看单个任务的处理状态与结果。</p>
              </div>
              <form class="task-search-form" @submit.prevent="handleSearchTask">
                <label class="prompt-field">
                  <span>任务 ID</span>
                  <input
                    v-model="searchId"
                    type="text"
                    placeholder="输入完整的 UUID"
                  />
                </label>

                <button class="secondary-btn submit-task-btn" type="submit" :disabled="!searchId.trim() || searchLoading">
                  {{ searchLoading ? '查询中...' : '查询状态' }}
                </button>
              </form>

              <section v-if="searchResult" class="query-result-card">
                <div class="query-result-row">
                  <span>状态</span>
                  <span class="status-pill" :class="`status-${searchResult.status || 'default'}`">
                    {{ searchResult.status }}
                  </span>
                </div>
                <div class="query-result-row">
                  <span>类型</span>
                  <strong>{{ searchResult.type || '-' }}</strong>
                </div>
                <div class="query-result-row">
                  <span>重试</span>
                  <strong>{{ searchResult.retries || 0 }}/{{ searchResult.max_retry || 0 }}</strong>
                </div>
                <div v-if="searchResult.result" class="query-result-text query-result-success">
                  {{ searchResult.result }}
                </div>
                <div v-if="searchResult.error" class="query-result-text query-result-error">
                  {{ searchResult.error }}
                </div>
                <button class="query-close-btn" type="button" @click="searchResult = null">关闭结果</button>
              </section>
            </section>
          </aside>

          <section class="card task-board">
            <div class="panel-head task-board-head">
              <div class="panel-title-block">
                <h2>任务列表</h2>
                <p>按会话分组展示最新状态，支持继续对话、删除与结果快速查看。</p>
              </div>
              <div class="task-board-tools">
                <span class="board-meta">{{ groupedConversations.length }} 个会话</span>
                <button class="refresh-link" type="button" @click="refreshTasks">
                  手动刷新
                </button>
              </div>
            </div>

            <div v-if="tasksLoading && taskGroups.length === 0" class="empty-state">
              正在加载任务列表...
            </div>

            <div v-else-if="taskGroups.length === 0" class="empty-state">
              暂无任务，请在左侧提交
            </div>

            <div v-else class="task-group-list">
              <article
                v-for="conversation in taskGroups"
                :key="conversation.id"
                class="task-session-card"
              >
                <div class="task-session-head">
                  <div class="task-session-title">
                    <span class="task-session-id">{{ conversation.id.slice(0, 8) }}...</span>
                    <strong>{{ conversation.latestTask.type || 'chat' }}</strong>
                    <span class="session-count-chip">对话数: {{ conversation.tasks.length }}</span>
                  </div>
                  <div class="task-session-actions">
                    <span class="status-pill" :class="`status-${conversation.latestTask.status || 'default'}`">
                      {{ conversation.latestTask.status }}
                    </span>
                    <button class="continue-btn" type="button" @click="openConversation(conversation.id)">
                      继续对话
                    </button>
                    <button class="icon-delete-btn" type="button" @click="handleDeleteConversation(conversation.id)">
                      删除
                    </button>
                  </div>
                </div>

                <div class="task-preview-block">
                  <strong>首次输入:</strong>
                  <p>{{ conversation.firstTask?.payload || '-' }}</p>
                </div>

                <div
                  class="task-preview-block task-preview-result"
                  :class="{
                    'task-preview-pending': conversation.latestTask.status === 'pending' || conversation.latestTask.status === 'processing',
                    'task-preview-error': conversation.latestTask.status === 'failed' || conversation.latestTask.status === 'cancelled'
                  }"
                >
                  <strong>最新结果:</strong>
                  <p>{{ conversation.latestResult || '等待生成回复...' }}</p>
                </div>

                <div class="task-session-foot">
                  <span>最后更新: {{ formatTime(conversation.latestTask.updated_at || conversation.updatedAt) }}</span>
                </div>
              </article>
            </div>
          </section>
        </section>
        </template>

        <AgentPanel
          v-if="activeView === 'agents'"
          :token="authToken"
          @message="setMessage"
          @agent-run="handleAgentRun"
        />

        <PlaceholderPanel
          v-if="activeView === 'tools'"
          title="工具管理"
          description="为 Agent 接入内置工具或 MCP 外部工具，赋予联网搜索、代码执行等能力。"
          :items="['联网搜索', '代码执行', '自定义 MCP 工具']"
        />

        <PlaceholderPanel
          v-if="activeView === 'kb'"
          title="知识库构建"
          description="上传文档，构建 RAG 向量库与 KAG 知识图谱，为 Agent 提供领域知识。"
          :items="['文档上传', 'RAG 向量化', 'KAG 图谱抽取']"
        />

        <section
          v-if="showChatModal"
          class="chat-dialog-backdrop"
          @click.self="closeChatModal"
        >
          <div class="chat-dialog">
            <section class="chat-main-panel">
              <header class="chat-topbar">
                <div>
                  <h1>{{ activeConversationTitle }}</h1>
                  <p>{{ conversationSummary }}</p>
                </div>
                <div class="chat-topbar-meta">
                  <span class="topbar-chip">
                    {{ activeSessionId && !activeSessionId.startsWith('new_') ? '连续对话中' : '新会话' }}
                  </span>
                  <span class="topbar-chip muted">
                    {{ latestTask ? `最近更新 ${formatTime(latestTask.updated_at || latestTask.created_at)}` : '等待开始' }}
                  </span>
                  <button class="chat-close-btn" type="button" @click="closeChatModal">关闭</button>
                </div>
              </header>

              <div ref="chatMessagesRef" class="chat-messages chat-messages-panel">
                <template v-if="activeConversationTasks.length > 0">
                  <div
                    v-for="task in activeConversationTasks"
                    :key="task.id"
                    class="chat-turn"
                  >
                    <article class="chat-bubble chat-bubble-user">
                      <div class="chat-meta">
                        <strong>我</strong>
                        <span>{{ formatTime(task.created_at) }}</span>
                      </div>
                      <p>{{ task.payload || '-' }}</p>
                    </article>

                    <article
                      class="chat-bubble chat-bubble-assistant"
                      :class="{
                        'chat-bubble-pending': task.status === 'pending' || task.status === 'processing',
                        'chat-bubble-error': task.status === 'failed' || task.status === 'cancelled'
                      }"
                    >
                      <div class="chat-meta">
                        <strong>GoTaskAI</strong>
                        <span>{{ task.status }}</span>
                      </div>
                      <p>{{ assistantStatusText(task) || '等待生成回复...' }}</p>
                    </article>
                  </div>
                </template>

                <div v-else class="chat-welcome">
                  <div class="chat-welcome-inner">
                    <div class="badge">New Chat</div>
                    <h2>开始一段新的 AI 对话</h2>
                    <p>输入第一条消息后，后续内容会自动归到同一个会话窗口。</p>
                  </div>
                </div>
              </div>

              <footer class="composer-panel">
                <form class="chat-composer ai-composer" @submit.prevent="handleChatSubmit">
                  <label class="prompt-field compact-prompt">
                    <span>系统设定</span>
                    <input
                      v-model="chatSystemPrompt"
                      type="text"
                      placeholder="例如：你是一个严谨的 AI 架构助手"
                    />
                  </label>

                  <textarea
                    v-model="chatInput"
                    rows="4"
                    placeholder="给 GoTaskAI 发送消息"
                  />

                  <div class="chat-composer-actions">
                    <span class="hint composer-hint">
                      {{ activeSessionId && !activeSessionId.startsWith('new_') ? `当前会话 ID：${activeSessionId}` : '发送后会自动创建新会话 ID' }}
                    </span>
                    <button class="primary-btn composer-send-btn" type="submit" :disabled="!canSendMessage">
                      {{ submitLoading ? '发送中...' : '发送' }}
                    </button>
                  </div>
                </form>
              </footer>
            </section>
          </div>
        </section>
      </template>
    </main>
  </div>
</template>
