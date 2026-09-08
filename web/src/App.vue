<script setup>
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import BackendProbe from './components/BackendProbe.vue'
import AuthPanel from './components/AuthPanel.vue'
import AgentPanel from './components/AgentPanel.vue'
import ToolPanel from './components/ToolPanel.vue'
import KnowledgeBasePanel from './components/KnowledgeBasePanel.vue'
import {
  createConversation,
  createTaskStream,
  deleteConversation,
  getTaskStatus,
  getTasks,
  listAgents,
  listConversations,
  login,
  register,
  runAgent
} from './api/client'
import { apiBaseUrl } from './config/env'

const tokenKey = 'gotaskai_token'
const usernameKey = 'gotaskai_username'

const authToken = ref(localStorage.getItem(tokenKey) || '')
const username = ref(localStorage.getItem(usernameKey) || '')
const authLoading = ref(false)
const submitLoading = ref(false)
const tasks = ref([])
const message = ref('')
const messageType = ref('info')
const chatInput = ref('')
const chatMessagesRef = ref(null)
// 与某个 Agent 的独立大窗对话
const chatModalAgentId = ref('')
const chatModalInput = ref('')
const chatModalRef = ref(null)
const searchId = ref('')
const searchLoading = ref(false)
const searchResult = ref(null)
const agents = ref([])
const agentsLoading = ref(false)
const selectedAgentId = ref('')
const conversations = ref([])
const toolEvents = ref([])
let taskEventSource = null
const activeView = ref('workspace')

const navItems = [
  { key: 'workspace', label: '工作台' },
  { key: 'agents', label: 'Agent' },
  { key: 'tools', label: '工具' },
  { key: 'kb', label: '知识库' },
  { key: 'tasks', label: '任务中心' }
]

const viewMeta = computed(() => {
  const meta = {
    workspace: { title: '工作台', desc: '在对话中运行 Agent，实时跟踪任务状态与结果。' },
    agents: { title: 'Agent', desc: '创建并配置可复用的智能体，统一管理提示词、模型与运行。' },
    tools: { title: '工具', desc: '为 Agent 接入内置工具或 MCP 外部工具，赋予联网搜索、代码执行等能力。' },
    kb: { title: '知识库', desc: '构建 RAG 向量库与 KAG 知识图谱，为 Agent 提供领域知识。' },
    tasks: { title: '任务中心', desc: '按任务 ID 查询与浏览原始任务记录。' }
  }
  return meta[activeView.value] || meta.workspace
})

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

const selectedAgent = computed(() =>
  agents.value.find((agent) => agent.id === selectedAgentId.value) || null
)

// 每个 Agent 对应的最新会话 ID（用于恢复多轮上下文）。
const conversationByAgent = computed(() => {
  const map = {}
  for (const conv of conversations.value) {
    if (conv.agent_id && !map[conv.agent_id]) {
      map[conv.agent_id] = conv.id
    }
  }
  return map
})

const activeAgentTasks = computed(() =>
  tasks.value
    .filter((task) => task.agent_id === selectedAgentId.value)
    .sort((a, b) => new Date(a.created_at).getTime() - new Date(b.created_at).getTime())
)

const activeAgentTitle = computed(() => selectedAgent.value?.name || '选择一个 Agent')

const activeAgentSummary = computed(() => {
  if (selectedAgent.value) {
    const prompt = (selectedAgent.value.system_prompt || '').trim()
    return prompt ? `${prompt.slice(0, 60)}${prompt.length > 60 ? '...' : ''}` : '该 Agent 暂无系统设定'
  }
  return agents.value.length > 0 ? '从左侧选择一个 Agent 开始对话' : '还没有 Agent，请先创建一个'
})

// 独立大窗对话相关计算
const chatModalAgent = computed(
  () => agents.value.find((agent) => agent.id === chatModalAgentId.value) || null
)

const chatModalTasks = computed(() =>
  tasks.value
    .filter((task) => task.agent_id === chatModalAgentId.value)
    .sort((a, b) => new Date(a.created_at).getTime() - new Date(b.created_at).getTime())
)

const chatModalTitle = computed(() => chatModalAgent.value?.name || '独立对话')

const chatModalSummary = computed(() => {
  const prompt = (chatModalAgent.value?.system_prompt || '').trim()
  return prompt ? `${prompt.slice(0, 80)}${prompt.length > 80 ? '...' : ''}` : (chatModalAgent.value?.model || '默认模型')
})

const canSendChatModalMessage = computed(() => chatModalInput.value.trim().length > 0 && !submitLoading.value)

const canSendMessage = computed(() => chatInput.value.trim().length > 0 && !submitLoading.value)

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
  chatInput.value = ''
  searchId.value = ''
  searchResult.value = null
  agents.value = []
  selectedAgentId.value = ''
  conversations.value = []
  toolEvents.value = []
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

function selectAgent(id) {
  selectedAgentId.value = id
  chatInput.value = ''
  scrollChatToBottom()
}

function goCreateAgent() {
  activeView.value = 'agents'
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

function toolEventsForTask(taskId) {
  return toolEvents.value.filter((ev) => ev.task_id === taskId)
}

function initTaskStream() {
  closeTaskStream()
  if (!authToken.value) return

  taskEventSource = createTaskStream(authToken.value)

  taskEventSource.addEventListener('task_update', (event) => {
    try {
      const updatedTask = JSON.parse(event.data)
      upsertTask(updatedTask)
      if (updatedTask.agent_id === selectedAgentId.value) {
        scrollChatToBottom()
      }
    } catch {
      // ignore malformed payload
    }
  })

  taskEventSource.addEventListener('tool_call', (event) => {
    try {
      const ev = JSON.parse(event.data)
      toolEvents.value.push(ev)
      if (ev.agent_id === selectedAgentId.value) {
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
  }
}

async function refreshAgents() {
  if (!authToken.value) return

  agentsLoading.value = true
  try {
    const data = await listAgents(authToken.value)
    agents.value = Array.isArray(data) ? data : []
    if (!selectedAgentId.value && agents.value.length > 0) {
      selectedAgentId.value = agents.value[0].id
    }
  } catch (error) {
    setMessage(`Agent 列表加载失败：${error.message}`, 'error')
  } finally {
    agentsLoading.value = false
  }
}

async function refreshConversations() {
  if (!authToken.value) return
  try {
    const data = await listConversations(authToken.value)
    conversations.value = Array.isArray(data) ? data : []
  } catch {
    // 会话列表非关键路径，失败时静默忽略
  }
}

// 获取或创建指定 Agent 的会话 ID（优先复用已有会话）。
async function getOrCreateConversationId(agentId) {
  const existing = conversationByAgent.value[agentId]
  if (existing) return existing

  try {
    const data = await createConversation(authToken.value, { agent_id: agentId })
    const id = data?.conversation?.id || ''
    await refreshConversations()
    return id
  } catch {
    // 会话接口异常时回退到本地生成，后端会以其作为会话 ID 复用。
    return createSessionId()
  }
}

// 返回指定 Agent 当前会话的标题（用于展示）。
function conversationTitle(agentId) {
  const convId = conversationByAgent.value[agentId]
  if (!convId) return ''
  const conv = conversations.value.find((c) => c.id === convId)
  return conv?.title || ''
}

// 为当前 Agent 新建一个空会话。
async function newConversation() {
  if (!selectedAgentId.value) {
    setMessage('请先选择一个 Agent', 'error')
    return
  }
  try {
    const data = await createConversation(authToken.value, { agent_id: selectedAgentId.value })
    await refreshConversations()
    const ok = Boolean(data?.conversation?.id)
    setMessage(ok ? '已新建会话' : '新建会话失败', ok ? 'success' : 'error')
  } catch (error) {
    setMessage(`新建会话失败：${error.message}`, 'error')
  }
}

// 删除当前 Agent 的当前会话。
async function removeCurrentConversation() {
  if (!selectedAgentId.value) return
  const convId = conversationByAgent.value[selectedAgentId.value]
  if (!convId) {
    setMessage('当前没有可删除的会话', 'error')
    return
  }
  if (!window.confirm('确定删除当前会话吗？此操作不会删除已产生的任务记录。')) return
  try {
    await deleteConversation(authToken.value, convId)
    await refreshConversations()
    setMessage('会话已删除', 'success')
  } catch (error) {
    setMessage(`删除会话失败：${error.message}`, 'error')
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
    await refreshAgents()
    await refreshConversations()
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
  refreshAgents()
}

async function handleAgentChatSubmit() {
  if (!authToken.value || !selectedAgentId.value) return
  const content = chatInput.value.trim()
  if (!content) return

  const sessionId = await getOrCreateConversationId(selectedAgentId.value)

  submitLoading.value = true
  try {
    const data = await runAgent(authToken.value, selectedAgentId.value, {
      payload: content,
      session_id: sessionId,
      priority: 2
    })
    chatInput.value = ''
    setMessage(`已发送给 Agent，任务 ID：${data.id}`, 'success')
    await refreshTasks()
    await refreshConversations()
    scrollChatToBottom()
  } catch (error) {
    setMessage(`发送失败：${error.message}`, 'error')
  } finally {
    submitLoading.value = false
  }
}

function openChatModal(agentId) {
  chatModalAgentId.value = agentId
  chatModalInput.value = ''
  nextTick(() => scrollChatModalToBottom())
}

function closeChatModal() {
  chatModalAgentId.value = ''
  chatModalInput.value = ''
}

function scrollChatModalToBottom() {
  nextTick(() => {
    const container = chatModalRef.value
    if (container) {
      container.scrollTo({ top: container.scrollHeight, behavior: 'smooth' })
    }
  })
}

async function handleChatModalSubmit() {
  const agentId = chatModalAgentId.value
  if (!authToken.value || !agentId) return
  const content = chatModalInput.value.trim()
  if (!content) return

  const sessionId = await getOrCreateConversationId(agentId)

  submitLoading.value = true
  try {
    const data = await runAgent(authToken.value, agentId, {
      payload: content,
      session_id: sessionId,
      priority: 2
    })
    chatModalInput.value = ''
    await refreshTasks()
    await refreshConversations()
    scrollChatModalToBottom()
  } catch (error) {
    setMessage(`发送失败：${error.message}`, 'error')
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

watch(
  () => [selectedAgentId.value, activeAgentTasks.value.length],
  () => {
    scrollChatToBottom()
  }
)

watch(
  () => [chatModalAgentId.value, chatModalTasks.value.length],
  () => {
    if (chatModalAgentId.value) {
      scrollChatModalToBottom()
    }
  }
)

onMounted(() => {
  if (authToken.value) {
    initTaskStream()
    refreshTasks()
    refreshAgents()
    refreshConversations()
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
            <a class="primary-btn" :href="`${apiBaseUrl}/admin/tasks`" target="_blank" rel="noreferrer">
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
            当前页面作用于 `web/` 独立前端工程，登录后会自动建立 SSE 长连接并实时更新任务状态。
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
        <div class="app-shell">
          <aside class="app-sidebar">
            <div class="app-brand">
              <div class="app-brand-mark">GT</div>
              <div class="app-brand-text">
                <strong>GoTaskAI</strong>
                <span>Agent 平台</span>
              </div>
            </div>

            <nav class="app-nav">
              <button
                v-for="item in navItems"
                :key="item.key"
                type="button"
                class="app-nav-item"
                :class="{ 'app-nav-item-active': activeView === item.key }"
                @click="activeView = item.key"
              >
                <span class="app-nav-dot"></span>
                <span>{{ item.label }}</span>
              </button>
            </nav>

            <div class="app-sidebar-foot">
              <div class="service-status">
                <span class="service-dot"></span>
                <span>服务运行中</span>
              </div>
              <div class="app-user">
                <div class="app-user-avatar">{{ username.slice(0, 1).toUpperCase() }}</div>
                <div class="app-user-meta">
                  <strong>{{ username }}</strong>
                  <button type="button" @click="handleLogout">退出登录</button>
                </div>
              </div>
            </div>
          </aside>

          <div class="app-main">
            <header class="app-topbar">
              <div class="app-topbar-title">
                <h1>{{ viewMeta.title }}</h1>
                <p>{{ viewMeta.desc }}</p>
              </div>
              <div class="app-topbar-actions">
                <a class="app-topbar-link" :href="`${apiBaseUrl}/admin/tasks`" target="_blank" rel="noreferrer">Asynq 面板</a>
                <button v-if="activeView === 'workspace'" type="button" class="primary-btn" @click="goCreateAgent">新建 Agent</button>
              </div>
            </header>

            <div class="app-content">
              <!-- 工作台：内联对话 -->
              <section v-if="activeView === 'workspace'" class="conversation-workspace">
                <aside class="conv-list-panel">
                  <button type="button" class="primary-btn conv-new-btn" @click="goCreateAgent">新建 Agent</button>

                  <div class="conv-list">
                    <div
                      v-for="agent in agents"
                      :key="agent.id"
                      class="conv-item"
                      :class="{ 'conv-item-active': agent.id === selectedAgentId }"
                    >
                      <button type="button" class="conv-item-main" @click="selectAgent(agent.id)">
                        <strong>{{ agent.name }}</strong>
                        <span>{{ conversationTitle(agent.id) || agent.model || '默认模型' }}</span>
                      </button>
                      <div class="conv-item-side">
                        <span class="status-pill status-completed">{{ agent.status || 'active' }}</span>
                        <button type="button" class="chat-modal-open-btn" @click="openChatModal(agent.id)">独立对话</button>
                      </div>
                    </div>

                    <div v-if="agentsLoading" class="conv-list-empty">正在加载 Agent...</div>
                    <div v-else-if="agents.length === 0" class="conv-list-empty">暂无 Agent，点击上方新建</div>
                  </div>

                  <div class="conv-list-foot">
                    <span>{{ agents.length }} 个 Agent</span>
                    <span>{{ taskStats.completed }} 已完成任务</span>
                  </div>
                </aside>

                <section class="conv-chat-panel">
                  <header class="conv-chat-head">
                    <div class="conv-chat-title">
                      <h2>{{ activeAgentTitle }}</h2>
                      <p>{{ activeAgentSummary }}</p>
                    </div>
                    <div class="conv-chat-actions">
                      <button v-if="selectedAgent" type="button" class="secondary-btn" @click="newConversation">新对话</button>
                      <button v-if="selectedAgent && conversationByAgent[selectedAgent.id]" type="button" class="secondary-btn" @click="removeCurrentConversation">删除会话</button>
                      <button v-if="selectedAgent" type="button" class="secondary-btn" @click="openChatModal(selectedAgent.id)">独立窗口</button>
                      <button v-if="selectedAgent" type="button" class="secondary-btn" @click="goCreateAgent">管理 Agent</button>
                    </div>
                  </header>

                  <div ref="chatMessagesRef" class="chat-messages chat-messages-panel">
                    <template v-if="activeAgentTasks.length > 0">
                      <div v-for="task in activeAgentTasks" :key="task.id" class="chat-turn">
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
                            <strong>{{ selectedAgent?.name || 'Agent' }}</strong>
                            <span>{{ task.status }}</span>
                          </div>
                          <p>{{ assistantStatusText(task) || '等待生成回复...' }}</p>
                        </article>

                        <div v-if="toolEventsForTask(task.id).length" class="tool-trace">
                          <div
                            v-for="ev in toolEventsForTask(task.id)"
                            :key="`${ev.task_id}-${ev.tool_name}-${ev.status}-${ev.timestamp}`"
                            class="tool-trace-item"
                          >
                            <span class="tool-trace-status" :class="`tool-trace-${ev.status}`">{{ ev.status }}</span>
                            <strong>{{ ev.tool_name }}</strong>
                            <span v-if="ev.arguments" class="tool-trace-args">{{ ev.arguments }}</span>
                            <span v-if="ev.status === 'success' && ev.result" class="tool-trace-result">{{ ev.result }}</span>
                            <span v-if="ev.status === 'failed' && ev.error" class="tool-trace-error">{{ ev.error }}</span>
                          </div>
                        </div>
                      </div>
                    </template>

                    <div v-else class="chat-welcome">
                      <div class="chat-welcome-inner">
                        <div class="badge">{{ selectedAgent ? 'Agent' : 'No Agent' }}</div>
                        <h2>{{ selectedAgent ? `与「${selectedAgent.name}」对话` : '选择一个 Agent 开始' }}</h2>
                        <p>{{ selectedAgent ? '发送消息后，任务会绑定到当前 Agent 并异步执行。' : '从左侧选择一个已搭建的智能体，或先创建一个。' }}</p>
                      </div>
                    </div>
                  </div>

                  <footer class="composer-panel">
                    <form class="chat-composer ai-composer" @submit.prevent="handleAgentChatSubmit">
                      <textarea
                        v-model="chatInput"
                        rows="2"
                        :placeholder="selectedAgent ? `给 ${selectedAgent.name} 发送消息` : '请先选择一个 Agent'"
                      />

                      <div class="chat-composer-actions">
                        <span class="hint composer-hint">
                          {{ selectedAgent ? `当前 Agent：${selectedAgent.name}` : '尚未选择 Agent' }}
                        </span>
                        <button class="primary-btn composer-send-btn" type="submit" :disabled="!canSendMessage || !selectedAgent">
                          {{ submitLoading ? '发送中...' : '发送' }}
                        </button>
                      </div>
                    </form>
                  </footer>
                </section>
              </section>

              <!-- Agent -->
              <AgentPanel
                v-if="activeView === 'agents'"
                :token="authToken"
                @message="setMessage"
                @agent-run="handleAgentRun"
              />

              <!-- 工具 -->
              <ToolPanel
                v-if="activeView === 'tools'"
                :token="authToken"
                @message="setMessage"
              />

              <!-- 知识库 -->
              <KnowledgeBasePanel
                v-if="activeView === 'kb'"
                :token="authToken"
                @message="setMessage"
              />

              <!-- 任务中心（弱化为原始任务查询） -->
              <section v-if="activeView === 'tasks'" class="task-center">
                <section class="card query-panel">
                    <div class="panel-head">
                      <div class="panel-title-block">
                        <h2>原始任务查询</h2>
                        <p>按任务 ID 快速查看单个任务的处理状态与结果。</p>
                      </div>
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

                  <section class="card task-list-panel">
                    <div class="panel-head">
                      <div class="panel-title-block">
                        <h2>任务列表</h2>
                        <p>共 {{ tasks.length }} 条原始任务记录。</p>
                      </div>
                    </div>
                    <div v-if="tasks.length === 0" class="empty-state">暂无任务</div>
                    <div v-else class="task-list">
                      <div v-for="task in tasks" :key="task.id" class="task-list-item">
                        <div class="task-list-item-main">
                          <strong>{{ task.id }}</strong>
                          <span>{{ task.type }} · {{ formatTime(task.created_at) }}</span>
                        </div>
                        <span class="status-pill" :class="`status-${task.status || 'default'}`">{{ task.status }}</span>
                      </div>
                    </div>
                  </section>
              </section>
            </div>
          </div>
        </div>

        <!-- 与某个 Agent 的独立宽大对话弹窗 -->
        <div v-if="chatModalAgentId && chatModalAgent" class="chat-modal-overlay" @click.self="closeChatModal">
          <div class="chat-modal">
            <header class="chat-modal-head">
              <div class="chat-modal-title">
                <h2>{{ chatModalTitle }}</h2>
                <p>{{ chatModalSummary }}</p>
              </div>
              <div class="chat-modal-tools">
                <span class="status-pill status-completed">{{ chatModalAgent?.status || 'active' }}</span>
                <button type="button" class="chat-modal-close" @click="closeChatModal">关闭</button>
              </div>
            </header>

            <div ref="chatModalRef" class="chat-modal-body chat-messages">
              <template v-if="chatModalTasks.length > 0">
                <div v-for="task in chatModalTasks" :key="task.id" class="chat-turn">
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
                      <strong>{{ chatModalAgent?.name || 'Agent' }}</strong>
                      <span>{{ task.status }}</span>
                    </div>
                    <p>{{ assistantStatusText(task) || '等待生成回复...' }}</p>
                  </article>

                  <div v-if="toolEventsForTask(task.id).length" class="tool-trace">
                    <div
                      v-for="ev in toolEventsForTask(task.id)"
                      :key="`${ev.task_id}-${ev.tool_name}-${ev.status}-${ev.timestamp}`"
                      class="tool-trace-item"
                    >
                      <span class="tool-trace-status" :class="`tool-trace-${ev.status}`">{{ ev.status }}</span>
                      <strong>{{ ev.tool_name }}</strong>
                      <span v-if="ev.arguments" class="tool-trace-args">{{ ev.arguments }}</span>
                      <span v-if="ev.status === 'success' && ev.result" class="tool-trace-result">{{ ev.result }}</span>
                      <span v-if="ev.status === 'failed' && ev.error" class="tool-trace-error">{{ ev.error }}</span>
                    </div>
                  </div>
                </div>
              </template>

              <div v-else class="chat-welcome">
                <div class="chat-welcome-inner">
                  <div class="badge">Agent</div>
                  <h2>与「{{ chatModalAgent?.name }}」开始对话</h2>
                  <p>发送消息后，任务会绑定到当前 Agent 并异步执行。</p>
                </div>
              </div>
            </div>

            <footer class="chat-modal-foot">
              <form class="chat-composer ai-composer" @submit.prevent="handleChatModalSubmit">
                <textarea
                  v-model="chatModalInput"
                  rows="2"
                  :placeholder="`给 ${chatModalAgent?.name} 发送消息`"
                />
                <div class="chat-composer-actions">
                  <span class="hint composer-hint">独立对话窗口</span>
                  <button class="primary-btn composer-send-btn" type="submit" :disabled="!canSendChatModalMessage">
                    {{ submitLoading ? '发送中...' : '发送' }}
                  </button>
                </div>
              </form>
            </footer>
          </div>
        </div>
      </template>
    </main>
  </div>
</template>
