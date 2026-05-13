import re

def update_frontend():
    with open("public/index.html", "r", encoding="utf-8") as f:
        html = f.read()

    # 1. 更新登录/注册模块 UI
    old_auth_ui_start = html.find('<!-- 登录/注册模块 -->')
    old_auth_ui_end = html.find('<main v-else')
    
    if old_auth_ui_start == -1 or old_auth_ui_end == -1:
        print("Failed to find auth UI")
        return

    new_auth_ui = """<!-- 登录/注册/找回密码模块 -->
        <div v-if="!isLoggedIn" class="max-w-md mx-auto bg-white p-8 rounded-xl shadow-lg border border-gray-100 mt-16">
            <h2 class="text-2xl font-bold mb-6 text-center text-gray-800">
                <span v-if="authMode === 'login'">系统登录</span>
                <span v-else-if="authMode === 'register'">注册账号</span>
                <span v-else>找回密码</span>
            </h2>
            
            <!-- 找回密码表单 -->
            <form v-if="authMode === 'forgot'" @submit.prevent="handleResetPassword">
                <div class="mb-4">
                    <label class="block text-sm font-medium text-gray-700 mb-1">账号用户名</label>
                    <div class="flex gap-2">
                        <input v-model="authForm.username" type="text" required class="flex-1 border-gray-300 rounded-lg shadow-sm border p-2.5 focus:ring-2 focus:ring-blue-500 focus:border-blue-500" placeholder="请输入你的用户名">
                        <button type="button" @click="requestResetCode" :disabled="!authForm.username || isRequestingCode" class="bg-gray-100 text-gray-700 hover:bg-gray-200 px-3 py-2.5 rounded-lg border border-gray-300 text-sm font-medium disabled:opacity-50 whitespace-nowrap">
                            {{ isRequestingCode ? '发送中...' : '获取验证码' }}
                        </button>
                    </div>
                    <p v-if="codeSent" class="text-xs text-green-600 mt-1 mt-2">✅ 验证码已发送，请查看后台终端控制台 (有效期5分钟)</p>
                </div>
                <div class="mb-4">
                    <label class="block text-sm font-medium text-gray-700 mb-1">6位验证码</label>
                    <input v-model="authForm.code" type="text" required maxlength="6" class="w-full border-gray-300 rounded-lg shadow-sm border p-2.5 focus:ring-2 focus:ring-blue-500 focus:border-blue-500" placeholder="请输入6位验证码">
                </div>
                <div class="mb-6">
                    <label class="block text-sm font-medium text-gray-700 mb-1">新密码</label>
                    <input v-model="authForm.password" type="password" required class="w-full border-gray-300 rounded-lg shadow-sm border p-2.5 focus:ring-2 focus:ring-blue-500 focus:border-blue-500" placeholder="请输入新密码 (最少6位)">
                </div>
                <button type="submit" :disabled="isAuthSubmitting" class="w-full bg-blue-600 text-white font-semibold py-2.5 px-4 rounded-lg hover:bg-blue-700 disabled:opacity-50 transition-all shadow-md">
                    {{ isAuthSubmitting ? '正在重置...' : '确认重置密码' }}
                </button>
                <div class="mt-5 text-center text-sm text-gray-500">
                    想起来了？
                    <a href="#" @click.prevent="authMode = 'login'" class="text-blue-600 hover:text-blue-800 font-medium">返回登录</a>
                </div>
            </form>

            <!-- 登录/注册表单 -->
            <form v-else @submit.prevent="handleAuth">
                <div class="mb-5">
                    <label class="block text-sm font-medium text-gray-700 mb-1">用户名</label>
                    <input v-model="authForm.username" type="text" required class="w-full border-gray-300 rounded-lg shadow-sm border p-2.5 focus:ring-2 focus:ring-blue-500 focus:border-blue-500" placeholder="请输入用户名 (3-20位)">
                </div>
                <div class="mb-6">
                    <div class="flex justify-between items-center mb-1">
                        <label class="block text-sm font-medium text-gray-700">密码</label>
                        <a v-if="authMode === 'login'" href="#" @click.prevent="authMode = 'forgot'" class="text-xs text-blue-600 hover:text-blue-800 font-medium tabindex='-1'">忘记密码？</a>
                    </div>
                    <input v-model="authForm.password" type="password" required class="w-full border-gray-300 rounded-lg shadow-sm border p-2.5 focus:ring-2 focus:ring-blue-500 focus:border-blue-500" placeholder="请输入密码 (最少6位)">
                </div>
                <button type="submit" :disabled="isAuthSubmitting" class="w-full bg-blue-600 text-white font-semibold py-2.5 px-4 rounded-lg hover:bg-blue-700 disabled:opacity-50 transition-all shadow-md">
                    {{ isAuthSubmitting ? '正在验证...' : (authMode === 'login' ? '安全登录' : '立即注册') }}
                </button>
                <div class="mt-5 text-center text-sm text-gray-500">
                    {{ authMode === 'login' ? '没有账号？' : '已有账号？' }}
                    <a href="#" @click.prevent="authMode = authMode === 'login' ? 'register' : 'login'" class="text-blue-600 hover:text-blue-800 font-medium">
                        {{ authMode === 'login' ? '创建新账号' : '返回登录' }}
                    </a>
                </div>
            </form>
        </div>

        """
    html = html[:old_auth_ui_start] + new_auth_ui + html[old_auth_ui_end:]

    # 2. 注入 Vue 逻辑
    # 替换 isLoginMode 相关的状态
    html = html.replace("const isLoginMode = ref(true);", """const authMode = ref('login'); // 'login', 'register', 'forgot'
                const isRequestingCode = ref(false);
                const codeSent = ref(false);""")

    # 更新 handleAuth
    old_handleAuth = """const handleAuth = async () => {
                    if (!authForm.value.username || !authForm.value.password) return;
                    isAuthSubmitting.value = true;
                    const endpoint = isLoginMode.value ? '/api/auth/login' : '/api/auth/register';"""
    new_handleAuth = """const handleAuth = async () => {
                    if (!authForm.value.username || !authForm.value.password) return;
                    isAuthSubmitting.value = true;
                    const endpoint = authMode.value === 'login' ? '/api/auth/login' : '/api/auth/register';"""
    html = html.replace(old_handleAuth, new_handleAuth)
    
    # 插入新方法
    new_methods = """
                const requestResetCode = async () => {
                    if (!authForm.value.username) {
                        alert("请先输入用户名");
                        return;
                    }
                    isRequestingCode.value = true;
                    codeSent.value = false;
                    try {
                        const res = await fetch('/api/auth/forgot-password/request-code', {
                            method: 'POST',
                            headers: { 'Content-Type': 'application/json' },
                            body: JSON.stringify({ username: authForm.value.username })
                        });
                        const data = await res.json();
                        if (res.ok) {
                            codeSent.value = true;
                            alert("验证码已生成！请查看后端 Go 控制台的输出。");
                        } else {
                            alert(data.error || '获取失败');
                        }
                    } catch (err) {
                        alert('网络错误: ' + err.message);
                    } finally {
                        isRequestingCode.value = false;
                    }
                };

                const handleResetPassword = async () => {
                    if (!authForm.value.username || !authForm.value.password || !authForm.value.code) {
                        alert("请填写完整信息");
                        return;
                    }
                    isAuthSubmitting.value = true;
                    try {
                        const res = await fetch('/api/auth/forgot-password/reset', {
                            method: 'POST',
                            headers: { 'Content-Type': 'application/json' },
                            body: JSON.stringify({ 
                                username: authForm.value.username,
                                code: authForm.value.code,
                                new_password: authForm.value.password
                            })
                        });
                        const data = await res.json();
                        if (res.ok) {
                            alert("密码重置成功！请重新登录。");
                            authMode.value = 'login';
                            authForm.value.password = '';
                            authForm.value.code = '';
                            codeSent.value = false;
                        } else {
                            alert(data.error || '重置失败');
                        }
                    } catch (err) {
                        alert('网络错误: ' + err.message);
                    } finally {
                        isAuthSubmitting.value = false;
                    }
                };
"""
    # 找到 const handleFormSubmit 的位置插入
    insert_pos = html.find("const handleFormSubmit = async () => {")
    if insert_pos != -1:
        html = html[:insert_pos] + new_methods + html[insert_pos:]

    # 更新 return
    return_block_old = "isLoginMode,"
    return_block_new = """authMode,
                    isRequestingCode,
                    codeSent,
                    requestResetCode,
                    handleResetPassword,"""
    html = html.replace(return_block_old, return_block_new)

    with open("public/index.html", "w", encoding="utf-8") as f:
        f.write(html)

update_frontend()