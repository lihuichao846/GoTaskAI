<script setup>
import { computed, onMounted, ref } from 'vue'
import {
  bindAgentKnowledgeBases,
  bindAgentTools,
  createAgent,
  deleteAgent,
  listAgentKnowledgeBases,
  listAgentTools,
  listAgents,
  listKnowledgeBases,
  listTools,
  runAgent,
  updateAgent
} from '../api/client'

const props = defineProps({
  token: { type: String, default: '' }
})

const emit = defineEmits(['message', 'agent-run'])

const agents = ref([])
const loading = ref(false)
const selectedId = ref('')
const form = ref({ name: '', system_prompt: '', model: '', api_key: '', base_url: '' })
const runPayload = ref('')
const running = ref(false)

const allTools = ref([])
const allKbs = ref([])
const boundToolIds = ref([])
const boundKbIds = ref([])

const selected = computed(() => agents.value.find((a) => a.id === selectedId.value) || null)
const isNew = computed(() => !selectedId.value)

async function refresh() {
  loading.value = true
  try {
    agents.value = await listAgents(props.token)
    if (!selectedId.value && agents.value.length > 0) {
      selectAgent(agents.value[0].id)
    }
  } catch (e) {
    emit('message', `加载 Agent 失败：${e.message}`, 'error')
  } finally {
    loading.value = false
  }
}

async function loadOptions() {
  try {
    const [tools, kbs] = await Promise.all([
      listTools(props.token),
      listKnowledgeBases(props.token)
    ])
    allTools.value = tools || []
    allKbs.value = kbs || []
  } catch (e) {
    emit('message', `加载工具/知识库失败：${e.message}`, 'error')
  }
}

async function loadBindings(agentId) {
  try {
    const [tools, kbs] = await Promise.all([
      listAgentTools(props.token, agentId),
      listAgentKnowledgeBases(props.token, agentId)
    ])
    boundToolIds.value = (tools || []).map((t) => t.id)
    boundKbIds.value = (kbs || []).map((k) => k.id)
  } catch (e) {
    boundToolIds.value = []
    boundKbIds.value = []
  }
}

async function selectAgent(id) {
  selectedId.value = id
  const a = agents.value.find((x) => x.id === id)
  if (a) {
    form.value = {
      name: a.name,
      system_prompt: a.system_prompt || '',
      model: a.model || '',
      api_key: a.api_key || '',
      base_url: a.base_url || ''
    }
    await loadBindings(id)
  }
}

function newAgent() {
  selectedId.value = ''
  form.value = { name: '', system_prompt: '', model: '', api_key: '', base_url: '' }
  boundToolIds.value = []
  boundKbIds.value = []
}

async function save() {
  if (!form.value.name.trim()) {
    emit('message', 'Agent 名称不能为空', 'error')
    return
  }
  try {
    let agentId = selectedId.value
    if (isNew.value) {
      const data = await createAgent(props.token, form.value)
      agentId = data?.agent?.id || ''
      selectedId.value = agentId
    } else {
      await updateAgent(props.token, selectedId.value, form.value)
    }

    if (agentId) {
      await bindAgentTools(props.token, agentId, boundToolIds.value)
      await bindAgentKnowledgeBases(props.token, agentId, boundKbIds.value)
    }

    emit('message', 'Agent 保存成功', 'success')
    await refresh()
  } catch (e) {
    emit('message', `保存失败：${e.message}`, 'error')
  }
}

async function remove() {
  if (!selected.value) return
  if (!window.confirm(`确定删除 Agent「${selected.value.name}」吗？`)) return
  try {
    await deleteAgent(props.token, selected.value.id)
    selectedId.value = ''
    form.value = { name: '', system_prompt: '', model: '', api_key: '', base_url: '' }
    boundToolIds.value = []
    boundKbIds.value = []
    emit('message', 'Agent 已删除', 'success')
    await refresh()
  } catch (e) {
    emit('message', `删除失败：${e.message}`, 'error')
  }
}

async function run() {
  if (!selected.value || !runPayload.value.trim()) return
  running.value = true
  try {
    const data = await runAgent(props.token, selected.value.id, { payload: runPayload.value.trim() })
    runPayload.value = ''
    emit('message', `Agent 已运行，任务 ID：${data.id}`, 'success')
    emit('agent-run')
  } catch (e) {
    emit('message', `运行失败：${e.message}`, 'error')
  } finally {
    running.value = false
  }
}

onMounted(async () => {
  await loadOptions()
  await refresh()
})
</script>

<template>
  <section class="card">
    <div class="panel-head">
      <div class="panel-title-block">
        <h2>Agent 管理</h2>
        <p>创建并配置智能体，绑定工具与知识库后运行进入异步任务队列。</p>
      </div>
      <button class="primary-btn" @click="newAgent">新建 Agent</button>
    </div>

    <div class="agent-layout">
      <aside class="agent-list">
        <div v-if="loading" class="empty-state">加载中...</div>
        <div v-else-if="agents.length === 0" class="empty-state">暂无 Agent，点击「新建 Agent」开始</div>
        <button
          v-for="agent in agents"
          :key="agent.id"
          class="agent-item"
          :class="{ 'agent-item-active': agent.id === selectedId }"
          @click="selectAgent(agent.id)"
        >
          <strong>{{ agent.name }}</strong>
          <span>{{ agent.model || '默认模型' }}</span>
        </button>
      </aside>

      <div class="agent-editor">
        <h3>{{ isNew ? '新建 Agent' : selected?.name || '编辑 Agent' }}</h3>

        <div class="auth-form">
          <label>
            <span>名称</span>
            <input v-model="form.name" type="text" placeholder="例如：客服助手" />
          </label>
          <label>
            <span>系统提示词 (System Prompt)</span>
            <textarea v-model="form.system_prompt" rows="5" placeholder="设定 Agent 的角色与行为" />
          </label>
          <label>
            <span>模型（留空使用平台默认）</span>
            <input v-model="form.model" type="text" placeholder="例如：qwen2.5:latest" />
          </label>
          <label>
            <span>API Key（留空使用平台默认）</span>
            <input v-model="form.api_key" type="password" placeholder="例如：sk-xxxxxxxx" autocomplete="off" />
          </label>
          <label>
            <span>接口地址 Base URL（留空使用平台默认）</span>
            <input v-model="form.base_url" type="text" placeholder="例如：https://api.openai.com/v1" />
          </label>

          <div class="agent-actions">
            <button class="primary-btn" @click="save">{{ isNew ? '创建' : '保存' }}</button>
            <button v-if="!isNew" class="secondary-btn" @click="remove">删除</button>
          </div>
        </div>

        <div class="agent-bindings">
          <h3>工具</h3>
          <div v-if="allTools.length === 0" class="empty-state">暂无可用工具</div>
          <label v-for="tool in allTools" :key="tool.id" class="bind-check">
            <input type="checkbox" :value="tool.id" v-model="boundToolIds" />
            <span>{{ tool.display_name || tool.name }}</span>
            <em>{{ tool.is_builtin ? '内置' : '私有' }}</em>
          </label>

          <h3>知识库</h3>
          <div v-if="allKbs.length === 0" class="empty-state">暂无知识库</div>
          <label v-for="kb in allKbs" :key="kb.id" class="bind-check">
            <input type="checkbox" :value="kb.id" v-model="boundKbIds" />
            <span>{{ kb.name }}</span>
            <em>{{ kb.type }}</em>
          </label>
        </div>

        <div v-if="!isNew" class="agent-run">
          <h3>运行 Agent</h3>
          <textarea v-model="runPayload" rows="3" placeholder="输入要发送给 Agent 的内容" />
          <button class="primary-btn" :disabled="running || !runPayload.trim()" @click="run">
            {{ running ? '运行中...' : '运行' }}
          </button>
        </div>
      </div>
    </div>
  </section>
</template>
