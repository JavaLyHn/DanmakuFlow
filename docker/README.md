# 本地 Docker 后端

使用 Docker Desktop 的 **Linux containers** 模式。五个常驻服务为 MySQL、Redis、Kafka、server、consumer；终端客户端仍在宿主机运行，启停页面使用 <http://127.0.0.1:4174>，后端运行后也可通过 <http://127.0.0.1:8080> 访问互动页面。项目名由启动器固定传入 `danmakuflow-local`，没有固定 `container_name`。

启动、停止和状态查询通过宿主机启动器完成。启动器逐个创建服务，记录容器身份和本次运行标识，按依赖顺序检查就绪；Compose 不自行连带创建依赖，不自动执行初始化任务。每次运行的控制目录由启动器挂载到 `/run/danmakuflow`，server、consumer 以 Go 程序本身作为 PID 1 响应停止信号和控制请求。

## 配置和首次初始化

首次依次执行 `scripts/setup-docker.ps1`、`scripts/build-docker.ps1`、`scripts/init-docker.ps1`。配置与初始化时，旧的 4174 启动器必须已经退出；关闭网页不会退出启动器。配置脚本独占该端口，防止覆盖正在使用的配置。日常使用根目录 `Start-DanmakuFlow.cmd` 打开页面。Docker 模式不继承终端中其他项目的 DSN。

`.local/docker/docker.env` 是机器本地文件，不能提交；它必须提供以下两个值，Compose 没有默认密码：

- `MYSQL_ROOT_PASSWORD`：专用于这个本地容器数据库的密码。
- `DANMAKUFLOW_MYSQL_DSN`：指向 `tcp(mysql:3306)/danmakuflow` 的 Go MySQL DSN；用户名和密码必须与容器中的账户一致。`mysql` 是容器网络内的服务名，不是宿主机地址。宿主机的数据库检查使用 `127.0.0.1:3306`，不得复用现有 MySQL 数据目录或数据库连接配置。

MySQL 官方入口程序只在全新 `mysql-data` 卷中创建数据库及账户；它不创建应用表。已有卷中的账户密码不会因修改环境文件而自动改变。`MYSQL_ROOT_HOST=%` 允许应用容器连接，本机发布端口仍只绑定 loopback。

配置和镜像构建完成、Docker Linux 引擎就绪后，显式运行初始化入口：

```powershell
.\scripts\init-docker.ps1
```

该脚本只调用宿主机 launcher 的 `-initialize-docker` 模式，使用已保存的 DPAPI 数据库连接；不构建镜像、不打开浏览器。初始化期间会独占 4174 端口，因此应先通过页面停止本次服务并关闭页面启动器。存在未确认停止的 owned 实例时，初始化会拒绝执行；从容器 receipt 恢复出的实例也先保存到同一份 `.local/runtime/instances.json`，供页面核查和停止。

初始化会按顺序启动或复用 MySQL、Redis、Kafka，只检查基础协议就绪，再运行 `setup` profile 下的 `migrate`、`kafka-init` 两个任务。每一步最多等待 300 秒，初始化启动阶段总上限 20 分钟。两个任务也使用同一份容器身份、创建意图和停止记录，不创建无法追踪的一次性容器。`migrate` 使用 `cmd/migrate`，只创建缺失的应用表，保留已有表。`kafka-init` 使用 Kafka 自带管理工具的 `--create --if-not-exists`：缺失时创建 `danmaku_save_topic`（1 个分区、1 个副本），已存在时不改变分区数或配置。

成功后，初始化入口会确认任务正常退出，并正常停止本次创建的基础服务；复用的外部基础服务保持运行。随后可打开 `Start-DanmakuFlow.cmd`，在页面上启动五个常驻服务。常规启动仅连接并检查表结构和 topic；缺少它们时失败，不自动运行初始化任务，普通启动的超时设置也不变。

初始化或停止失败时，保留已记录的任务和依赖，不强杀容器。重新打开本地页面，可查看 `数据库初始化`、`Kafka 初始化` 的日志，并使用原有“停止本次服务”操作收尾；完成停止后再显式重试初始化。超时、OOM 或非零退出码不会被当成初始化成功。

这个 Compose 文件不包含删除数据、重置表或自动迁移入口。停机应先让 server 停止接收并完成发送，再让 consumer 消费并提交剩余消息，最后停止基础服务；请使用启动器保留这一步骤和本次运行的所有权检查。

容器身份同时核对完整 ID、启动时间和本地私有记录。若容器启动后，Docker 检查和记录保存均未能完成，启动时间无法确认，启动器会保留该容器并提示人工核对；不会把它或外部重新启动的实例直接当成本次正常运行。已有且可验证的更新记录可以在启动器重开后恢复。

## 数据与资源

| 服务 | 镜像/运行方式 | 内存上限 | CPU 上限 | 宿主机端口 |
| --- | --- | --- | --- | --- |
| MySQL | `mysql:8.4.13` | 512 MiB | 0.50 | `127.0.0.1:3306` |
| Redis | `redis:7.4.12-alpine3.21` | 64 MiB | 0.25 | `127.0.0.1:6379` |
| Kafka | `apache/kafka:4.1.2`，单节点 KRaft | 768 MiB | 0.75 | `127.0.0.1:9092` |
| server | `danmakuflow-local-app:dev` | 192 MiB | 0.50 | `127.0.0.1:8080` |
| consumer | 同一应用镜像 | 192 MiB | 0.50 | 无 |

常驻容器配置的内存上限合计 **1728 MiB**；这不是实测占用，也不含 Docker Desktop/WSL 虚拟机、镜像构建、终端客户端或初始化任务。Go 服务设置 `GOMAXPROCS=2`、`GOMEMLIMIT=128MiB`，Kafka 堆为 128–256 MiB。初始化任务应串行运行。该配置面向本机功能开发，没有负载或吞吐量验证。

MySQL 和 Kafka 使用 Docker 管理的 `mysql-data`、`kafka-data` 命名卷，保留到下一次启动；Kafka 数据不绑定到 Windows/NTFS 目录。Kafka 的固定集群 ID 必须与卷保持对应，不要在保留卷时随意更改。Kafka 对应用发布 `kafka:29092`，对宿主机客户端发布 `127.0.0.1:9092`，controller 的 9093 端口不发布。

Redis 关闭 RDB/AOF，`/data` 使用 tmpfs；重启会丢失点赞计数、在线状态等缓存数据。`maxmemory=32mb` 且 `noeviction`，达到上限会拒绝新增写入。所有服务均为 `restart: "no"`，Docker `local` 日志驱动每容器最多 2 个 5 MiB 日志文件。

## 镜像构建和静态检查

Dockerfile 用固定版本 Node 24 构建前端，再用 Go 1.25 构建 Linux 静态程序；三个 Go 程序以 `-p 1` 串行编译。最终 scratch 镜像含程序、前端、CA 证书和项目 LICENSE，不含源代码、编译器或本机配置。Compose 提供临时 `/tmp`，日志直接写标准输出/标准错误。构建会下载公开依赖，但不会连接数据库或运行应用。

装好 Docker Compose 后，可只校验配置（不创建或启动容器）：

```powershell
docker compose --project-name danmakuflow-local --env-file .local/docker/docker.env --profile setup config --quiet
```

不要将不带 `--quiet` 的展开配置上传或粘贴到日志，它包含本地数据库凭据。镜像构建与真实容器启动必须分别验证；静态配置检查不能证明服务已经正常启动。

镜像版本及配置依据：[Docker 官方 MySQL 清单](https://github.com/docker-library/official-images/blob/master/library/mysql)、[Redis 清单](https://github.com/docker-library/official-images/blob/master/library/redis)、[Node 清单](https://github.com/docker-library/official-images/blob/master/library/node)、[Go 官方镜像](https://hub.docker.com/_/golang)、[Apache Kafka 4.1 配置](https://kafka.apache.org/41/configuration/broker-configs/)、[Kafka 4.1.2 官方镜像源码](https://github.com/apache/kafka/blob/4.1.2/docker/jvm/Dockerfile)。
