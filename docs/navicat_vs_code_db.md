# 数据库操作答疑：Navicat vs 代码调用 & 存储路径揭秘

很多刚开始写后端代码的同学，习惯了使用 **Navicat** 这种图形化工具去操作 MySQL。当开始用 Go (GORM) 写代码时，经常会产生疑惑：它们到底有什么区别？我代码里存的数据到底存在了哪里？

---

## 1. Navicat 与 代码调用 (GORM) 的本质区别

首先要明确一点：**无论是 Navicat 还是你写的 Go 代码，它们都只是 MySQL 数据库的“客户端（Client）”。**

它们就像是两部不同型号的手机，都在给同一个“接线员”（MySQL 服务端）打电话。

### 1.1 角色定位的区别

*   **Navicat (图形化客户端)：**
    *   **定位：** 给 **“人”** 用的管理工具。
    *   **用途：** 主要是为了方便程序员在开发、调试时，直观地建库、建表、看数据、写复杂的 SQL 进行临时查询或修改。
    *   **运作方式：** 你在界面上点一个“保存”按钮，Navicat 在后台悄悄帮你拼凑出一条 `UPDATE ...` 的 SQL 语句，然后通过网络（TCP协议）发给 MySQL 服务端。

*   **Go 代码 + GORM (程序化客户端)：**
    *   **定位：** 给 **“程序”** 用的自动化工具。
    *   **用途：** 当你的系统上线后，你不可能 24 小时坐在电脑前用 Navicat 帮用户注册账号。你的代码代替了你，全天候自动接收用户的 HTTP 请求，然后将其转化为数据库操作。
    *   **运作方式：** 你在代码里写 `db.Save(&user)`，GORM 同样在后台帮你拼凑出一条 `UPDATE ...` 的 SQL 语句，通过网络发给 MySQL。

### 1.2 连接方式的区别 (DSN)

你会发现，无论是 Navicat 还是 Go 代码，都需要填写一些关键信息才能连上数据库，这被称为 **DSN (Data Source Name)**。

在 [main.go](../main.go) 中，我们写了这样一行：
```go
dsn := "root:root@tcp(127.0.0.1:3306)/gotaskai?charset=utf8mb4..."
```
这其实完全对应了你在 Navicat 里新建连接时填写的表单：
*   **用户名 (User):** `root`
*   **密码 (Password):** `root`
*   **主机 (Host):** `127.0.0.1` (本地)
*   **端口 (Port):** `3306` (MySQL 默认端口)
*   **数据库名 (Database):** `gotaskai`

**总结：** 它们俩是平级的兄弟关系。你在 Go 代码里存进去的数据，完全可以立刻用 Navicat 连上去看到（前提是连的同一个数据库）。

---

## 2. 当前数据库的数据究竟存在了哪里？

在传统的开发方式中，你可能是在 Windows 电脑上下载了一个 `.exe` 的 MySQL 安装包，一路“下一步”，数据就存在了 `C:\ProgramData\MySQL\MySQL Server 8.0\Data` 之类的地方。

**但在我们的项目中，我们使用的是 Docker。**

如果你打开了项目根目录下的 [docker-compose.yml](../docker-compose.yml)，你会看到这样的配置：

```yaml
services:
  mysql:
    image: mysql:8.0
    container_name: gotaskai_mysql
    # ... 省略 ...
    volumes:
      - mysql_data:/var/lib/mysql # <-- 关键在这里！

volumes:
  mysql_data:
```

### 2.1 容器内的路径
在 Docker 运行的那个虚拟的、隔离的 Linux 环境（容器）里，MySQL 默认把所有的数据文件（如 `.ibd` 表文件）保存在：
👉 `/var/lib/mysql`

### 2.2 宿主机的映射 (Volumes)
如果仅仅存在容器里，一旦你删除了这个容器，所有数据就灰飞烟灭了。
所以，我们在 `docker-compose.yml` 中使用了 **Volumes（数据卷）** 映射技术：
`mysql_data:/var/lib/mysql`

这句话的意思是：**让 Docker 在你的真机（Windows宿主机）上划出一块安全的地盘（起名叫 `mysql_data`），把它像个 U 盘一样“插”进容器里的 `/var/lib/mysql` 目录下。**

这样一来，MySQL 以为自己把数据写在了容器里，实际上数据被保存到了你的真机硬盘上。即使你把容器删了重建，只要重新挂载这个“U盘”，数据就还在。

### 2.3 在 Windows 上，这块“地盘”到底在哪？

Docker Desktop for Windows 通常运行在 WSL2（Windows Subsystem for Linux）虚拟机内部。
因此，你无法直接在 `C 盘` 或 `D 盘` 的某个直观文件夹里看到这些原始的数据库文件。

它们被深藏在 Docker 的虚拟磁盘文件中（通常位于 `C:\Users\<你的用户名>\AppData\Local\Docker\wsl\data\ext4.vhdx`）。

**给你的建议：**
永远不要尝试直接去 Windows 资源管理器里找这些原始的底层数据文件，甚至不要去修改它们，因为这极易导致数据库损坏。

如果你想查看、备份或导出数据：
**正确做法就是打开你的 Navicat，输入 `127.0.0.1:3306`，连上它，然后用 Navicat 的“导出向导”或“数据传输”功能把数据导成 `.sql` 或 `.csv` 文件。**