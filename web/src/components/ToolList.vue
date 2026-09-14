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
              <svg class="icon icon-sm" viewBox="0 0 24 24" aria-hidden="true">
                <path d="M12 3v9" />
                <path d="M6.6 6.6a8 8 0 1 0 10.8 0" />
              </svg>
              {{ tool.status === 'disabled' ? '启用' : '停用' }}
            </button>
          </div>
        </div>

        <div class="tool-row-side">
          <button class="link-btn" @click="toggleExpand(tool.id)">
            <svg class="icon icon-sm" viewBox="0 0 24 24" aria-hidden="true" :style="expanded[tool.id] ? 'transform: rotate(180deg)' : ''">
              <path d="m6 9 6 6 6-6" />
            </svg>
            绑定 Agent（{{ agentCount(tool.id) }}）
          </button>
          <div class="tool-actions">
            <button
              class="secondary-btn"
              :disabled="tool.is_builtin"
              @click="openEdit(tool)"
            >
              <svg class="icon icon-sm" viewBox="0 0 24 24" aria-hidden="true">
                <path d="M12 20h9" />
                <path d="M16.5 3.5a2.12 2.12 0 0 1 3 3L7 19l-4 1 1-4z" />
              </svg>
              编辑
            </button>
            <button
              class="danger-btn"
              :disabled="tool.is_builtin"
              @click="remove(tool)"
            >
              <svg class="icon icon-sm" viewBox="0 0 24 24" aria-hidden="true">
                <path d="M3 6h18M8 6V4h8v2M19 6l-1 14H6L5 6M10 11v6M14 11v6" />
              </svg>
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
  gap: var(--space-3);
  margin-bottom: var(--space-4);
}
.tool-search input {
  flex: 1;
  min-width: 0;
  padding: var(--space-3);
  border: 1px solid var(--border-default);
  border-radius: var(--radius-md);
  font: inherit;
  font-size: var(--text-base);
  background: var(--bg-2);
  color: var(--text-primary);
  outline: none;
  transition: border-color var(--dur-fast) var(--ease-out),
    box-shadow var(--dur-fast) var(--ease-out);
}
.tool-search input::placeholder {
  color: var(--text-disabled);
}
.tool-search input:focus {
  border-color: var(--primary);
  box-shadow: var(--shadow-focus);
}
.tool-count {
  font-size: var(--text-sm);
  color: var(--text-tertiary);
  white-space: nowrap;
}
.tool-table {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
}
.tool-row {
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-lg);
  padding: var(--space-3) var(--space-4);
  background: var(--surface-2);
  transition: border-color var(--dur-fast) var(--ease-out),
    background-color var(--dur-fast) var(--ease-out);
}
.tool-row:hover {
  border-color: var(--border-default);
  background: var(--surface-3);
}
.tool-row-main {
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
}
.tool-name {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  flex-wrap: wrap;
}
.tool-name strong {
  font-size: var(--text-base);
  font-weight: var(--weight-medium);
  color: var(--text-primary);
}
.tool-name code {
  font-size: var(--text-xs);
}
.type-badge {
  font-size: var(--text-xs);
  padding: 1px var(--space-2);
  border-radius: var(--radius-full);
  background: var(--primary-soft);
  color: var(--primary-hover);
  border: 1px solid var(--border-default);
}
.type-badge.builtin {
  background: var(--surface-3);
  color: var(--text-tertiary);
}
.tool-desc {
  font-size: var(--text-sm);
  color: var(--text-tertiary);
  margin: 0;
  line-height: var(--leading-normal);
}
.tool-meta {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  margin-top: var(--space-1);
}
.status-badge {
  font-size: var(--text-xs);
  padding: 1px var(--space-2);
  border-radius: var(--radius-full);
  background: var(--success-soft);
  color: var(--success-text);
  border: 1px solid var(--border-default);
}
.status-badge.off {
  background: var(--surface-3);
  color: var(--text-tertiary);
  border-color: var(--border-default);
}
.tool-row-side {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-top: var(--space-3);
  gap: var(--space-3);
  flex-wrap: wrap;
}
.tool-actions {
  display: flex;
  gap: var(--space-2);
}
.link-btn {
  display: inline-flex;
  align-items: center;
  gap: var(--space-1);
  background: none;
  border: none;
  color: var(--primary-hover);
  cursor: pointer;
  font-size: var(--text-sm);
  font-weight: var(--weight-medium);
  padding: 0;
  transition: color var(--dur-fast) var(--ease-out);
}
.link-btn .icon {
  transition: transform var(--dur-base) var(--ease-out);
}
.link-btn:hover:not(:disabled) {
  color: var(--primary);
}
.link-btn:disabled {
  color: var(--text-disabled);
  cursor: not-allowed;
}
.tool-agents {
  margin-top: var(--space-3);
  padding-top: var(--space-3);
  border-top: 1px solid var(--border-subtle);
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2);
}
.agent-chip {
  font-size: var(--text-xs);
  padding: 1px var(--space-2);
  border-radius: var(--radius-full);
  background: var(--surface-3);
  color: var(--text-secondary);
  border: 1px solid var(--border-subtle);
}
</style>
