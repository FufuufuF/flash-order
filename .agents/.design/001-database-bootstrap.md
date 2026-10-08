# 001：数据库迁移与 Go 连接初始化

- 日期：2026-10-08
- 状态：已实施；验收项已于 2026-10-08 执行。
- 对应阶段：单体最小版之前的数据库接入小阶段。

## 1. 要解决的问题与完成目标

数据库容器运行起来，只代表 MySQL 进程可用。项目还需要两项能力：

1. 用可追踪的步骤建立商品、库存和订单表，并知道数据库结构处于哪个版本。
2. Go 程序启动时连接指定数据库，确认连接可用，并在退出时释放连接池。

本阶段完成标准：命令行迁移工具成功建立三张业务表；Go 程序能确认连接并持续等待退出信号；错误配置或数据库不可用时启动失败；退出时关闭连接池。

这次只实现数据库接入骨架。业务 HTTP 接口、下单与查询 SQL、商品与 Worker 独立进程、Redis、事务扣库存、请求幂等和并发保护属于后续设计。本阶段结束后还不能创建订单，也不能宣称已满足 AGENT.md 的最终正确性要求。

## 2. 当前环境与依赖选择

编写文档时已观察到：

- 仓库尚无 Go 源码、go.mod 或迁移文件。
- Go 为 1.27.1，运行于 macOS arm64。
- 已有 `deploy/compose/compose.dev.yaml`，使用 `mysql:8.4`。
- Compose 项目名为 `flash-order-dev`，MySQL 服务名为 `mysql`。
- 宿主机端口配置为 `127.0.0.1:${MYSQL_PORT:-3306}:3306`。
- Compose 默认数据库名和普通用户为 `flash_order`；实际值以本机配置为准。
- 本次检查未发现可用的 `migrate` 命令；实现时需要安装并确认其可执行路径。

实现前重新检查 Docker 容器状态和实际端口。容器曾健康不代表当前能从 Go 程序成功连接；Go 连接实验需要使用普通开发用户的真实配置。

选择：

| 依赖 | 约定 | 理由 |
| --- | --- | --- |
| Go 数据库接口 | 标准库 `database/sql` | 明确学习连接池、context 和错误处理 |
| MySQL 驱动 | `github.com/go-sql-driver/mysql` v1.10.1 | 将 Go 数据库调用转换为 MySQL 通信；版本写入 go.mod |
| 迁移工具 | `golang-migrate` CLI v4.20.1 | 按版本执行 SQL，记录数据库结构版本 |

上述依赖版本已核对发布页。升级版本或更换工具时先更新本设计，再修改实现。运行程序依赖 MySQL 驱动；迁移 CLI 是独立开发工具，应用代码不导入迁移库。

## 3. 文件与目录

新增业务源码严格为两个 Go 文件，迁移为两个 SQL 文件；go.mod、go.sum 属于依赖管理文件。

```text
flash-order/
├── AGENT.md
├── .agents/
│   └── .design/
│       └── 001-database-bootstrap.md
├── go.mod
├── go.sum
├── cmd/
│   └── order/
│       └── main.go
├── internal/
│   └── database/
│       └── mysql.go
├── migrations/
│   ├── 000001_create_initial_tables.up.sql
│   └── 000001_create_initial_tables.down.sql
└── deploy/
    └── compose/
        ├── compose.dev.yaml
        └── .env.dev
```

| 文件或目录 | 职责及命名理由 |
| --- | --- |
| `cmd` | command 的常见缩写，集中放可执行程序入口；本阶段只有 order |
| `cmd/order/main.go` | `package main`，组装配置、连接池、日志与退出流程 |
| `internal` | Go 对内部包导入范围有特殊限制，供项目内部使用 |
| `internal/database/mysql.go` | `package database`，实现连接池建立与连接检查 |
| `migrations` | 保存版本化 SQL；目录路径显式传给 CLI |
| `000001_*.up.sql` | 版本 1：建立三张业务表 |
| `000001_*.down.sql` | 回退版本 1：删除这三张表，开发回退会丢失表内数据 |
| `go.mod` | 模块路径使用 `github.com/FufuufuF/flash-order`，记录 Go 与依赖版本 |
| `go.sum` | Go 工具生成与维护的依赖校验信息，纳入 Git |
| `deploy/compose` | 已有容器运行配置；本阶段沿用现有 MySQL 配置 |
| `.env.dev` | 本机开发配置，已被 .gitignore 排除；不把真实密码复制进设计文档或源码 |

`cmd`、`database`、`migrations`、`deploy` 都是组织约定；`internal` 有 Go 工具层面的特殊语义。布局为这个项目服务，不代表所有 Go 项目都必须如此组织。

## 4. 职责边界与启动顺序

```text
Compose 启动 MySQL，初始化开发数据库及用户
    ↓
migrate CLI 连接该数据库，执行尚未应用的迁移
    ↓
Go main 读取配置，调用 database.Open
    ↓
创建 sql.DB → 限时 PingContext → 输出连接成功日志
    ↓
等待 SIGINT / SIGTERM → 关闭连接池 → 退出
```

应用启动时不执行 CREATE TABLE、ALTER TABLE、迁移或 ORM 自动建表。原因是建表与改表有独立执行结果和版本记录，便于学习、审查与排查。初次建库与创建用户由现有 MySQL 容器初始化负责；迁移负责该库中的表。

Ping 成功只证明能连接数据库，不证明业务表存在。本阶段日志应使用 `database_connected`，不能宣称业务接口已就绪。用空数据库运行连接程序也可能成功，这是有意保留的职责边界。

## 5. 初始数据结构

业务假设：一笔订单购买一种商品，允许多件；每个商品一份库存；orders 表代表成功购买。商品资料与库存分开，是为后续服务归属明确，并非范式要求一对一关系必须拆表。

所有业务表使用 InnoDB 和 utf8mb4。普通文本使用 `utf8mb4_unicode_ci`。ID 统一使用有符号 BIGINT，便于后续映射 Go int64；自增商品及订单 ID 从正数开始。数量使用有符号 INT。

### products

| 字段 | 类型与约束 | 为什么 |
| --- | --- | --- |
| id | BIGINT，PRIMARY KEY，AUTO_INCREMENT | 用稳定标识查商品；名称可改且不要求唯一 |
| name | VARCHAR(128)，NOT NULL，无默认值 | 保存商品名称，长度为当前 Demo 的明确边界 |
| is_active | BOOLEAN，NOT NULL，DEFAULT TRUE；CHECK 值属于 0、1 | 表达可售与不可售；MySQL BOOLEAN 别名本身不会限制只能存 0、1 |

### inventory

| 字段 | 类型与约束 | 为什么 |
| --- | --- | --- |
| product_id | BIGINT，PRIMARY KEY，不自增 | 一种商品只有一份库存，复用商品 ID 就能唯一标识这一行 |
| available | INT，NOT NULL，无默认值 | 库存必须显式初始化；有符号类型便于后续观察错误扣减结果 |

版本 1 暂不添加 available >= 0 的 CHECK。后续会先观察简单扣减的错误，再设计正确的扣减语句、事务及最终约束。此处的实验性结构不具备防负库存或防超卖能力。

### orders

| 字段 | 类型与约束 | 为什么 |
| --- | --- | --- |
| id | BIGINT，PRIMARY KEY，AUTO_INCREMENT | 用户以后凭订单 ID 查询结果 |
| request_id | VARCHAR(64)，CHARACTER SET ascii，COLLATE ascii_bin，NOT NULL；暂不唯一 | 保存请求标识；区分大小写；后续重复请求实验后再引入唯一约束 |
| product_id | BIGINT，NOT NULL | 保存此次购买的商品标识 |
| quantity | INT，NOT NULL；CHECK quantity > 0 | 一笔购买必须至少买一件 |
| created_at | DATETIME(6)，NOT NULL，无默认值 | 保存微秒精度的创建时间；后续写入方显式提供 UTC 时间，避免依赖数据库会话时区 |

约定后续客户端请求 ID 为 1–64 个 ASCII 字符，由接口层校验长度与非空；当前只定义存储结构。版本 1 尚无业务写入逻辑，也不生成测试订单。

当前关系由 product_id 表达，不添加 inventory/orders 指向 products 的数据库外键，理由是后续商品资料将由独立服务拥有。代价是数据库暂时不能阻止悬空商品 ID；后续业务代码必须检查商品存在，本项目当前没有删除商品的业务。商品存在不代表库存行必然存在，后续下单实现须明确处理库存行缺失。

索引只建立上述主键。现阶段目标是按 ID 查找；request_id 唯一索引及其他索引随对应查询和实验另写设计。orders 当前没有状态、金额或用户字段，因为这一阶段没有订单状态变化、计价或账户业务。

## 6. 两个迁移文件的要求

up 文件按 products、inventory、orders 顺序建表，使用普通 CREATE TABLE。重复执行由 migrate 的版本记录防止，不使用 IF NOT EXISTS 掩盖表结构冲突。迁移只建业务表，不包含 CREATE DATABASE、用户授权或测试数据。

down 文件按 orders、inventory、products 顺序删表，仅操作本版本创建的三张业务表，不删除数据库或迁移工具的版本表。开发回退前明确确认目标是可丢弃数据的开发实例。

migrate 默认用 `schema_migrations` 记录当前版本和 dirty 状态；这张工具表由 CLI 自己管理，不算第四张业务表。

MySQL 多项 DDL 不应被当成一个可整体回滚的业务事务。迁移中途失败可能留下部分表和 dirty 状态。此时先检查真实表结构与错误，再决定修复方式；不能直接 force 版本或自动重试以掩盖半完成的结构。

已应用并共享的迁移文件不再修改。新增字段、约束或表写进更高版本的迁移；先更新对应设计。down 恢复表结构的过程不会恢复被删掉的数据。

## 7. 配置传递

Go 程序读取进程环境变量，不自动加载 .env 文件，也不引入 dotenv 库。main 负责默认值与校验，database 包接收明确的 Config，不在包里读取环境变量。

| 环境变量 | 本阶段约定 |
| --- | --- |
| MYSQL_HOST | 缺省为 127.0.0.1，Go 运行在 Mac 主机上 |
| MYSQL_PORT | 缺省为 3306；设置时必须是 1–65535 的整数，且对应宿主机映射端口 |
| MYSQL_USER | 必填，使用 Compose 创建的普通开发用户 |
| MYSQL_PASSWORD | 必填，读取本机配置，不允许写入源码或日志 |
| MYSQL_DATABASE | 必填，与实际开发数据库一致 |
| MIGRATION_DATABASE_URL | 仅供 migrate CLI 使用，格式见下方；应用不读取 |

Compose 的 --env-file 不会把变量自动传给宿主机上的 Go。启动 Go 前须将上表变量导出到当前 shell；可以复用本机 `.env.dev` 中已有的值。若采用 shell source，先确认文件是可信的、兼容 shell 的简单赋值文件，再用 set -a 导出；不在设计或运行日志中打印真实值。

迁移 URL 格式为 `mysql://user:password@tcp(127.0.0.1:port)/dbname`。它与 Go 驱动的 DSN 格式不同；密码中的 URL 保留字符须按工具的 URL 格式编码。URL 保存在本机环境变量中，不提交 Git。

Go 使用驱动的 NewConfig 与 FormatDSN 生成 DSN，不靠手工拼接账号密码。设置 Net=tcp、Addr、User、Passwd、DBName、ParseTime=true、Loc=UTC；本阶段不启用 MultiStatements。配置中的密码、完整 Config、DSN 和迁移 URL 均不可输出到日志。

## 8. 两个 Go 文件的实现契约

### internal/database/mysql.go

提供 Config，字段为 Host、Port、User、Password、Database；Host/User/Password/Database 为 string，Port 为 int。

提供函数签名：

```go
func Open(ctx context.Context, cfg Config) (*sql.DB, error)
```

执行顺序：

1. 先用 mysql.NewConfig() 建立驱动配置，保留 CheckConnLiveness、AllowNativePasswords 等默认值；再设置本设计指定的字段，用 net.JoinHostPort 组织地址，用 FormatDSN 创建 DSN。
2. 驱动连接超时设置为 3 秒，读写超时各为 5 秒。
3. 调用 sql.Open 得到连接池；不能把返回成功视为已建立真实连接。
4. 设置连接池：最大打开连接数 5，最大空闲连接数 5；其他池参数暂用默认值，不做性能调优。
5. 用传入的 ctx 调用 PingContext。
6. Ping 失败时先关闭已创建的连接池，再返回错误；成功时返回连接池，由调用方负责关闭。

该函数不读取环境变量、不终止进程、不输出日志、不迁移、不执行业务 SQL。错误应指出阶段并保留可判断的原因，例如“检查数据库连接失败”；不附带原始 DSN 或密码，也避免原样输出可能包含配置值的第三方错误文本。

### cmd/order/main.go

main 调用本文件中的 run 函数，run 返回 error。清理在 run 内通过 defer 完成；若失败，main 在 run 返回之后输出脱敏错误并以非零状态退出，避免 os.Exit 跳过资源清理。

执行顺序：

1. 读取并校验上表环境变量。缺少必填值或端口格式错误时，在建立连接前失败。
2. 用 signal.NotifyContext 订阅 SIGINT、SIGTERM，并 defer 停止订阅。
3. 从该 context 派生 3 秒启动检查 context，调用 database.Open，然后释放启动超时 context。
4. 成功后安排连接池关闭，并输出结构化 JSON 日志事件 database_connected；用标准库 log/slog，不打印配置对象。
5. 使用信号 context 等待退出，不用已到期的启动检查 context 等待退出。
6. 收到退出信号，输出退出日志并关闭连接池；关闭失败要记录脱敏错误。

本阶段没有 HTTP 监听端口。这个持续运行的程序验证订单服务将来的数据库启动和退出骨架；它不检查业务表版本，也不执行轮询 SQL 或启动 goroutine 来保持存活。

## 9. 实现顺序与运行命令

所有命令在仓库根目录执行；以下是实施指导，写设计文档时不执行安装、迁移或数据库变更。

1. 确认 Docker/MySQL 状态与开发库配置。
2. 安装 CLI，并确认其路径；固定 v4.20.1，避免依赖 latest 漂移。

   ```sh
   go install -tags mysql github.com/golang-migrate/migrate/v4/cmd/migrate@v4.20.1
   ```

   若 migrate 不在 PATH，使用 `go env GOPATH` 下的 bin/migrate，或实际配置的 GOBIN 路径。安装 CLI 本身不应把迁移库加入应用依赖。

3. 初始化模块，固定驱动依赖：

   ```sh
   go mod init github.com/FufuufuF/flash-order
   go get github.com/go-sql-driver/mysql@v1.10.1
   ```

4. 按第 5、6 节手写两个 SQL 文件，由学习者解释字段、约束与回退操作，再运行迁移。
5. 按第 7、8 节实现 mysql.go 与 main.go，格式化源码并运行 go mod tidy。
6. 执行第 10 节的验收，记录实际结果后完成本阶段。

本次实施实际环境：Docker Compose 中的 `flash-order-dev-mysql-1` 健康运行，宿主机映射为
`127.0.0.1:3306`。`migrate` v4.20.1 已安装在 `/Users/royce/go/bin/migrate`，不在当前
`PATH` 中，因此验收命令使用该绝对路径；应用依赖仍只有 `github.com/go-sql-driver/mysql`
v1.10.1。

启动与检查现有容器：

```sh
docker compose --env-file deploy/compose/.env.dev -f deploy/compose/compose.dev.yaml up -d mysql
docker compose --env-file deploy/compose/.env.dev -f deploy/compose/compose.dev.yaml ps
```

导出开发配置与 MIGRATION_DATABASE_URL 后执行：

```sh
migrate -path ./migrations -database "$MIGRATION_DATABASE_URL" up
migrate -path ./migrations -database "$MIGRATION_DATABASE_URL" version
go run ./cmd/order
```

该 URL 通过 CLI 参数传递，运行时可能出现在本机进程参数中；本阶段只使用可丢弃开发账号，不复用生产凭据。

Go 的 DSN 与迁移 URL 必须指向同一宿主机端口和数据库。CLI 若不在 PATH，按前文换成其实际路径。文档中的目录均指项目相对路径。

## 10. 验收实验

每项先预测结果，再执行；实施后填写结果。表结构检查可通过已有 SQL 客户端，或 Compose exec mysql 使用 mysql 客户端和交互式密码输入完成。不要把真实密码写在命令文本中。

| 验收项 | 操作与预期 | 实际结果 |
| --- | --- | --- |
| 首次迁移 | 在没有业务表的开发库运行 up；出现 products、inventory、orders 和工具版本表；version 为 1 且非 dirty | 通过：`1/u create_initial_tables`；三张业务表和 `schema_migrations` 存在，version 为 `1`、dirty 为 `0`。 |
| 表结构 | SHOW CREATE TABLE 核对类型、主键、默认值与 CHECK；request_id 没有唯一约束，product_id 没有外键，available 暂无非负 CHECK | 通过：实际 DDL 与设计一致；主键、字符集、默认值和两个 CHECK 正确，未发现 request_id 唯一约束、外键或 available 非负 CHECK。 |
| 重复迁移 | 再运行 up；提示无新迁移可执行，表结构与版本不变；记录工具实际输出和退出码，不把“无变化”误认为建表失败 | 通过：输出 `no change`，退出码为 0，版本仍为 `1`。 |
| 正常连接 | 正确配置启动 Go；看到 database_connected，进程等待退出，不执行建表或业务写入 | 通过：输出 JSON 事件 `database_connected` 并等待信号；数据库中没有应用写入。 |
| 配置缺失 | 在单独 shell 中去掉 MYSQL_USER 或 MYSQL_DATABASE；明确指出缺失字段并非零退出，日志无密码 | 通过：去掉 `MYSQL_USER` 后输出脱敏错误“缺少环境变量 MYSQL_USER”，退出码为 1。 |
| 连接失败 | 在单独 shell 使用一个确认未监听的端口；连接在超时边界内失败，非零退出，不出现成功日志 | 通过：使用未监听的 `33306` 端口立即失败，退出码为 1，无 `database_connected`，日志无密码。 |
| 失效连接恢复（审查补充） | 临时验收程序调用真实 database.Open；终止其自行创建的空闲连接，再执行 SELECT 1；应自动换用可用连接并成功，不终止其他会话，不修改业务数据 | 通过：真实 database.Open 创建的空闲连接被终止后，SELECT 1 自动使用可用连接并成功；临时程序已移除，未修改业务数据 |
| 信号退出 | 构建到临时目录并运行可执行文件，发送 Ctrl+C/SIGINT 或 SIGTERM；观察退出流程与连接池关闭 | 通过：Ctrl+C 后依次输出 `shutdown_requested`、`database_closed`，退出码为 0。 |
| 构建与静态检查 | gofmt 格式化两个文件；go build ./... 与 go vet ./... 通过 | 通过：`gofmt -l` 无输出，`go build ./...`、`go vet ./...` 和 `go test ./...` 均通过。 |

信号验收建议直接运行编译出的程序，避免把 go run 包装进程的行为误认为应用行为。可用 `go build -o /tmp/flash-order-db-bootstrap ./cmd/order`，然后运行该临时可执行文件。

down/up 回退实验只在尚无需要保留的业务数据的开发库执行：down 1 删除三张业务表，再 up 重建，版本恢复为 1。如已有需要保留的数据，跳过破坏性回退，先修改验收环境设计，再执行；不能为验证方便清空现有数据。

迁移命令中的连接信息可能由 CLI 错误信息输出；分享输出前检查并脱敏。应用日志不得包含密码或完整连接字符串。

## 11. 完成记录与后续阶段

- [x] 两个迁移 SQL 文件完成并审查。
- [x] 两个 Go 文件完成并审查。
- [x] 配置传递、迁移与连接正常路径通过。
- [x] 配置失败、连接失败、信号退出实验通过。
- [x] 构建与静态检查通过，实际结果已填写。

审查修正（2026-10-08）：发现 mysql.Config{} 会关闭默认的连接存活检查。按原设计改为 mysql.NewConfig() 后逐项设置字段，并对实际 database.Open 进行失效连接恢复复验。修正与复验通过：失效空闲连接恢复、应用正常启动与 SIGTERM 退出均成功；缺少 MYSQL_USER、非法 MYSQL_PORT、错误密码均以退出码 1 失败且日志不泄露密码。构建、go vet 与 go test 通过；当前没有持久化测试文件，go test 的通过表示包编译通过。

补充回退实验：开发库当时没有需要保留的业务数据，执行 `down 1` 后再执行 `up` 成功，
版本恢复为 `1`，三张业务表重新存在。

后续写新的设计文档，再实现正常下单与查单。事务扣库存、并发正确性、request_id 唯一约束与冲突处理按 AGENT.md 的问题实验顺序推进。数据库连接成功不等于这些能力已经完成。

## 12. 参考依据

- [Go 项目布局建议](https://go.dev/doc/modules/layout)：cmd 与 internal 的组织理由。
- [Go 数据库连接文档](https://go.dev/doc/database/open-handle)：sql.DB、sql.Open 与连接检查。
- [迁移文件格式](https://github.com/golang-migrate/migrate/blob/master/MIGRATIONS.md)：版本化 up/down 文件。
- [迁移 CLI 文档](https://github.com/golang-migrate/migrate/blob/master/cmd/migrate/README.md)：安装与 up/version 命令。
- [迁移工具 MySQL 驱动](https://github.com/golang-migrate/migrate/blob/master/database/mysql/README.md)：MySQL 迁移 URL 格式。
- [migrate v4.20.1](https://github.com/golang-migrate/migrate/releases/tag/v4.20.1) 与 [MySQL 驱动 v1.10.1](https://github.com/go-sql-driver/mysql/releases/tag/v1.10.1)：本设计固定的依赖版本。
