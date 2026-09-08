# GoTaskAI：JWT 鉴权机制实现解析

在 GoTaskAI 项目中，我们采用 **JWT (JSON Web Token)** 作为核心鉴权机制。由于 API Server 作为接客网关是无状态的，JWT 完美契合了系统的高并发和微服务解耦需求。本文将聚焦当前项目中 JWT 的具体代码实现。

---

## 1. Token 的生成 (用户登录)

当用户在前端发起 `/api/auth/login` 请求时，流程进入 API Server 的 `AuthHandler`：

1. **密码校验**：后端比对用户邮箱，并使用 `bcrypt` 校验哈希密码。
2. **生成 Claims 载荷**：校验通过后，调用 `jwt.GenerateToken(userID)`。
3. **签名与颁发**：
   - 载荷中包含 `UserID`，并将过期时间设为 24 小时。
   - 使用 `HS256` 算法和服务器专属的私钥（如 `gotaskai-super-secret-key`）对载荷进行签名。
   - 最终生成的 JWT 字符串被返回给前端客户端。

代码入口参考：`internal/api/auth.go` 和 `internal/pkg/jwt/jwt.go`。

---

## 2. 请求拦截与校验 (AuthMiddleware)

对于需要保护的接口（如提交任务、查询任务列表、建立 SSE 长连接等），API Server 在路由层挂载了全局的 `AuthMiddleware` 鉴权中间件。

前端在每次请求时，必须在 HTTP 头中携带 Token：
`Authorization: Bearer <token>`

中间件的处理逻辑如下：
1. **提取 Header**：拦截请求并读取 `Authorization` 字段。
2. **格式检查**：校验是否以 `Bearer ` 开头。
3. **CPU 验签**：调用 `jwt.ParseToken` 方法，利用服务器私钥直接在 CPU 层面进行签名校验和过期时间检查。**此过程无需查询 MySQL 或 Redis，极大地降低了 I/O 开销。**
4. **上下文传递**：校验成功后，从解析出的载荷中取出 `UserID`，并将其注入到当前请求的 Gin Context 中（`c.Set("userID", claims.UserID)`）。
5. **放行**：调用 `c.Next()` 将请求放行给后续的业务层。

代码入口参考：`internal/middleware/auth.go`。

---

## 3. 业务层无缝调用

经过 `AuthMiddleware` 拦截并注入上下文后，后续的业务 Controller（如处理任务提交的 `Handler`）不再需要关心“用户是否登录”或如何鉴权。

业务代码只需简单调用 `c.GetUint("userID")`，就能立刻获取当前请求的发起者 ID，从而实现数据隔离（例如将新创建的任务与该 UserID 绑定入库）。这种设计实现了鉴权逻辑与业务逻辑的完美解耦。
