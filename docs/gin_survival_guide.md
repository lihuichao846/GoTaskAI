# 给新手的 Gin 框架“救生圈”指南：只学这 4 个方法！

我知道当你打开一份文档，看到满屏幕的 `c.Bind`, `c.ShouldBind`, `c.Param`, `c.Query`, `c.JSON`, `c.String`, `c.Data`…… 肯定会觉得头晕目眩。

**其实，在日常 90% 的 Web 开发中，你只需要掌握 Gin 的 4 个核心方法！**
忘记其他的，先把这 4 个方法刻在脑子里，你就能写出绝大多数的接口了。

---

## 核心方法 1：怎么收 JSON 数据？👉 `c.ShouldBindJSON()`

当你的前端（比如 Vue/React）通过 POST 请求给你发来一段 JSON 格式的数据时，你只需要用这个方法。

**场景：前端发来 `{"title": "写代码", "desc": "学习Gin"}`**

```go
// 1. 先定义一个长得像这段 JSON 的结构体
type Task struct {
    Title string `json:"title"`
    Desc  string `json:"desc"`
}

r.POST("/add", func(c *gin.Context) {
    var t Task 
    // 2. 施展魔法：把发来的数据直接变到变量 t 里面
    if err := c.ShouldBindJSON(&t); err != nil {
        c.JSON(400, gin.H{"error": "数据格式不对哦"})
        return
    }
    
    // 3. 现在你可以直接用 t.Title 和 t.Desc 了！
})
```

---

## 核心方法 2：怎么收 URL 里的问号参数？👉 `c.Query()`

当请求是 GET 方法，并且参数跟在 URL 的问号后面时，比如 `http://localhost:8080/search?keyword=golang&page=1`。

**场景：你要获取用户搜了什么关键词**

```go
r.GET("/search", func(c *gin.Context) {
    // 括号里写问号后面的名字
    kw := c.Query("keyword") // 结果是 "golang"
    pg := c.Query("page")    // 结果是 "1"
    
    // 如果没传这个参数，它会返回空字符串 ""
})
```

---

## 核心方法 3：怎么收 URL 路径里的参数？👉 `c.Param()`

有些时候，参数不是在问号后面，而是直接长在路径里面，比如你想看 10 号用户的资料：`http://localhost:8080/user/10`。

**场景：获取路径里的那个 10**

```go
// 1. 在路由里用冒号 : 打个标记，给它取名叫 id
r.GET("/user/:id", func(c *gin.Context) {
    // 2. 用 c.Param 把这个 id 抓出来
    userId := c.Param("id") // 结果是 "10"
})
```

---

## 核心方法 4：怎么把结果返回给前端？👉 `c.JSON()`

你处理完业务了，要给前端回话。现在流行全用 JSON 沟通。

**场景：告诉前端任务创建成功了，顺便把任务详情发回去**

```go
r.GET("/success", func(c *gin.Context) {
    // 两个参数：
    // 第一个是 HTTP 状态码，200代表成功，400代表前端错，500代表后端错
    // 第二个是你要返回的数据
    
    // 绝招：用 gin.H{} 可以快速手写一个 JSON 对象，不用再去定义结构体！
    c.JSON(200, gin.H{
        "status": "ok",
        "message": "任务创建成功啦！",
    })
})
```

---

## 终极总结（截图保存在手机里）

| 我想干什么？ | 我该用哪个方法？ | 例子 |
| :--- | :--- | :--- |
| **收 JSON (Body)** | `c.ShouldBindJSON(&变量)` | 注册、登录、提交表单 |
| **收 ? 后面的参数** | `c.Query("名字")` | 翻页、搜索条件 |
| **收路径里的参数** | `c.Param("名字")` | 获取指定 ID 的详情 |
| **给前端回 JSON** | `c.JSON(200, 数据)` | 所有接口的最后一步 |

就这 4 个！
现在，你可以去看看你项目里的 [handler.go](../internal/api/handler.go)，找找看，是不是全都是这 4 个老朋友在干活？其他的花里胡哨的方法，等你以后这 4 个用得滚瓜烂熟了，再学也不迟！