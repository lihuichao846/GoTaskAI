    import re

with open('public/index.html', 'r', encoding='utf-8') as f:
    content = f.read()

# 1. Restore body and app div
content = content.replace('<body class="bg-gray-50 h-screen font-sans text-gray-800 overflow-hidden">', '<body class="bg-gray-50 min-h-screen font-sans text-gray-800">')
content = content.replace('<div id="app" class="container mx-auto px-4 py-6 max-w-7xl h-full flex flex-col">', '<div id="app" class="container mx-auto px-4 py-8 max-w-6xl">')
content = content.replace('<header class="mb-6 flex items-center justify-between shrink-0">', '<header class="mb-8 flex items-center justify-between">')

# 2. Restore main grid
main_start_pattern = r'<main v-else class="flex flex-col flex-1 overflow-hidden">.*?<!-- Task Mode UI \(Old Main\) -->\s*<div v-show="currentTab === \'tasks\'" class="grid grid-cols-1 md:grid-cols-3 gap-6 h-full overflow-y-auto custom-scrollbar pb-6 pr-2">'
content = re.sub(main_start_pattern, '<main v-else class="grid grid-cols-1 md:grid-cols-3 gap-6">', content, flags=re.DOTALL)

# 3. Replace the task list rendering
task_list_pattern = r'<div v-else class="space-y-4 max-h-\[600px\] overflow-y-auto pr-2">.*?<!-- Chat Mode UI -->'
new_task_list = r'''<div v-else class="space-y-4 max-h-[800px] overflow-y-auto pr-2 custom-scrollbar">
                    <div v-for="group in groupedConversations" :key="group.id" class="border border-gray-200 rounded-md p-4 relative overflow-hidden transition-all hover:shadow-md bg-white" :class="getBorderColor(group.latest_task.status)">
                        <div class="flex justify-between items-start mb-2">
                            <div class="flex items-center group">
                                <span class="text-xs font-mono bg-gray-100 text-gray-600 px-2 py-1 rounded mr-2" :title="group.id">
                                    {{ group.id.substring(0, 8) }}...
                                </span>
                                <span class="text-sm font-semibold capitalize">{{ group.latest_task.type }}</span>
                                <span class="ml-2 text-[10px] bg-blue-50 text-blue-600 px-2 py-0.5 rounded-full border border-blue-100">对话数: {{ group.tasks.length }}</span>
                            </div>
                            <div class="flex items-center space-x-2">
                                <span :class="getStatusBadgeClass(group.latest_task.status)" class="text-xs px-2 py-1 rounded-full font-medium capitalize">
                                    {{ group.latest_task.status }}
                                </span>
                                <!-- 继续对话按钮 -->
                                <button @click="openChat(group.id)" class="text-blue-500 hover:text-blue-700 transition-colors flex items-center text-sm ml-2 bg-blue-50 px-2 py-1 rounded hover:bg-blue-100" title="打开对话窗口">
                                    <svg class="w-4 h-4 mr-1" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 10h.01M12 10h.01M16 10h.01M9 16H5a2 2 0 01-2-2V6a2 2 0 012-2h14a2 2 0 012 2v8a2 2 0 01-2 2h-5l-5 5v-5z"></path></svg>
                                    继续对话
                                </button>
                                <!-- 删除按钮 -->
                                <button @click="deleteSession(group.id)" class="text-gray-400 hover:text-red-500 transition-colors ml-2" title="删除整个对话">
                                    <svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16"></path></svg>
                                </button>
                            </div>
                        </div>

                        <div class="text-sm text-gray-600 mb-3 line-clamp-2">
                            <strong>首次输入:</strong> {{ group.first_payload }}
                        </div>

                        <div v-if="group.latest_task.result" class="text-sm text-green-700 bg-green-50 p-2 rounded mb-2 border border-green-100 line-clamp-3">
                            <strong>最新结果:</strong> {{ group.latest_task.result }}
                        </div>

                        <div v-if="group.latest_task.error" class="text-sm text-red-700 bg-red-50 p-2 rounded mb-2 border border-red-100 line-clamp-2">
                            <strong>最新错误:</strong> {{ group.latest_task.error }}
                        </div>

                        <div class="flex justify-between text-xs text-gray-400 mt-2">
                            <span>最后更新: {{ formatDate(group.updated_at) }}</span>
                        </div>

                        <!-- 进度条 -->
                        <div v-if="['pending', 'processing'].includes(group.latest_task.status)" class="absolute bottom-0 left-0 right-0 h-1 bg-gray-100">
                            <div class="h-full progress-bar-transition" 
                                 :class="group.latest_task.status === 'processing' ? 'bg-blue-500 w-full' : 'bg-gray-300 w-1/4'"
                                 :style="group.latest_task.status === 'processing' ? 'animation: progress 2s ease-in-out infinite' : ''">
                            </div>
                        </div>
                    </div>
                </div>
            </div>
        </main>

        <!-- Chat Modal -->
        <div v-if="showChatModal" class="fixed inset-0 bg-black bg-opacity-50 z-50 flex items-center justify-center p-4">
            <div class="bg-white rounded-xl shadow-2xl w-full max-w-4xl h-[85vh] flex flex-col overflow-hidden">
                <!-- Header -->
                <div class="p-4 border-b border-gray-200 flex justify-between items-center bg-gray-50 shrink-0">
                    <h2 class="text-lg font-bold text-gray-800 flex items-center">
                        <svg class="w-5 h-5 mr-2 text-blue-600" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 10h.01M12 10h.01M16 10h.01M9 16H5a2 2 0 01-2-2V6a2 2 0 012-2h14a2 2 0 012 2v8a2 2 0 01-2 2h-5l-5 5v-5z"></path></svg>
                        沉浸式连续对话
                        <span class="ml-3 text-xs font-mono bg-gray-200 text-gray-600 px-2 py-1 rounded">Session: {{ activeSessionId.startsWith('new_') ? '新会话' : activeSessionId }}</span>
                    </h2>
                    <button @click="showChatModal = false" class="text-gray-400 hover:text-gray-600 transition-colors p-1 rounded-lg hover:bg-gray-200">
                        <svg class="w-6 h-6" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12"></path></svg>
                    </button>
                </div>
                
                <!-- System Prompt -->
                <div class="border-b border-gray-100 p-2 bg-white flex items-center gap-2 text-xs text-gray-500 shrink-0 px-4">
                    <label class="whitespace-nowrap font-medium">系统设定(System Prompt):</label>
                    <input v-model="chatSystemPrompt" type="text" class="flex-1 bg-gray-50 border border-gray-200 rounded px-2 py-1.5 focus:ring-1 focus:ring-blue-500 focus:border-blue-500" placeholder="例如：你是一个幽默的助手...">
                </div>

                <!-- Chat Messages -->
                <div id="chat-messages-container" class="flex-1 overflow-y-auto p-6 space-y-6 custom-scrollbar bg-gray-50">
                    <div v-for="task in activeSessionTasks" :key="task.id" class="flex flex-col gap-6">
                        <div class="flex justify-end">
                            <div class="bg-blue-600 text-white p-3.5 rounded-2xl rounded-tr-sm max-w-[85%] shadow-sm text-sm whitespace-pre-wrap leading-relaxed">{{ task.payload }}</div>
                        </div>
                        <div class="flex justify-start">
                            <div class="bg-white text-gray-800 p-3.5 rounded-2xl rounded-tl-sm max-w-[85%] shadow-sm text-sm whitespace-pre-wrap border border-gray-200 relative group leading-relaxed">
                                <button @click="copyContent(task.result)" v-if="task.result" class="absolute -right-8 top-1 text-gray-400 hover:text-blue-500 opacity-0 group-hover:opacity-100 transition-opacity bg-white p-1 rounded shadow-sm border border-gray-100" title="复制">
                                    <svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z"></path></svg>
                                </button>
                                <div v-if="task.status === 'processing'" class="flex items-center text-blue-600 font-medium">
                                    <svg class="animate-spin h-4 w-4 mr-2" xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"></path></svg>
                                    AI 思考中...
                                </div>
                                <div v-else-if="task.status === 'failed'" class="text-red-600">
                                    ❌ 失败: {{ task.error }}
                                    <button @click="retryTask(task.id)" class="ml-2 underline text-xs">重试</button>
                                </div>
                                <div v-else-if="task.result">{{ task.result }}</div>
                                <div v-else class="text-gray-500">等待调度...</div>
                            </div>
                        </div>
                    </div>
                </div>
                
                <!-- Chat Input -->
                <div class="p-4 bg-white border-t border-gray-200 shrink-0">
                    <div class="relative flex items-end gap-2">
                        <textarea v-model="chatInput" @keydown.enter.exact.prevent="sendChatMessage" rows="3" class="flex-1 border-gray-300 rounded-xl shadow-inner border p-3 focus:ring-2 focus:ring-blue-500 focus:border-blue-500 text-sm custom-scrollbar bg-gray-50 resize-none" placeholder="输入消息... (Enter 发送，Shift+Enter 换行)"></textarea>
                        <button @click="sendChatMessage" :disabled="!chatInput.trim() || isChatSubmitting" class="bg-blue-600 hover:bg-blue-700 text-white h-12 w-12 flex items-center justify-center rounded-xl transition-colors disabled:opacity-50 disabled:cursor-not-allowed shadow-sm mb-1">
                            <svg v-if="isChatSubmitting" class="animate-spin w-5 h-5" xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"></path></svg>
                            <svg v-else class="w-5 h-5 transform rotate-90" fill="currentColor" viewBox="0 0 20 20"><path d="M10.894 2.553a1 1 0 00-1.788 0l-7 14a1 1 0 001.169 1.409l5-1.429A1 1 0 009 15.571V11a1 1 0 112 0v4.571a1 1 0 00.725.962l5 1.428a1 1 0 001.17-1.408l-7-14z"></path></svg>
                        </button>
                    </div>
                </div>
            </div>
        </div>
        <!-- Chat Mode UI ends here -->'''
content = re.sub(task_list_pattern, new_task_list, content, flags=re.DOTALL)

# 4. Remove the old Chat Mode UI block
chat_mode_pattern = r'<!-- Chat Mode UI -->.*?</div>\s*</div>\s*</main>'
content = re.sub(chat_mode_pattern, '</main>', content, flags=re.DOTALL)

# 5. Fix Vue state: remove currentTab, add showChatModal
content = content.replace("const currentTab = ref('chat'); // 'chat' 或 'tasks'", "const showChatModal = ref(false);")
content = content.replace("currentTab,", "showChatModal,")

# 6. Update openChat function to also open modal
open_chat_func = r'''const openChat = \(sessionId\) => \{
                    activeSessionId\.value = sessionId;
                    const group = groupedConversations\.value\.find\(g => g\.id === sessionId\);
                    if \(group && group\.tasks\.length > 0\) \{
                        // 尝试恢复 System Prompt
                        const sysPrompt = group\.tasks\[group\.tasks\.length - 1\]\.system_prompt;
                        if \(sysPrompt\) chatSystemPrompt\.value = sysPrompt;
                    \}
                    scrollToBottom\(\);
                \};'''
new_open_chat_func = '''const openChat = (sessionId) => {
                    activeSessionId.value = sessionId;
                    const group = groupedConversations.value.find(g => g.id === sessionId);
                    if (group && group.tasks.length > 0) {
                        // 尝试恢复 System Prompt
                        const sysPrompt = group.tasks[group.tasks.length - 1].system_prompt;
                        if (sysPrompt) chatSystemPrompt.value = sysPrompt;
                    }
                    showChatModal.value = true;
                    scrollToBottom();
                };'''
content = re.sub(open_chat_func, new_open_chat_func, content, flags=re.DOTALL)

with open('public/index.html', 'w', encoding='utf-8') as f:
    f.write(content)