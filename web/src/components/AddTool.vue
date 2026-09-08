<script setup>
import { ref } from 'vue'
import { searchMCPServers } from '../api/client'
import ToolFormModal from './ToolFormModal.vue'

const props = defineProps({
  token: { type: String, default: '' }
})

const emit = defineEmits(['message', 'added'])

const sourceTab = ref('discover')

// 发现 MCP
const searchQuery = ref('')
const searching = ref(false)
const discoveryItems = ref([])
const parsedIntent = ref(null)

// 官方预置：飞书
const feishuForm = ref({ app_id: '', app_secret: '', use_user: false, display_name: '飞书' })

const modalOpen = ref(false)
const modalInitial = ref({})

async function searchMCP() {
  const q = searchQuery.value.trim()
  if (!q) {
    emit('message', '请输入自然语言描述，例如「一个能查天气的 mcp 工具」', 'error')
    return
  }
  searching.value = true
  discoveryItems.value = []
  parsedIntent.value = null
  try {
    const res = await searchMCPServers(props.token, q)
    discoveryItems.value = res.items || []
    parsedIntent.value = res.parsed || null
    emit('message', `找到 ${discoveryItems.value.length} 个候选工具`, 'success')
  } catch (e) {
    emit('message', `搜索失败：${e.message}`, 'error')
  } finally {
    searching.value = false
  }
}

// 将候选工具填入注册表单
function useCandidate(item) {
  modalInitial.value = {
    name: item.name,
    display_name: item.display_name || item.name,
    description: item.description || '',
    config: JSON.stringify(item.config || {})
  }
  modalOpen.value = true
}

// 一键接入飞书：先构建连接配置，再交由表单确认
function openFeishu() {
  const appId = feishuForm.value.app_id.trim()
  const appSecret = feishuForm.value.app_secret.trim()
  if (!appId || !appSecret) {
    emit('message', '请填写飞书 App ID 与 App Secret', 'error')
    return
  }
  const args = ['-y', '@larksuiteoapi/lark-mcp', 'mcp', '-a', appId, '-s', appSecret, '-t', 'preset.light']
  if (feishuForm.value.use_user) {
    args.push('--oauth', '--token-mode', 'user_access_token')
  }
  modalInitial.value = {
    name: 'lark_mcp',
    display_name: feishuForm.value.display_name.trim() || '飞书',
    description: '操作飞书消息、云文档、多维表格、日历等',
    config: JSON.stringify({ command: 'npx', args })
  }
  modalOpen.value = true
}

function openManual() {
  modalInitial.value = { name: '', display_name: '', description: '', config: '' }
  modalOpen.value = true
}

function onSaved() {
  emit('added', true)
  emit('message', '工具已添加，可在「工具列表」查看', 'success')
}
</script>

<template>
  <div>
    <div class="source-tabs">
      <button class="source-tab" :class="{ active: sourceTab === 'discover' }" @click="sourceTab = 'discover'">
        发现 MCP
      </button>
      <button class="source-tab" :class="{ active: sourceTab === 'preset' }" @click="sourceTab = 'preset'">
        官方预置
      </button>
      <button class="source-tab" :class="{ active: sourceTab === 'manual' }" @click="sourceTab = 'manual'">
        手动接入
      </button>
    </div>

    <!-- 发现 MCP -->
    <div v-if="sourceTab === 'discover'" class="source-pane">
      <p class="source-help">用自然语言描述需求，AI 会解析并检索 GitHub / npm 上相关的 MCP 工具。</p>
      <div class="discover-form">
        <input v-model="searchQuery" type="text" placeholder="例如：一个能查天气的 mcp 工具" @keyup.enter="searchMCP" />
        <button class="primary-btn" :disabled="searching" @click="searchMCP">
          {{ searching ? '检索中...' : '查找工具' }}
        </button>
      </div>

      <div v-if="parsedIntent" class="source-intent">解析意图：关键词「{{ parsedIntent.keywords }}」</div>

      <div v-if="discoveryItems.length" class="discover-results">
        <div v-for="(item, idx) in discoveryItems" :key="idx" class="discover-item">
          <div class="discover-item-head">
            <strong>{{ item.display_name }}</strong>
            <span class="badge">{{ item.source }}</span>
            <span v-if="item.stars" class="stars">★ {{ item.stars }}</span>
          </div>
          <p class="discover-item-desc">{{ item.description }}</p>
          <code v-if="item.install_command" class="install-cmd">{{ item.install_command }}</code>
          <a v-if="item.repo_url" :href="item.repo_url" target="_blank" rel="noopener" class="repo-link">查看仓库</a>
          <button class="primary-btn" @click="useCandidate(item)">使用此工具</button>
        </div>
      </div>
      <div v-else-if="searching" class="empty-state">正在解析并检索，请稍候...</div>
    </div>

    <!-- 官方预置 -->
    <div v-else-if="sourceTab === 'preset'" class="source-pane">
      <p class="source-help">填写飞书开放平台自建应用的 App ID 与 App Secret，一键接入官方飞书 MCP（国内版）。</p>
      <div class="auth-form">
        <label>
          <span>App ID</span>
          <input v-model="feishuForm.app_id" type="text" placeholder="cli_xxxxxxxxxxxxxxxx" />
        </label>
        <label>
          <span>App Secret</span>
          <input v-model="feishuForm.app_secret" type="password" placeholder="应用密钥" autocomplete="off" />
        </label>
        <label class="checkbox-row">
          <input v-model="feishuForm.use_user" type="checkbox" />
          <span>使用用户身份（需已执行 lark-mcp login，可操作个人数据）</span>
        </label>
        <label>
          <span>显示名称</span>
          <input v-model="feishuForm.display_name" type="text" placeholder="飞书" />
        </label>
        <button class="primary-btn" @click="openFeishu">接入飞书 MCP</button>
      </div>
    </div>

    <!-- 手动接入 -->
    <div v-else class="source-pane">
      <p class="source-help">手动填写 MCP 工具名称与连接配置，适用于不便于自动发现或飞书预置的场景。</p>
      <button class="primary-btn" @click="openManual">开始手动配置</button>
    </div>

    <ToolFormModal
      :token="token"
      :open="modalOpen"
      mode="create"
      :initial="modalInitial"
      @close="modalOpen = false"
      @saved="onSaved"
      @message="($event, $type) => emit('message', $event, $type)"
    />
  </div>
</template>

<style scoped>
.source-tabs {
  display: flex;
  gap: 0.5rem;
  margin-bottom: 1rem;
}
.source-tab {
  border: 1px solid var(--border, #e2e2e2);
  background: #fff;
  padding: 0.4rem 0.9rem;
  border-radius: 999px;
  cursor: pointer;
  font-size: 0.85rem;
  color: var(--muted, #666);
}
.source-tab.active {
  background: var(--accent, #4f6ef7);
  border-color: var(--accent, #4f6ef7);
  color: #fff;
}
.source-pane {
  display: flex;
  flex-direction: column;
  gap: 0.8rem;
}
.source-help {
  color: var(--muted, #8c8c8c);
  font-size: 0.8rem;
  margin: 0;
}
.discover-form {
  display: flex;
  gap: 0.5rem;
}
.discover-form input {
  flex: 1;
}
.source-intent {
  font-size: 0.82rem;
  color: var(--muted, #8c8c8c);
}
.discover-results {
  display: flex;
  flex-direction: column;
  gap: 0.6rem;
}
.discover-item {
  background: var(--bg-soft, #f6f7f9);
  border: 1px solid var(--border, #e2e2e2);
  border-radius: 8px;
  padding: 0.6rem 0.7rem;
}
.discover-item-head {
  display: flex;
  align-items: center;
  gap: 0.5rem;
}
.badge {
  font-size: 0.72rem;
  padding: 0.05rem 0.4rem;
  border-radius: 999px;
  background: var(--accent, #4f6ef7);
  color: #fff;
}
.stars {
  font-size: 0.75rem;
  color: var(--muted, #8c8c8c);
}
.discover-item-desc {
  font-size: 0.82rem;
  color: var(--muted, #6b6b6b);
  margin: 0.35rem 0;
}
.install-cmd {
  font-size: 0.78rem;
  display: block;
  background: #eef0f3;
  border-radius: 6px;
  padding: 0.25rem 0.4rem;
  overflow-x: auto;
}
.repo-link {
  font-size: 0.78rem;
  margin-right: 0.6rem;
}
.checkbox-row {
  display: flex;
  align-items: center;
  gap: 0.5rem;
}
</style>
