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
  gap: var(--space-2);
  margin-bottom: var(--space-4);
}
.source-tab {
  border: 1px solid var(--border-default);
  background: var(--surface-2);
  padding: var(--space-2) var(--space-4);
  border-radius: var(--radius-full);
  cursor: pointer;
  font-size: var(--text-sm);
  font-weight: var(--weight-medium);
  color: var(--text-secondary);
  transition: color var(--dur-fast) var(--ease-out),
    border-color var(--dur-fast) var(--ease-out),
    background-color var(--dur-fast) var(--ease-out);
}
.source-tab:hover {
  color: var(--text-primary);
  border-color: var(--border-strong);
}
.source-tab.active {
  background: var(--primary);
  border-color: var(--primary);
  color: var(--primary-contrast);
}
.source-pane {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}
.source-help {
  color: var(--text-tertiary);
  font-size: var(--text-sm);
  line-height: var(--leading-relaxed);
  margin: 0;
}
.discover-form {
  display: flex;
  gap: var(--space-2);
}
.discover-form input {
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
.discover-form input::placeholder {
  color: var(--text-disabled);
}
.discover-form input:focus {
  border-color: var(--primary);
  box-shadow: var(--shadow-focus);
}
.source-intent {
  font-size: var(--text-sm);
  color: var(--text-tertiary);
}
.discover-results {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}
.discover-item {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: var(--space-2);
  background: var(--surface-2);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-lg);
  padding: var(--space-4);
  transition: border-color var(--dur-fast) var(--ease-out);
}
.discover-item:hover {
  border-color: var(--border-default);
}
.discover-item-head {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  flex-wrap: wrap;
  width: 100%;
}
.discover-item-head strong {
  font-size: var(--text-base);
  font-weight: var(--weight-medium);
  color: var(--text-primary);
}
.badge {
  font-size: var(--text-xs);
  padding: 1px var(--space-2);
  border-radius: var(--radius-full);
  background: var(--primary-soft);
  color: var(--primary-hover);
  border: 1px solid var(--border-default);
}
.stars {
  font-size: var(--text-xs);
  color: var(--text-tertiary);
}
.discover-item-desc {
  font-size: var(--text-sm);
  color: var(--text-secondary);
  margin: 0;
  line-height: var(--leading-normal);
}
.install-cmd {
  font-size: var(--text-xs);
  display: block;
  width: 100%;
  background: var(--bg-2);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  padding: var(--space-2);
  overflow-x: auto;
  color: var(--text-secondary);
}
.repo-link {
  font-size: var(--text-sm);
  color: var(--primary-hover);
  transition: color var(--dur-fast) var(--ease-out);
}
.repo-link:hover {
  color: var(--primary);
  text-decoration: underline;
  text-underline-offset: 3px;
}
.checkbox-row {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  flex-direction: row;
}
</style>
