# DanmakuFlow

Go 直播弹幕项目，由 JavaLyHn 维护。支持 WebSocket 房间、弹幕广播、点赞与在线连接数统计，提供浏览器界面与终端客户端，使用 Redis 进行跨节点分发，使用 Kafka 和 MySQL 完成异步持久化。

当前以 **Go 版本为开发主线**。[DanmakuFlow-Java](https://github.com/JavaLyHn/DanmakuFlow-Java) 保留 Java 实现并暂停开发；只有收到明确的同步指令后，才将选定的 Go 改动同步到 Java。

仓库拆分、验证边界和后续事项见 [开发记录](docs/DEVELOPMENT.md)。

## 代码结构

| 路径 | 用途 |
| --- | --- |
| `cmd/server` | Gin / WebSocket 网关，入口为 `/ws` |
| `cmd/consumer` | Kafka 消费与 MySQL 批量持久化 |
| `cmd/migrate` | 显式补建缺失的 MySQL 表，保留已有表 |
| `cmd/client` | 交互式 WebSocket 客户端 |
| `web` | Vue 3 / TypeScript 房间互动界面，Vite 构建 |
| `internal/webui` | 托管构建后的页面与静态资源 |
| `cmd/benchmark` | 已有压测工具，本轮不运行 |
| `internal/ws` | 连接、房间与消息分发 |
| `internal/infra` | Redis、Kafka、MySQL 连接与表结构检查 |
| `internal/repo` | 数据库访问 |
| `internal/service` | 业务服务 |

Go 模块要求版本为 1.25.0。主要依赖为 Gin、Gorilla WebSocket、go-redis、Sarama 和 GORM。

协议地址示例：`ws://127.0.0.1:8080/ws?uid=1001&name=demo&room=1001`。101 为弹幕、102 为房间统计、103 为点赞。`uid` 是客户端提供的身份参数，尚无账号认证。回放 HTTP 接口当前未启用。

## 浏览器界面

使用 Node.js 24，在仓库根目录执行：

```powershell
cd web
npm ci
npm run build
cd ..
```

在原有数据库及 Redis/Kafka 环境已经配置就绪时，沿用服务端启动方式：

```powershell
go run ./cmd/server -port 8080
```

浏览器打开 `http://127.0.0.1:8080/`，填写端口、房间 ID、用户 ID 后点击“连接房间”。UID 可留空自动生成。页面与终端连接同一个 `/ws`：Enter 或发送按钮提交原文，只有恰好输入 `1` 才点赞；点赞按钮不会清除草稿。消息和统计等待服务端推送，不显示模拟数据。

页面采用直播间布局，视频区仅为静态占位，没有视频播放或推流能力。当前范围是本机客户端，WebSocket 主机固定为 `127.0.0.1`；在其他机器访问页面不会自动连接到部署机器。身份参数不代表登录认证。

生产运行不需要 Node 常驻：部署 Go 程序与 `web/dist`，从仓库根目录启动；其他目录可指定 `-web-dir <构建目录绝对路径>`。未构建页面时，Go 服务记录提示并继续支持终端。只提供首页与构建资源，不提供源文件或目录浏览。

前端开发可在 `web` 内运行 `npm run dev`，按输出的本机地址打开页面，再连接已有的 Go 服务。`npm test` 是串行单元测试；`npm run build` 同时检查类型。浏览器回归在 GitHub Actions 中用单个 Chromium worker 和模拟 WebSocket 执行，不依赖 MySQL、Redis 或 Kafka。

消息按接收顺序批量显示，滚动到旧消息时不会强制跳回底部。为保持简单，会话内容一直保留到下一次连接，没有历史存储、消息淘汰或虚拟列表框架；长时间持续接收大量消息会增加浏览器内存。UID 和 uint64 统计按原始精度处理。浏览器网络错误只展示实际可用的信息，不推断底层失败原因。

## 数据库配置与运行限制

服务端、消费者和迁移命令均要求设置 `DANMAKUFLOW_MYSQL_DSN`，不再使用内置数据库地址或口令。正常启动只连接数据库并检查三张表的必需列，不执行建表、改表或数据重置；缺少表或列时启动失败。

首次初始化由独立的 `go run ./cmd/migrate` 显式补建缺失表。它不创建数据库，不修改已有表，也不提供清空数据的命令。配置示例、账户权限、已有数据库接入和检查范围见 [数据库操作指南](docs/DATABASE.md)。

本次数据库修复仅允许单进程轻量测试与编译；真实 MySQL 验证、基础设施启动、压测和基准测试仍暂停。启动检查通过不代表已验证真实数据库上的完整读写兼容性。

服务入口是 `cmd/server`、`cmd/consumer`；根目录 `main.go` 仅输出运行时信息。性能优化是后续工作目标，现阶段不承诺未经测量的连接数或吞吐量。

## 许可

采用 [MIT License](LICENSE)，分发时保留已有版权和许可声明。
