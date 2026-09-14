<script setup>
import { ref } from 'vue'
import AddTool from './AddTool.vue'
import ToolList from './ToolList.vue'

const props = defineProps({
  token: { type: String, default: '' }
})

const emit = defineEmits(['message'])

const activeTab = ref('list')

function onAdded() {
  // 添加完成后切换到工具列表，便于查看新工具。
  activeTab.value = 'list'
}
</script>

<template>
  <section class="card">
    <div class="panel-head">
      <div class="panel-title-block">
        <h2>工具管理</h2>
        <p>管理内置工具与 MCP 外部工具，供 Agent 按需绑定调用。</p>
      </div>
    </div>

    <div class="tool-tabs">
      <button class="tool-tab" :class="{ active: activeTab === 'list' }" @click="activeTab = 'list'">
        工具列表
        <em>查看/编辑/启停</em>
      </button>
      <button class="tool-tab" :class="{ active: activeTab === 'add' }" @click="activeTab = 'add'">
        添加工具
        <em>发现 / 预置 / 手动</em>
      </button>
    </div>

    <ToolList v-if="activeTab === 'list'" :token="token" @message="($event, $type) => emit('message', $event, $type)" />
    <AddTool v-else :token="token" @message="($event, $type) => emit('message', $event, $type)" @added="onAdded" />
  </section>
</template>

<style scoped>
.tool-tabs {
  display: flex;
  gap: var(--space-1);
  margin-bottom: var(--space-5);
  border-bottom: 1px solid var(--border-subtle);
}
.tool-tab {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 2px;
  background: none;
  border: none;
  border-bottom: 2px solid transparent;
  cursor: pointer;
  font-size: var(--text-base);
  font-weight: var(--weight-medium);
  padding: var(--space-2) var(--space-4);
  color: var(--text-tertiary);
  transition: color var(--dur-fast) var(--ease-out),
    border-color var(--dur-fast) var(--ease-out);
}
.tool-tab:hover {
  color: var(--text-secondary);
}
.tool-tab em {
  font-style: normal;
  font-size: var(--text-xs);
  color: var(--text-disabled);
}
.tool-tab.active {
  color: var(--primary-hover);
  border-bottom-color: var(--primary);
}
.tool-tab.active em {
  color: var(--text-tertiary);
}
</style>
