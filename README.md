# DanmakuFlow

Go 直播弹幕后端，由 JavaLyHn 维护。支持 WebSocket 房间、弹幕广播、点赞与在线连接数统计，使用 Redis 进行跨节点分发，使用 Kafka 和 MySQL 完成异步持久化。

当前以 **Go 版本为开发主线**。[DanmakuFlow-Java](https://github.com/JavaLyHn/DanmakuFlow-Java) 保留 Java 实现并暂停开发；只有收到明确的同步指令后，才将选定的 Go 改动同步到 Java。

仓库拆分、验证边界和后续事项见 [开发记录](docs/DEVELOPMENT.md)。

## 代码结构

| 路径 | 用途 |
| --- | --- |
| `cmd/server` | Gin / WebSocket 网关，入口为 `/ws` |
| `cmd/consumer` | Kafka 消费与 MySQL 批量持久化 |
| `cmd/migrate` | 显式补建缺失的 MySQL 表，保留已有表 |
| `cmd/client` | 交互式 WebSocket 客户端 |
| `cmd/benchmark` | 已有压测工具，本轮不运行 |
| `internal/ws` | 连接、房间与消息分发 |
| `internal/infra` | Redis、Kafka、MySQL 连接与表结构检查 |
| `internal/repo` | 数据库访问 |
| `internal/service` | 业务服务 |

Go 模块要求版本为 1.25.0。主要依赖为 Gin、Gorilla WebSocket、go-redis、Sarama 和 GORM。

协议地址示例：`ws://127.0.0.1:8080/ws?uid=1001&name=demo&room=1001`。101 为弹幕、102 为房间统计、103 为点赞。`uid` 是客户端提供的身份参数，尚无账号认证。回放 HTTP 接口当前未启用。

## 数据库配置与运行限制

服务端、消费者和迁移命令均要求设置 `DANMAKUFLOW_MYSQL_DSN`，不再使用内置数据库地址或口令。正常启动只连接数据库并检查三张表的必需列，不执行建表、改表或数据重置；缺少表或列时启动失败。

首次初始化由独立的 `go run ./cmd/migrate` 显式补建缺失表。它不创建数据库，不修改已有表，也不提供清空数据的命令。配置示例、账户权限、已有数据库接入和检查范围见 [数据库操作指南](docs/DATABASE.md)。

本次数据库修复仅允许单进程轻量测试与编译；真实 MySQL 验证、基础设施启动、压测和基准测试仍暂停。启动检查通过不代表已验证真实数据库上的完整读写兼容性。

服务入口是 `cmd/server`、`cmd/consumer`；根目录 `main.go` 仅输出运行时信息。性能优化是后续工作目标，现阶段不承诺未经测量的连接数或吞吐量。

## 许可

采用 [MIT License](LICENSE)，分发时保留已有版权和许可声明。
