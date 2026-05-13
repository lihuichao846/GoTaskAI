import re

def update_index_html():
    with open("public/index.html", "r", encoding="utf-8") as f:
        html = f.read()

    # 1. 替换右侧面板
    old_right_panel_start = html.find('<!-- 右侧：任务列表 -->')
    old_right_panel_end = html.find('</main>')
    
    if old_right_panel_start == -1 or old_right_panel_end == -1:
        print("Failed to find right panel")
        return

    new_right_panel = """<!-- 右侧：会话与聊天面板 -->
            <div class="lg:col-span-7 xl:col-span-8 bg-white p-0 rounded-xl shadow-sm border border-gray-200 flex flex-col h-[750px] overflow-hidden">
                
                <!-- 视图 1：会话列表 (折叠状态) -->
                <div v-if="!activeSessionId" class="flex flex-col h-full bg-gray-50/50">
                    <div class="p-5 border-b border-gray-200 flex justify-between items-center bg-white z-10 shadow-sm">
                        <h2 class="text-lg font-bold text-gray-800 flex items-center">
                            <svg class="w-5 h-5 mr-2 text-blue-500" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M17 8h2a2 2 0 012 2v6a2 2 0 01-2 2h-2v4l-4-4H9a1.994 1.994 0 01-1.414-.586m0 0L11 14h4a2 2 0 002-2V6a2 2 0 00-2-2H5a2 2 0 00-2 2v6a2 2 0 002 2h2v4l.586-.586z"></path></svg>
                            对话会话大厅
                            <span class="ml-3 bg-blue-50 text-blue-600 text-xs py-0.5 px-2.5 rounded-full border border-blue-100 font-medium">{{ groupedConversations.length }} 个历史会话</span>
                        </h2>
                        <button @click="fetchTasks" class="text-sm text-gray-600 hover:text-blue-600 flex items-center bg-gray-50 px-3 py-1.5 rounded-lg border border-gray-200 transition-colors shadow-sm hover:bg-white">
                            <svg class="w-4 h-4 mr-1.5" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"></path></svg>
                            刷新
                        </button>
                    </div>
                    
                    <div class="flex-1 overflow-y-auto p-5 space-y-4 custom-scrollbar">
                        <div v-if="groupedConversations.length === 0" class="flex flex-col items-center justify-center h-full text-gray-400">
                            <svg class="w-16 h-16 mb-4 text-gray-200" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M8 12h.01M12 12h.01M16 12h.01M21 12c0 4.418-4.03 8-9 8a9.863 9.863 0 01-4.255-.949L3 20l1.395-3.72C3.512 15.042 3 13.574 3 12c0-4.418 4.03-8 9-8s9 3.582 9 8z"></path></svg>
                            <p>暂无对话，请在左侧提交新任务</p>
                        </div>
                        
                        <!-- 会话卡片 -->
                        <div v-for="group in groupedConversations" :key="group.id" @click="openChat(group.id)" class="bg-white border border-gray-200 rounded-xl p-5 cursor-pointer hover:shadow-md hover:border-blue-300 transition-all flex flex-col gap-3 relative group">
                            <div class="flex justify-between items-center">
                                <div class="flex items-center gap-2">
                                    <span class="text-xs font-mono bg-gray-100 text-gray-600 px-2 py-1 rounded">Session: {{ group.id.substring(0,8) }}...</span>
                                    <span class="text-xs bg-indigo-50 text-indigo-600 px-2 py-1 rounded-full border border-indigo-100 font-medium">{{ group.tasks.length }} 条上下文</span>
                                </div>
                                <span class="text-xs text-gray-400">{{ formatDate(group.updated_at) }}</span>
                            </div>
                            <div class="text-sm text-gray-700 font-medium line-clamp-2 leading-relaxed bg-gray-50/50 p-2 rounded">{{ group.first_payload }}</div>
                            <div class="flex items-center justify-between text-xs mt-1">
                                <div class="flex items-center gap-2">
                                    <span class="text-gray-500">最新状态:</span>
                                    <span :class="getStatusBadgeClass(group.latest_status)" class="capitalize font-bold flex items-center">
                                        <span v-if="group.latest_status === 'processing'" class="w-1.5 h-1.5 rounded-full bg-blue-500 mr-1.5 animate-ping"></span>
                                        <span v-else-if="group.latest_status === 'completed'" class="w-1.5 h-1.5 rounded-full bg-green-500 mr-1.5"></span>
                                        <span v-else-if="group.latest_status === 'failed'" class="w-1.5 h-1.5 rounded-full bg-red-500 mr-1.5"></span>
                                        <span v-else class="w-1.5 h-1.5 rounded-full bg-gray-400 mr-1.5"></span>
                                        {{ group.latest_status }}
                                    </span>
                                </div>
                                <span class="text-blue-500 opacity-0 group-hover:opacity-100 transition-opacity flex items-center">继续对话 <svg class="w-3 h-3 ml-1" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5l7 7-7 7"></path></svg></span>
                            </div>
                        </div>
                    </div>
                </div>

                <!-- 视图 2：连续对话沉浸式聊天界面 -->
                <div v-else class="flex flex-col h-full bg-gray-50 relative">
                    <!-- 聊天头部 -->
                    <div class="p-4 border-b border-gray-200 bg-white flex justify-between items-center shadow-sm z-10">
                        <div class="flex items-center gap-3">
                            <button @click="closeChat" class="text-gray-500 hover:text-blue-600 transition-colors p-1.5 rounded-lg hover:bg-blue-50 border border-transparent hover:border-blue-100">
                                <svg class="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M15 19l-7-7 7-7"></path></svg>
                            </button>
                            <div>
                                <h2 class="text-md font-bold text-gray-800 flex items-center">
                                    沉浸式连续对话
                                    <span class="ml-2 text-[10px] bg-green-100 text-green-700 px-1.5 py-0.5 rounded border border-green-200">上下文已关联</span>
                                </h2>
                                <div class="text-[11px] font-mono text-gray-400 mt-0.5">Session ID: {{ activeSessionId }}</div>
                            </div>
                        </div>
                        <button @click="fetchTasks" class="text-xs text-gray-500 hover:text-gray-800 flex items-center bg-gray-100 px-2 py-1 rounded">
                            <svg class="w-3 h-3 mr-1" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"></path></svg>刷新
                        </button>
                    </div>
                    
                    <!-- 聊天消息区 -->
                    <div id="chat-messages-container" class="flex-1 overflow-y-auto p-6 space-y-6 custom-scrollbar bg-[url('data:image/svg+xml;base64,PHN2ZyB3aWR0aD0iMjAiIGhlaWdodD0iMjAiIHhtbG5zPSJodHRwOi8vd3d3LnczLm9yZy8yMDAwL3N2ZyI+PGNpcmNsZSBjeD0iMiIgY3k9IjIiIHI9IjEiIGZpbGw9IiNlNWU3ZWIiLz48L3N2Zz4=')]">
                        <div v-for="task in activeSessionTasks" :key="task.id" class="flex flex-col gap-5">
                            
                            <!-- 用户消息气泡 (右侧) -->
                            <div class="flex justify-end w-full">
                                <div class="bg-blue-500 text-white p-4 rounded-2xl rounded-tr-sm max-w-[85%] shadow-sm text-sm leading-relaxed whitespace-pre-wrap relative group">
                                    {{ task.payload }}
                                    <div class="text-[10px] text-blue-200 mt-2 text-right font-medium opacity-80">{{ formatDate(task.created_at) }}</div>
                                    
                                    <!-- 悬浮工具栏：复制ID/删除 -->
                                    <div class="absolute top-2 -left-16 opacity-0 group-hover:opacity-100 transition-opacity flex flex-col gap-1">
                                        <button @click="copyToSearch(task.id)" class="bg-white text-gray-500 hover:text-blue-500 p-1.5 rounded-full shadow border border-gray-100" title="复制 Task ID">
                                            <svg class="w-3 h-3" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z"></path></svg>
                                        </button>
                                        <button @click="deleteTask(task.id)" class="bg-white text-gray-500 hover:text-red-500 p-1.5 rounded-full shadow border border-gray-100" title="删除该任务">
                                            <svg class="w-3 h-3" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16"></path></svg>
                                        </button>
                                    </div>
                                </div>
                            </div>
                            
                            <!-- AI 回复气泡 (左侧) -->
                            <div class="flex justify-start w-full">
                                <div class="bg-white border border-gray-200 p-4 rounded-2xl rounded-tl-sm max-w-[85%] shadow-sm relative group">
                                    <!-- 状态指示图标 -->
                                    <div class="absolute -top-2.5 -left-2 bg-white rounded-full p-0.5 shadow-sm border border-gray-100 z-10">
                                        <span v-if="task.status === 'processing'" class="flex h-4 w-4 relative"><span class="animate-ping absolute inline-flex h-full w-full rounded-full bg-blue-400 opacity-75"></span><span class="relative inline-flex rounded-full h-4 w-4 bg-blue-500"></span></span>
                                        <svg v-else-if="task.status === 'completed'" class="w-4 h-4 text-green-500" fill="currentColor" viewBox="0 0 20 20"><path fill-rule="evenodd" d="M10 18a8 8 0 100-16 8 8 0 000 16zm3.707-9.293a1 1 0 00-1.414-1.414L9 10.586 7.707 9.293a1 1 0 00-1.414 1.414l2 2a1 1 0 001.414 0l4-4z" clip-rule="evenodd"></path></svg>
                                        <svg v-else-if="task.status === 'failed'" class="w-4 h-4 text-red-500" fill="currentColor" viewBox="0 0 20 20"><path fill-rule="evenodd" d="M10 18a8 8 0 100-16 8 8 0 000 16zM8.707 7.293a1 1 0 00-1.414 1.414L8.586 10l-1.293 1.293a1 1 0 101.414 1.414L10 11.414l1.293 1.293a1 1 0 001.414-1.414L11.414 10l1.293-1.293a1 1 0 00-1.414-1.414L10 8.586 8.707 7.293z" clip-rule="evenodd"></path></svg>
                                        <svg v-else class="w-4 h-4 text-gray-400" fill="currentColor" viewBox="0 0 20 20"><path fill-rule="evenodd" d="M10 18a8 8 0 100-16 8 8 0 000 16zm1-12a1 1 0 10-2 0v4a1 1 0 00.293.707l2.828 2.829a1 1 0 101.415-1.415L11 9.586V6z" clip-rule="evenodd"></path></svg>
                                    </div>
                                    
                                    <!-- 内容展示 -->
                                    <div v-if="task.status === 'processing'" class="text-sm text-blue-600 flex items-center font-medium">
                                        <svg class="animate-spin h-4 w-4 mr-2" xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"></path></svg>
                                        AI 正在绞尽脑汁思考中...
                                        <button @click="cancelTask(task.id)" class="ml-4 text-xs bg-red-50 text-red-500 hover:bg-red-100 px-2 py-1 rounded border border-red-100">中止</button>
                                    </div>
                                    <div v-else-if="task.status === 'failed'" class="text-sm text-red-600">
                                        <span class="font-bold block mb-1">执行失败:</span>
                                        {{ task.error }}
                                        <button @click="retryTask(task.id)" class="mt-3 text-xs bg-red-100 hover:bg-red-200 px-3 py-1.5 rounded text-red-700 font-medium flex items-center border border-red-200">
                                            <svg class="w-3 h-3 mr-1" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"></path></svg>
                                            重新尝试
                                        </button>
                                    </div>
                                    <div v-else-if="task.status === 'completed'" class="text-sm text-gray-800 whitespace-pre-wrap leading-relaxed">{{ task.result }}</div>
                                    <div v-else class="text-sm text-gray-500 flex items-center">
                                        <svg class="w-4 h-4 mr-1.5 text-gray-400" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 8v4l3 3m6-3a9 9 0 11-18 0 9 9 0 0118 0z"></path></svg>
                                        任务已进入队列，等待调度...
                                    </div>
                                </div>
                            </div>
                        </div>
                    </div>
                    
                    <!-- 聊天底部快捷输入框 -->
                    <div class="p-4 bg-white border-t border-gray-200 z-10 shadow-[0_-4px_6px_-1px_rgba(0,0,0,0.02)]">
                        <div class="relative flex items-end gap-2">
                            <textarea v-model="chatInput" @keydown.enter.exact.prevent="sendChatMessage" rows="2" class="flex-1 border-gray-300 rounded-xl shadow-inner border p-3 focus:ring-2 focus:ring-blue-500 focus:border-blue-500 text-sm custom-scrollbar bg-gray-50" placeholder="输入你想继续追问的内容... (Enter 快捷发送，Shift+Enter 换行)"></textarea>
                            <button @click="sendChatMessage" :disabled="!chatInput.trim() || isChatSubmitting" class="bg-blue-600 hover:bg-blue-700 text-white h-11 w-11 flex items-center justify-center rounded-xl transition-colors disabled:opacity-50 disabled:cursor-not-allowed shadow-md mb-1">
                                <svg v-if="isChatSubmitting" class="animate-spin w-5 h-5" xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"></path></svg>
                                <svg v-else class="w-5 h-5 transform rotate-90" fill="currentColor" viewBox="0 0 20 20"><path d="M10.894 2.553a1 1 0 00-1.788 0l-7 14a1 1 0 001.169 1.409l5-1.429A1 1 0 009 15.571V11a1 1 0 112 0v4.571a1 1 0 00.725.962l5 1.428a1 1 0 001.17-1.408l-7-14z"></path></svg>
                            </button>
                        </div>
                    </div>
                </div>

            </div>
        </main>
"""
    html = html[:old_right_panel_start] + new_right_panel + html[old_right_panel_end:]

    # 2. 注入新的 Vue 状态和逻辑
    state_injection = """
                // 聊天相关状态
                const activeSessionId = ref(null);
                const chatInput = ref('');
                const isChatSubmitting = ref(false);

                // 计算属性：按 session_id 分组任务
                const groupedConversations = computed(() => {
                    const groups = {};
                    tasks.value.forEach(task => {
                        const key = task.session_id || task.id; // 如果没有 session_id，自身 ID 即为上下文起点
                        if (!groups[key]) {
                            groups[key] = {
                                id: key,
                                tasks: [],
                                updated_at: task.created_at,
                                first_payload: task.payload,
                                latest_status: task.status,
                                system_prompt: task.system_prompt || ''
                            };
                        }
                        groups[key].tasks.push(task);
                        
                        // 更新组的最新时间与状态
                        if (new Date(task.created_at) >= new Date(groups[key].updated_at)) {
                            groups[key].updated_at = task.created_at;
                            groups[key].latest_status = task.status;
                        }
                        // 寻找最早的一条作为摘要
                        if (new Date(task.created_at) < new Date(groups[key].tasks[0].created_at)) {
                            groups[key].first_payload = task.payload;
                            groups[key].system_prompt = task.system_prompt || groups[key].system_prompt;
                        }
                    });
                    return Object.values(groups).sort((a, b) => new Date(b.updated_at) - new Date(a.updated_at));
                });

                // 当前激活的会话任务列表（时间升序排列用于聊天展示）
                const activeSessionTasks = computed(() => {
                    if (!activeSessionId.value) return [];
                    const group = groupedConversations.value.find(g => g.id === activeSessionId.value);
                    if (!group) return [];
                    return [...group.tasks].sort((a, b) => new Date(a.created_at) - new Date(b.created_at));
                });

                const scrollToBottom = () => {
                    const container = document.getElementById('chat-messages-container');
                    if (container) {
                        container.scrollTop = container.scrollHeight;
                    }
                };

                const openChat = (id) => {
                    activeSessionId.value = id;
                    // 同步到左侧表单的 session_id，以防用户习惯从左侧提交
                    form.value.session_id = id;
                    setTimeout(scrollToBottom, 100);
                };

                const closeChat = () => {
                    activeSessionId.value = null;
                    form.value.session_id = '';
                };

                const sendChatMessage = async () => {
                    if (!chatInput.value.trim() || isChatSubmitting.value) return;
                    isChatSubmitting.value = true;
                    
                    const group = groupedConversations.value.find(g => g.id === activeSessionId.value);
                    const sysPrompt = group ? group.system_prompt : '';

                    const payloadData = {
                        session_id: activeSessionId.value,
                        system_prompt: sysPrompt,
                        priority: 2,
                        payload: chatInput.value
                    };

                    try {
                        const res = await fetchWithAuth('/api/tasks/submit', {
                            method: 'POST',
                            headers: { 'Content-Type': 'application/json' },
                            body: JSON.stringify(payloadData)
                        });
                        if (res.ok) {
                            chatInput.value = '';
                            await fetchTasks();
                            setTimeout(scrollToBottom, 200);
                        } else if (res.status === 429) {
                            const errData = await res.json();
                            alert(errData.error || '您的操作太频繁啦，请稍后再试');
                        } else {
                            const errData = await res.json();
                            alert('发送失败: ' + (errData.error || res.statusText));
                        }
                    } catch (err) {
                        alert('发送请求出错: ' + err.message);
                    } finally {
                        isChatSubmitting.value = false;
                    }
                };
"""
    # 找到 `const searchResult = ref(null);` 并在其后插入
    insert_pos = html.find("const searchResult = ref(null);")
    if insert_pos != -1:
        insert_pos = html.find("\n", insert_pos)
        html = html[:insert_pos] + "\n" + state_injection + html[insert_pos:]

    # 3. 增强 SSE 以支持滚动
    old_sse = "if (searchResult.value && searchResult.value.id === updatedTask.id) {"
    new_sse = """if (searchResult.value && searchResult.value.id === updatedTask.id) {
                            searchResult.value = updatedTask;
                        }
                        
                        // 如果当前正好在这个聊天页面内，并且是这个会话的消息更新了，滚动到底部
                        if (activeSessionId.value === updatedTask.session_id || activeSessionId.value === updatedTask.id) {
                            setTimeout(scrollToBottom, 100);
                        }"""
    html = html.replace(old_sse + "\n                            searchResult.value = updatedTask;\n                        }", new_sse)

    # 4. 删除旧的 continueChat，因为已经被 openChat 替代了，避免混淆。但为防止模板报错，保留空函数或映射过去
    html = html.replace("const continueChat = (task) => {", "const continueChat = (task) => { openChat(task.session_id || task.id); return; ")

    # 5. 更新 return 对象，暴露新方法
    return_block = """
                    isOptimizing,
                    activeSessionId,
                    chatInput,
                    isChatSubmitting,
                    groupedConversations,
                    activeSessionTasks,
                    openChat,
                    closeChat,
                    sendChatMessage,
                    searchId,"""
    html = html.replace("isOptimizing,\n                    searchId,", return_block)

    with open("public/index.html", "w", encoding="utf-8") as f:
        f.write(html)

update_index_html()
