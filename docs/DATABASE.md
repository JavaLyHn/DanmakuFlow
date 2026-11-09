# 数据库操作指南

服务端和消费者启动时调用 `OpenDB()` 与 `CheckSchema()`：连接 MySQL，检查当前模型需要的表和列，再初始化 Redis、Kafka 等运行组件。数据库启动路径不执行 DDL 或数据写入，也不清空已有数据。消费者启动后的消息持久化仍会正常写入数据库。

## 连接配置

`cmd/server`、`cmd/consumer`、`cmd/migrate` 均从必填环境变量 `DANMAKUFLOW_MYSQL_DSN` 读取 MySQL 驱动 DSN。未配置、格式非法或未指定数据库名时直接报错，没有内置地址、数据库或口令回退。配置错误不会回显 DSN；不要把真实 DSN 写进仓库、日志或问题报告。

以下 PowerShell 示例从仓库根目录执行，所有尖括号内容都是占位符，使用时通过本地安全配置或部署凭据机制替换。配置只对当前 PowerShell 会话及其子进程生效。

```powershell
$env:DANMAKUFLOW_MYSQL_DSN = '<APP_USER>:<APP_PASSWORD>@tcp(<DB_HOST>:<DB_PORT>)/<DB_NAME>?charset=utf8mb4&timeout=5s&readTimeout=5s&writeTimeout=5s'
```

连接强制启用 `parseTime=true`，即使 DSN 指定 `parseTime=false` 也会覆盖。连接、读取和写入超时分别对应 `timeout`、`readTimeout`、`writeTimeout`；默认各为 5 秒，可以通过 DSN 指定其他正时长。

## 首次初始化

1. 由数据库管理员预先创建目标数据库并准备独立的迁移账户。`cmd/migrate` 不创建数据库。
2. 核对目标库，使用迁移账户显式运行初始化命令：

```powershell
$env:DANMAKUFLOW_MYSQL_DSN = '<MIGRATION_USER>:<MIGRATION_PASSWORD>@tcp(<DB_HOST>:<DB_PORT>)/<DB_NAME>?charset=utf8mb4'
go run ./cmd/migrate
```

3. 初始化成功后，改回运行账户的 DSN。在已经另行准备好所需基础设施的环境中，按需启动服务端或消费者；各进程都必须获得自己的环境配置：

```powershell
$env:DANMAKUFLOW_MYSQL_DSN = '<APP_USER>:<APP_PASSWORD>@tcp(<DB_HOST>:<DB_PORT>)/<DB_NAME>?charset=utf8mb4'
go run ./cmd/server
# 消费者应在另一个已配置环境变量的终端或独立进程中运行：
# go run ./cmd/consumer
```

迁移命令固定执行三条 `CREATE TABLE IF NOT EXISTS`，只补建 `danmaku_messages`、`rooms` 和 `users`，新表使用 `utf8mb4`；随后执行 `CheckSchema()`。已有表的内容、字段、索引和字符集不会被修改。该命令不是完整的版本迁移框架，未来模型变更需要另行审阅相应迁移。

`go run ./cmd/migrate -h` 仅显示帮助，不连接数据库。正常执行不接受位置参数，也没有 reset 选项。

## 已有数据库

接入前先核对备份、表结构和账户权限。完整且兼容的已有库不需要重新初始化，运行账户的启动检查通过后即可继续启动。缺失表可由迁移账户显式补建；已有表缺少字段时，`migrate` 也不会自动补列。

`CheckSchema()` 仅对以下必需列执行 `SELECT ... LIMIT 0`，不读取已有消息记录：

| 表 | 必需列 |
| --- | --- |
| `danmaku_messages` | `id`、`room_id`、`user_id`、`content`、`send_time` |
| `rooms` | `id`、`room_number` |
| `users` | `id`、`username` |

表或列缺失、查询权限不足或查询失败都会阻止启动。检查通过只证明这些列可查询，不能证明字段类型、长度、索引、约束或额外必填列完全兼容，也不能证明业务写入成功。已有结构不兼容时，需要先审阅并实施单独的迁移，再验证兼容性；本项目不提供 reset 或删表重建命令。

## 失败处理

| 失败情况 | 处理方式 |
| --- | --- |
| DSN 缺失、格式非法或数据库名为空 | 修正当前进程环境配置，不要在排查输出中暴露口令 |
| 连接失败或超时 | 核对主机、端口、已创建的数据库、凭据和网络可达性；必要时设置正值超时 |
| 启动检查失败 | 核对报错表的必需列和查询权限；缺失表显式初始化，已有表不兼容则另行迁移 |
| 迁移中途失败 | 排除对应的权限、连接或结构问题后重跑；已经创建的表会保留，重跑可继续补齐缺失表 |
| 建表结束后检查失败 | 已有表可能缺列或不可查询；命令失败不会回滚已创建的表，也不会自动修复已有表 |

MySQL DDL 可能部分成功，不能把三条建表语句视为一个可整体回滚的事务。命令成功后仍应以运行账户检查权限，并在另行授权的隔离环境验证实际业务读写。

## 账户权限

- 运行账户不授予 `DROP`、`ALTER` 或 `CREATE`。启动检查需要对三张表的必需列有 `SELECT` 权限；消费者业务持久化还需要对 `danmaku_messages` 有 `INSERT` 权限。按各进程实际用途分配权限。
- 迁移使用独立账户，仅在目标数据库内授予所需 `CREATE` 与 `SELECT` 权限；不需要 `DROP`、`ALTER` 或删除数据的权限。服务端、消费者不要沿用迁移账户。
- 变更已有结构时另行审阅操作与权限，避免为方便运行而长期扩大应用账户权限。

## 验证范围与时间

2026-11-06 的数据库修复阶段仅授权单进程轻量测试与编译。SQL 边界测试使用真实 GORM MySQL 方言和 `sqlmock`，检查启动只读、错误传递、重复初始化及部分失败等行为；这一阶段未连接真实数据库或启动基础设施。这些模拟测试本身不能证明真实 MySQL 的 DDL、权限、字符集或业务读写兼容性。

2026-11-09 按后续 Docker 单实例功能验证授权，在独立 Docker 命名卷中完成真实初始化、消息写入和停服重启检查。显式 `migrate` 任务以 0 退出；两个浏览器页面共发送 10 条弹幕并完成 2 次点赞，最终 MySQL 精确保留 10 条弹幕。停服前已查询的 8 条记录，重启后的主键、房间、用户 ID、内容和发送时间全部一致；停服前最后一条及重启后新增的一条也均落库，点赞没有产生弹幕记录。

随后主目录入口验收复用同一 Docker 数据卷，确认上述 10 条记录全字段保留，并验证新增的第 11 条消息落库。此次 Docker 验收未访问或修改原生 MySQL 数据及 Kafka 备份，没有导入旧数据。结果只覆盖本次容器开发配置，未验证最小权限账户、任意已有表结构兼容性或故障注入场景；压测和基准测试仍未运行。正常启动依然只执行 `OpenDB()` 与 `CheckSchema()`，建表只属于显式迁移入口。具体结果与此前单次授权的历史验证见 [开发记录](DEVELOPMENT.md)。
