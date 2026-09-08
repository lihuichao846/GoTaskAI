<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import { deleteTool, listToolAgents, listTools, updateToolStatus } from '../api/client'
import ToolFormModal from './ToolFormModal.vue'

const props = defineProps({
  token: { type: String, default: '' }
})

const emit = defineEmits(['message'])

const tools = ref([])
const loading = ref(false)
const searchQuery = ref('')
// tool_id -> 绑定 Agent 列表
const toolAgents = reactive({})
const expanded = reactive({})

const editTarget = ref(null)
const showEdit = ref(false)

const filteredTools = computed(() => {
  const q = searchQuery.value.trim().toLowerCase()
  if (!q) return tools.value
  return tools.value.filter(
    (t) =>
      (t.name || '').toLowerCase().includes(q) ||
      (t.display_name || '').toLowerCase().includes(q) ||
      (t.description || '').toLowerCase().includes(q)
  )
})

async function refresh() {
  loading.value = true
  try {
    tools.value = await listTools(props.token)
    await loadAgentBindings()
  } catch (e) {
    emit('message', `加载工具失败：${e.message}`, 'error')
  } finally {
    loading.value = false
  }
}

// 并行拉取每个工具绑定的 Agent，用于展示数量与展开列表。
async function loadAgentBindings() {
  const tasks = tools.value.map(async (t) => {
    try {
      toolAgents[t.id] = await listToolAgents(props.token, t.id)
    } catch {
      toolAgents[t.id] = []
    }
  })
  await Promise.all(tasks)
}

function agentCount(toolId) {
  return (toolAgents[toolId] || []).length
}

function toggleExpand(toolId) {
  expanded[toolId] = !expanded[toolId]
}

async function toggleStatus(tool) {
  const next = tool.status === 'disabled' ? 'active' : 'disabled'
  try {
    await updateToolStatus(props.token, tool.id, next)
    emit('message', `工具已${next === 'active' ? '启用' : '停用'}`, 'success')
    await refresh()
  } catch (e) {
    emit('message', `状态更新失败：${e.message}`, 'error')
  }
}

function openEdit(tool) {
  editTarget.value = tool
  showEdit.value = true
}

async function remove(tool) {
  if (!window.confirm(`确定删除工具「${tool.display_name || tool.name}」吗？`)) return
  try {
    await deleteTool(props.token, tool.id)
    emit('message', '工具已删除', 'success')
    await refresh()
  } catch (e) {
    emit('message', `删除失败：${e.message}`, 'error')
  }
}

async function onSaved() {
  await refresh()
}

onMounted(refresh)
</script>

<template>
  <div>
    <div class="tool-search">
      <input v-model="searchQuery" type="text" placeholder="按名称 / 描述搜索工具" />
      <span class="tool-count">共 {{ tools.length }} 个</span>
    </div>

    <div v-if="loading" class="empty-state">加载中...</div>
    <div v-else-if="filteredTools.length === 0" class="empty-state">暂无工具</div>

    <div v-else class="tool-table">
      <div v-for="tool in filteredTools" :key="tool.id" class="tool-row">
        <div class="tool-row-main">
          <div class="tool-name">
            <strong>{{ tool.display_name || tool.name }}</strong>
            <span class="type-badge" :class="{ builtin: tool.is_builtin }">
              {{ tool.is_builtin ? '内置' : '私有' }}
            </span>
            <code v-if="tool.name !== (tool.display_name || tool.name)">{{ tool.name }}</code>
          </div>
          <p v-if="tool.description" class="tool-desc">{{ tool.description }}</p>
          <div class="tool-meta">
            <span class="status-badge" :class="{ off: tool.status === 'disabled' }">
              {{ tool.status === 'disabled' ? '已停用' : '启用' }}
            </span>
            <button
              class="link-btn"
              :disabled="tool.is_builtin"
              @click="toggleStatus(tool)"
            >
              {{ tool.status === 'disabled' ? '启用' : '停用' }}
            </button>
          </div>
        </div>

        <div class="tool-row-side">
          <button class="link-btn" @click="toggleExpand(tool.id)">
            绑定 Agent（{{ agentCount(tool.id) }}）
          </button>
          <div class="tool-actions">
            <button
              class="secondary-btn"
              :disabled="tool.is_builtin"
              @click="openEdit(tool)"
            >
              编辑
            </button>
            <button
              class="danger-btn"
              :disabled="tool.is_builtin"
              @click="remove(tool)"
            >
              删除
            </button>
          </div>
        </div>

        <div v-if="expanded[tool.id]" class="tool-agents">
          <div v-if="agentCount(tool.id) === 0" class="empty-state">暂未被任何 Agent 绑定</div>
          <div v-for="agent in toolAgents[tool.id] || []" :key="agent.id" class="agent-chip">
            {{ agent.name }}
          </div>
        </div>
      </div>
    </div>

    <ToolFormModal
      :token="token"
      :open="showEdit"
      mode="edit"
      :initial="editTarget || {}"
      @close="showEdit = false"
      @saved="onSaved"
      @message="($event, $type) => emit('message', $event, $type)"
    />
  </div>
</template>

<style scoped>
.tool-search {
  display: flex;
  align-items: center;
  gap: 0.8rem;
  margin-bottom: 1rem;
}
.tool-search input {
  flex: 1;
}
.tool-count {
  font-size: 0.82rem;
  color: var(--muted, #8c8c8c);
  white-space: nowrap;
}
.tool-table {
  display: flex;
  flex-direction: column;
  gap: 0.6rem;
}
.tool-row {
  border: 1px solid var(--border, #e2e2e2);
  border-radius: 8px;
  padding: 0.7rem 0.8rem;
  background: var(--bg-soft, #fff);
}
.tool-row-main {
  display: flex;
  flex-direction: column;
  gap: 0.3rem;
}
.tool-name {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  flex-wrap: wrap;
}
.tool-name code {
  font-size: 0.78rem;
  color: var(--muted, #8c8c8c);
}
.type-badge {
  font-size: 0.72rem;
  padding: 0.05rem 0.4rem;
  border-radius: 999px;
  background: var(--accent, #4f6ef7);
  color: #fff;
}
.type-badge.builtin {
  background: #8a8f98;
}
.tool-desc {
  font-size: 0.82rem;
  color: var(--muted, #6b6b6b);
  margin: 0;
}
.tool-meta {
  display: flex;
  align-items: center;
  gap: 0.6rem;
  margin-top: 0.2rem;
}
.status-badge {
  font-size: 0.72rem;
  padding: 0.05rem 0.45rem;
  border-radius: 999px;
  background: #e9f7ef;
  color: #1d7a3a;
  border: 1px solid #bfe3cc;
}
.status-badge.off {
  background: #f5f5f5;
  color: #8c8c8c;
  border: 1px solid #ddd;
}
.tool-row-side {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-top: 0.5rem;
  gap: 0.6rem;
  flex-wrap: wrap;
}
.tool-actions {
  display: flex;
  gap: 0.5rem;
}
.link-btn {
  background: none;
  border: none;
  color: var(--accent, #4f6ef7);
  cursor: pointer;
  font-size: 0.82rem;
  padding: 0;
}
.link-btn:disabled {
  color: var(--muted, #b8b8b8);
  cursor: not-allowed;
}
.danger-btn {
  background: none;
  border: 1px solid #f3c3c3;
  color: #b3261e;
  cursor: pointer;
  font-size: 0.82rem;
  padding: 0.3rem 0.7rem;
  border-radius: 6px;
}
.danger-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.tool-agents {
  margin-top: 0.5rem;
  padding-top: 0.5rem;
  border-top: 1px dashed var(--border, #ddd);
  display: flex;
  flex-wrap: wrap;
  gap: 0.4rem;
}
.agent-chip {
  font-size: 0.78rem;
  padding: 0.1rem 0.5rem;
  border-radius: 999px;
  background: #eef1f6;
  color: #333;
}
</style>
