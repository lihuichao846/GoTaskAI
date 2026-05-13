import re

with open("public/index.html", "r", encoding="utf-8") as f:
    content = f.read()

new_head = """<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>GoTaskAI 调度平台</title>
    <script src="https://cdn.tailwindcss.com"></script>
    <script src="https://unpkg.com/vue@3/dist/vue.global.js"></script>
    <style>
        .progress-bar-transition {
            transition: width 0.5s ease-in-out;
        }
        /* 自定义滚动条 */
        .custom-scrollbar::-webkit-scrollbar {
            width: 6px;
        }
        .custom-scrollbar::-webkit-scrollbar-track {
            background: #f8fafc; 
            border-radius: 10px;
        }
        .custom-scrollbar::-webkit-scrollbar-thumb {
            background: #cbd5e1; 
            border-radius: 10px;
        }
        .custom-scrollbar::-webkit-scrollbar-thumb:hover {
            background: #94a3b8; 
        }
        @keyframes progress {
            0% { width: 0%; opacity: 1; }
            50% { width: 100%; opacity: 0.5; }
            100% { width: 100%; opacity: 0; }
        }
    </style>
</head>"""

new_body = """<body class="bg-gray-50 min-h-screen font-sans text-gray-800">
    <div id="app" class="container mx-auto px-4 py-8 max-w-7xl">
        <header class="mb-8 flex items-center justify-between bg-white p-4 rounded-xl shadow-sm border border-gray-200">
            <div class="flex items-center gap-3">
                <div class="w-10 h-10 bg-blue-600 rounded-lg flex items-center justify-center text-white font-bold text-xl shadow-inner">G</div>
                <h1 class="text-2xl font-bold text-gray-800 tracking-tight">GoTaskAI <span class="text-blue-600 font-medium text-lg ml-1">调度中枢</span></h1>
            </div>
            <div class="flex items-center gap-4 text-sm text-gray-500">
                <span v-if="isLoggedIn" class="font-medium text-gray-700 bg-gray-100 px-3 py-1 rounded-full flex items-center">
                    <svg class="w-4 h-4 mr-1 text-gray-500" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M16 7a4 4 0 11-8 0 4 4 0 018 0zM12 14a7 7 0 00-7 7h14a7 7 0 00-7-7z"></path></svg>
                    {{ username }}
                </span>
                <button v-if="isLoggedIn" @click="logout" class="text-red-500 hover:text-red-700 font-medium transition-colors">退出登录</button>
                <div class="flex items-center bg-green-50 px-3 py-1.5 rounded-full text-green-700 border border-green-200 font-medium">
                    <span class="inline-block w-2.5 h-2.5 rounded-full bg-green-500 mr-2 animate-pulse shadow-[0_0_5px_#22c55e]"></span>
                    服务在线
                </div>
            </div>
        </header>

        <!-- 登录/注册模块 -->
        <div v-if="!isLoggedIn" class="max-w-md mx-auto bg-white p-8 rounded-xl shadow-lg border border-gray-100 mt-16">
            <h2 class="text-2xl font-bold mb-6 text-center text-gray-800">{{ isLoginMode ? '系统登录' : '注册账号' }}</h2>
            <form @submit.prevent="handleAuth">
                <div class="mb-5">
                    <label class="block text-sm font-medium text-gray-700 mb-1">用户名</label>
                    <input v-model="authForm.username" type="text" required class="w-full border-gray-300 rounded-lg shadow-sm border p-2.5 focus:ring-2 focus:ring-blue-500 focus:border-blue-500" placeholder="请输入用户名 (3-20位)">
                </div>
                <div class="mb-6">
                    <label class="block text-sm font-medium text-gray-700 mb-1">密码</label>
                    <input v-model="authForm.password" type="password" required class="w-full border-gray-300 rounded-lg shadow-sm border p-2.5 focus:ring-2 focus:ring-blue-500 focus:border-blue-500" placeholder="请输入密码 (最少6位)">
                </div>
                <button type="submit" :disabled="isAuthSubmitting" class="w-full bg-blue-600 text-white font-semibold py-2.5 px-4 rounded-lg hover:bg-blue-700 disabled:opacity-50 transition-all shadow-md">
                    {{ isAuthSubmitting ? '正在验证...' : (isLoginMode ? '安全登录' : '立即注册') }}
                </button>
                <div class="mt-5 text-center text-sm text-gray-500">
                    {{ isLoginMode ? '没有账号？' : '已有账号？' }}
                    <a href="#" @click.prevent="isLoginMode = !isLoginMode" class="text-blue-600 hover:text-blue-800 font-medium">
                        {{ isLoginMode ? '创建新账号' : '返回登录' }}
                    </a>
                </div>
            </form>
        </div>

        <main v-else class="grid grid-cols-1 lg:grid-cols-12 gap-8">
            <!-- 左侧：提交与查询 -->
            <div class="lg:col-span-5 xl:col-span-4 space-y-6">
                <!-- 提交表单 -->
                <div class="bg-white p-6 rounded-xl shadow-sm border border-gray-200 relative">
                    <!-- 批量提交切换开关 -->
                    <div class="absolute top-6 right-6 flex items-center bg-gray-50 px-2 py-1 rounded-md border border-gray-200">
                        <span class="text-xs text-gray-600 mr-2 font-medium">批量</span>
                        <button @click="isBatchMode = !isBatchMode" class="relative inline-flex h-5 w-9 items-center rounded-full transition-colors focus:outline-none" :class="isBatchMode ? 'bg-blue-500' : 'bg-gray-300'">
                            <span class="inline-block h-3 w-3 transform rounded-full bg-white transition-transform shadow-sm" :class="isBatchMode ? 'translate-x-5' : 'translate-x-1'"></span>
                        </button>
                    </div>

                    <h2 class="text-lg font-bold mb-5 text-gray-800 flex items-center">
                        <svg class="w-5 h-5 mr-2 text-blue-500" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v16m8-8H4"></path></svg>
                        {{ isBatchMode ? '批量提交任务' : '提交新任务' }}
                    </h2>
                    <form @submit.prevent="handleFormSubmit">
                        
                        <!-- 核心必填区 -->
                        <div class="mb-5">
                            <label class="block text-sm font-semibold text-gray-700 mb-2">
                                请求内容 (User Prompt) <span class="text-red-500">*</span>
                                <span v-if="isBatchMode" class="text-xs text-blue-600 font-normal ml-2 bg-blue-50 px-1.5 py-0.5 rounded border border-blue-100">每行独立任务</span>
                            </label>
                            <textarea v-model="form.payload" :rows="isBatchMode ? 6 : 4" class="w-full border-gray-300 rounded-lg shadow-sm border p-3 focus:ring-2 focus:ring-blue-500 focus:border-blue-500 transition-shadow text-sm" :placeholder="isBatchMode ? '任务1...\\n任务2...\\n任务3...' : '请输入你想让 AI 帮你完成的事情...'"></textarea>
                        </div>

                        <!-- 选填/高级配置区 (分组收纳) -->
                        <div class="bg-gray-50 p-4 rounded-lg border border-gray-200 mb-5 space-y-4">
                            <div class="text-xs font-semibold text-gray-500 uppercase tracking-wider">上下文与设定 (选填)</div>
                            
                            <div>
                                <label class="block text-xs font-medium text-gray-700 mb-1">会话关联 (Session ID)</label>
                                <input v-model="form.session_id" type="text" class="w-full border-gray-300 rounded-md shadow-sm border p-2 focus:ring-blue-500 focus:border-blue-500 font-mono text-xs bg-white" placeholder="相同ID可保持对话记忆">
                            </div>
                            
                            <div>
                                <label class="flex justify-between items-center text-xs font-medium text-gray-700 mb-1">
                                    <span>系统角色设定 (System Prompt)</span>
                                    <button type="button" @click="optimizePrompt" :disabled="isOptimizing || !form.system_prompt.trim()" class="text-[10px] text-blue-600 hover:text-blue-800 disabled:opacity-50 flex items-center bg-blue-50 px-2 py-0.5 rounded border border-blue-100 transition-colors">
                                        <svg v-if="isOptimizing" class="animate-spin h-3 w-3 mr-1" xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"></path></svg>
                                        <svg v-else class="w-3 h-3 mr-1" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 3v4M3 5h4M6 17v4m-2-2h4m5-16l2.286 6.857L21 12l-5.714 2.143L13 21l-2.286-6.857L5 12l5.714-2.143L13 3z"></path></svg>
                                        {{ isOptimizing ? '优化中...' : 'AI 一键扩写' }}
                                    </button>
                                </label>
                                <textarea v-model="form.system_prompt" rows="2" class="w-full border-gray-300 rounded-md shadow-sm border p-2 focus:ring-blue-500 focus:border-blue-500 text-xs bg-white" placeholder="例如：翻译专家..."></textarea>
                            </div>
                        </div>

                        <div class="mb-6">
                            <label class="block text-sm font-semibold text-gray-700 mb-2">任务优先级</label>
                            <select v-model.number="form.priority" class="w-full border-gray-300 rounded-lg shadow-sm border p-2.5 focus:ring-2 focus:ring-blue-500 focus:border-blue-500 text-sm bg-white">
                                <option :value="1">🟢 低优先级 (排队靠后)</option>
                                <option :value="2">🔵 普通优先级 (默认)</option>
                                <option :value="3">🔴 高优先级 (插队优先)</option>
                            </select>
                        </div>
                        <button type="submit" :disabled="isSubmitting" class="w-full bg-blue-600 text-white font-bold py-3 px-4 rounded-lg hover:bg-blue-700 focus:ring-4 focus:ring-blue-200 disabled:opacity-50 transition-all shadow-md">
                            {{ isSubmitting ? '正在提交...' : '🚀 立即提交任务' }}
                        </button>
                    </form>
                </div>

                <!-- 精确查询 -->
                <div class="bg-white p-6 rounded-xl shadow-sm border border-gray-200">
                    <h2 class="text-lg font-bold mb-4 text-gray-800 flex items-center">
                        <svg class="w-5 h-5 mr-2 text-gray-500" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z"></path></svg>
                        精确查询
                    </h2>
                    <form @submit.prevent="searchTask">
                        <div class="mb-4">
                            <input v-model="searchId" type="text" class="w-full border-gray-300 rounded-lg shadow-sm border p-2.5 focus:ring-2 focus:ring-blue-500 focus:border-blue-500 font-mono text-sm" placeholder="输入完整的 UUID">
                        </div>
                        <button type="submit" :disabled="!searchId.trim() || isSearching" class="w-full bg-gray-50 text-gray-700 font-semibold py-2.5 px-4 rounded-lg hover:bg-gray-100 disabled:opacity-50 transition-colors border border-gray-200 shadow-sm">
                            {{ isSearching ? '正在查询...' : '🔍 查询状态' }}
                        </button>
                    </form>

                    <!-- 查询结果展示卡片 -->
                    <div v-if="searchResult" class="mt-4 p-4 rounded-lg border text-sm border-l-4 bg-gray-50" :class="getBorderColor(searchResult.status)">
                        <div class="flex justify-between items-center mb-3">
                            <span class="font-semibold text-gray-700">查询结果</span>
                            <span :class="getStatusBadgeClass(searchResult.status)" class="px-2.5 py-0.5 rounded-full text-xs font-medium capitalize">{{ searchResult.status }}</span>
                        </div>
                        <div class="mb-1 text-gray-600 break-all bg-white p-2 rounded border border-gray-100"><span class="font-semibold text-gray-800 text-xs">ID:</span> <span class="font-mono text-xs">{{ searchResult.id }}</span></div>
                        <div class="flex gap-4 mt-2 mb-2">
                            <div class="text-gray-600"><span class="font-semibold text-gray-800">类型:</span> {{ searchResult.type }}</div>
                            <div class="text-gray-600"><span class="font-semibold text-gray-800">重试:</span> {{ searchResult.retries }}/{{ searchResult.max_retry }}</div>
                        </div>
                        <div v-if="searchResult.result" class="mt-2 p-2.5 bg-green-50 text-green-800 rounded-lg border border-green-200">{{ searchResult.result }}</div>
                        <div v-if="searchResult.error" class="mt-2 p-2.5 bg-red-50 text-red-800 rounded-lg border border-red-200">{{ searchResult.error }}</div>
                        <button @click="searchResult = null" class="mt-4 text-xs text-gray-500 hover:text-gray-800 w-full text-center py-1.5 bg-white border rounded hover:bg-gray-50">关闭结果</button>
                    </div>
                </div>
            </div>

            <!-- 右侧：任务列表 -->
            <div class="lg:col-span-7 xl:col-span-8 bg-white p-6 rounded-xl shadow-sm border border-gray-200 flex flex-col min-h-[700px]">
                <div class="flex justify-between items-center mb-6 pb-4 border-b border-gray-100">
                    <h2 class="text-lg font-bold text-gray-800 flex items-center">
                        <svg class="w-5 h-5 mr-2 text-blue-500" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5H7a2 2 0 00-2 2v12a2 2 0 002 2h10a2 2 0 002-2V7a2 2 0 00-2-2h-2M9 5a2 2 0 002 2h2a2 2 0 002-2M9 5a2 2 0 012-2h2a2 2 0 012 2"></path></svg>
                        任务执行大厅
                        <span class="ml-3 bg-blue-50 text-blue-600 text-xs py-0.5 px-2 rounded-full border border-blue-100 font-medium">共 {{ tasks.length }} 项</span>
                    </h2>
                    <button @click="fetchTasks" class="text-sm text-gray-600 hover:text-blue-600 flex items-center bg-gray-50 px-3 py-1.5 rounded-lg border border-gray-200 transition-colors shadow-sm">
                        <svg class="w-4 h-4 mr-1.5" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"></path></svg>
                        刷新状态
                    </button>
                </div>

                <div v-if="tasks.length === 0" class="flex-1 flex flex-col items-center justify-center text-gray-400">
                    <svg class="w-16 h-16 mb-4 text-gray-200" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M20 13V6a2 2 0 00-2-2H6a2 2 0 00-2 2v7m16 0v5a2 2 0 01-2 2H6a2 2 0 01-2-2v-5m16 0h-2.586a1 1 0 00-.707.293l-2.414 2.414a1 1 0 01-.707.293h-3.172a1 1 0 01-.707-.293l-2.414-2.414A1 1 0 006.586 13H4"></path></svg>
                    <p>暂无任务，请在左侧提交</p>
                </div>

                <div v-else class="flex-1 overflow-y-auto pr-3 space-y-4 custom-scrollbar" style="max-height: calc(100vh - 250px);">
                    <div v-for="task in sortedTasks" :key="task.id" class="bg-white border rounded-xl p-5 relative overflow-hidden transition-all hover:shadow-md border-l-4" :class="getBorderColor(task.status)">
                        <div class="flex justify-between items-start mb-3">
                            <div class="flex items-center group">
                                <span class="text-xs font-mono bg-gray-50 text-gray-500 border border-gray-100 px-2 py-1 rounded mr-2" :title="task.id">
                                    {{ task.id.substring(0, 8) }}...
                                </span>
                                <!-- 一键复制完整 ID 按钮 -->
                                <button @click.prevent="copyToSearch(task.id)" class="text-gray-400 hover:text-blue-600 mr-3 opacity-0 group-hover:opacity-100 transition-opacity bg-white p-1 rounded-md shadow-sm border border-gray-100" title="复制并填入查询框">
                                    <svg class="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z"></path></svg>
                                </button>
                                <span class="text-sm font-bold text-gray-700 capitalize">{{ task.type }}</span>
                                <span v-if="task.priority === 3" class="ml-2 text-[10px] bg-red-50 text-red-600 px-2 py-0.5 rounded-full border border-red-100 font-medium">高优</span>
                                <span v-else-if="task.priority === 1" class="ml-2 text-[10px] bg-gray-50 text-gray-500 px-2 py-0.5 rounded-full border border-gray-200 font-medium">低优</span>
                            </div>
                            <div class="flex items-center space-x-2 bg-gray-50 px-2 py-1 rounded-lg border border-gray-100">
                                <span :class="getStatusBadgeClass(task.status)" class="text-xs px-2 py-1 rounded font-medium capitalize flex items-center">
                                    <span v-if="task.status === 'processing'" class="w-1.5 h-1.5 rounded-full bg-blue-500 mr-1.5 animate-ping"></span>
                                    <span v-else-if="task.status === 'completed'" class="w-1.5 h-1.5 rounded-full bg-green-500 mr-1.5"></span>
                                    <span v-else-if="task.status === 'failed'" class="w-1.5 h-1.5 rounded-full bg-red-500 mr-1.5"></span>
                                    <span v-else class="w-1.5 h-1.5 rounded-full bg-gray-400 mr-1.5"></span>
                                    {{ task.status }}
                                </span>
                                <div class="w-px h-4 bg-gray-300 mx-1"></div>
                                <!-- 继续对话按钮 (仅成功状态可见) -->
                                <button v-if="task.status === 'completed'" @click.prevent="continueChat(task)" class="text-indigo-500 hover:text-indigo-700 transition-colors p-1" title="以此任务为上下文继续对话">
                                    <svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 10h.01M12 10h.01M16 10h.01M9 16H5a2 2 0 01-2-2V6a2 2 0 012-2h14a2 2 0 012 2v8a2 2 0 01-2 2h-5l-5 5v-5z"></path></svg>
                                </button>
                                <!-- 重试按钮 (仅失败状态可见) -->
                                <button v-if="task.status === 'failed'" @click="retryTask(task.id)" class="text-blue-500 hover:text-blue-700 transition-colors p-1" title="重试任务">
                                    <svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"></path></svg>
                                </button>
                                <!-- 取消按钮 (仅等待中和处理中可见) -->
                                <button v-if="['pending', 'processing'].includes(task.status)" @click="cancelTask(task.id)" class="text-orange-500 hover:text-orange-700 transition-colors p-1" title="取消任务">
                                    <svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M18.364 18.364A9 9 0 005.636 5.636m12.728 12.728A9 9 0 015.636 5.636m12.728 12.728L5.636 5.636"></path></svg>
                                </button>
                                <!-- 删除按钮 -->
                                <button @click="deleteTask(task.id)" class="text-gray-400 hover:text-red-500 transition-colors p-1" title="删除任务">
                                    <svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16"></path></svg>
                                </button>
                            </div>
                        </div>

                        <div class="text-sm text-gray-700 mb-3 bg-gray-50 p-3 rounded-lg border border-gray-100">
                            <strong class="text-gray-500 text-xs uppercase block mb-1">输入 (Payload)</strong>
                            <div class="line-clamp-2">{{ task.payload }}</div>
                        </div>

                        <div v-if="task.result" class="text-sm text-green-800 bg-green-50 p-3 rounded-lg mb-3 border border-green-200">
                            <strong class="text-green-600 text-xs uppercase block mb-1">执行结果 (Result)</strong>
                            <div>{{ task.result }}</div>
                        </div>

                        <div v-if="task.error" class="text-sm text-red-800 bg-red-50 p-3 rounded-lg mb-3 border border-red-200">
                            <strong class="text-red-600 text-xs uppercase block mb-1">错误信息 (Error)</strong>
                            <div>{{ task.error }}</div>
                        </div>

                        <div class="flex justify-between text-xs text-gray-400 mt-3 pt-3 border-t border-gray-50">
                            <span>🔄 重试: {{ task.retries }}/{{ task.max_retry }}</span>
                            <span>🕒 {{ formatDate(task.created_at) }}</span>
                        </div>

                        <!-- 进度条 -->
                        <div v-if="['pending', 'processing'].includes(task.status)" class="absolute bottom-0 left-0 right-0 h-1 bg-gray-100">
                            <div class="h-full progress-bar-transition" 
                                 :class="task.status === 'processing' ? 'bg-blue-500 w-full' : 'bg-gray-300 w-1/4'"
                                 :style="task.status === 'processing' ? 'animation: progress 2s ease-in-out infinite' : ''">
                            </div>
                        </div>
                    </div>
                </div>
            </div>
        </main>
    </div>
"""

script_block = content[content.find("<script>"):content.find("</body>")]

script_block = script_block.replace("""
                const getStatusBadgeClass = (status) => {
                    const map = {
                        'pending': 'bg-gray-100 text-gray-800',
                        'processing': 'bg-blue-100 text-blue-800',
                        'completed': 'bg-green-100 text-green-800',
                        'failed': 'bg-red-100 text-red-800'
                    };
                    return map[status] || 'bg-gray-100 text-gray-800';
                };

                const getBorderColor = (status) => {
                    const map = {
                        'processing': 'border-blue-300 shadow-[0_0_10px_rgba(59,130,246,0.1)]',
                        'completed': 'border-green-200',
                        'failed': 'border-red-200',
                        'pending': 'border-gray-200'
                    };
                    return map[status] || 'border-gray-200';
                };
""", """
                const getStatusBadgeClass = (status) => {
                    const map = {
                        'pending': 'text-gray-600',
                        'processing': 'text-blue-600',
                        'completed': 'text-green-600',
                        'failed': 'text-red-600'
                    };
                    return map[status] || 'text-gray-600';
                };

                const getBorderColor = (status) => {
                    const map = {
                        'processing': 'border-gray-200 border-l-blue-500 shadow-[0_0_15px_rgba(59,130,246,0.15)]',
                        'completed': 'border-gray-200 border-l-green-500',
                        'failed': 'border-gray-200 border-l-red-500',
                        'pending': 'border-gray-200 border-l-gray-400'
                    };
                    return map[status] || 'border-gray-200 border-l-gray-200';
                };
""")

final_html = new_head + "\n" + new_body + "\n    " + script_block + "\n</body>\n</html>"
with open("public/index.html", "w", encoding="utf-8") as f:
    f.write(final_html)