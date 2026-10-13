# 002：HTTP 下单与查单——事务、库存行锁与请求幂等

## 1. 文档状态与学习约定

- 状态：设计已确定；框架、查单代码、HTTP 配置和启停已实现，HTTP 生命周期本地测试通过；版本 2 唯一索引迁移已应用并验收，回滚事务中的重复插入实验待执行；POST HTTP handler 已实现，但真实 HTTP/数据库下单验收仍待执行；学习者反馈真实 MySQL 连接启动及 GET 非法 ID 的 400 响应符合预期，其余查单验收暂缓。
- 前置阶段：`001-database-bootstrap.md`。已有数据库连接、退出清理和版本 1 三张表。
- 本文承接最近讨论：本阶段直接实现事务、`SELECT ... FOR UPDATE` 和 request_id 唯一约束，不再从无并发保护的版本开始。
- 学习者亲手实现 TODO。辅助 agent 每次只指导一个小步骤：解释问题和取舍，给函数签名、伪代码或关键 SQL，等待学习者尝试，再审查。未经明确授权，不补齐整条链路或生成完整文件。
- 教学粒度补充：一个教学轮次只引入一个主要知识点或一组紧密相关的 API。先解释用途、输入输出和一个最小例子，再让学习者用自己的话复述或预测行为；确认理解后才进入下一个知识点或要求写代码。遇到 HTTP、事务、并发等密集章节，主动拆成多个轮次，不因同一小步属于同一功能就一次讲完全部 API。
- 目录与签名是当前约定；如果发现需要更改，先更新本文，再改代码。框架能编译不表示接口或并发正确性已完成。
- 会话分工（2026-10-12）：主线管理会话只负责串联阶段、维护交接进度和代码 review；具体开发与教学在学习者另开的专项会话中进行。专项会话按第 12 节辅导，学习者亲手实现；阶段结束更新本文，由学习者回主线会话发起 review。这是角色分工，不把所有新会话限制为只做 review。

### 当前接手摘要（2026-10-13）

- 已实现：GET 查单代码、HTTP_ADDR 和四项 Server 超时配置、HTTP 启停与数据库连接池关闭流程。最近的 build、vet 和四项 HTTP 生命周期 race 测试通过，详见第 10 节。
- 学习者反馈：MySQL 容器 healthy，宿主机映射 127.0.0.1:3306；应用连接启动和 GET /orders/abc 的 400 响应符合预期。运行环境仍需在接手时重新核对。
- 暂缓验收：GET 404/200、重启后的查单和真实 SIGTERM 下的关闭顺序。学习者已明确选择先进入下单实现，不重复把这些项目设为本轮前置任务，也不记录为通过。
- B1 实际进度：按学习者明确授权完成两个版本 2 SQL 文件及迁移，版本 1 未修改。预检查确认目标开发库 flash_order、版本 1 非 dirty、orders=0 且无重复；migrate up 1 成功应用版本 2，CLI 与数据库新查询确认 version=2、dirty=0、uq_orders_request_id(request_id) 存在且 Non_unique=0，订单数仍为 0。重复插入与回滚清理实验继续待执行，未把 B1 全部记录为通过。
- 当前教学停点：2026-10-13 学习者明确授权导师第三次 review 后直接修正 Create/Store 与 if 嵌套。学习者已先修正 ReadProduct 两列映射；导师现已将 Create 主流程展开、InsertOrder 前移至商品/库存检查前、时间统一 UTC/微秒，补齐重复键显式回滚后重查/replay、回滚失败的安全日志与依赖错误、重查意外无单的 ErrInvariantViolation；保留正确的 Commit/ErrCommitUnknown 边界。Store.InsertOrder 用 MySQL 1062+完整索引名后缀分类 request_id 冲突并保留 cause；LockInventory 只将无行归为 ErrInventoryMissing，其他错误归为 ErrStorageUnavailable。build/vet/差异检查及临时 driver 的 35 个模拟子用例（-race）通过，临时文件已清理；这些只验证 Go 逻辑，不证明真实 SQL/行锁/并发正确性。按学习者最新指示，本轮未使用真实开发库验证、未写测试业务数据。当前 C1/B3/C2/C3 的对应代码已补齐，运行验收仍待完成；下一停点为学习者对照未暂存差异理解分支与 errors.As/Is。POST、B1 重复插入回滚实验、真实下单/并发验收和暂缓 GET 项继续待执行。历史 DeductInventory 三个真实 MySQL 局部测试证据保留，见第 10 节。
- 2026-10-13 POST handler 已按用户明确授权实现：请求 Content-Type 使用 `mime.ParseMediaType`，接受带参数的 `application/json`；请求体用 `http.MaxBytesReader` 限制为 4 KiB；使用 `DisallowUnknownFields` 并二次 Decode 拒绝多个 JSON 值；调用 service 时派生 5 秒 context；新建返回 201、重放返回 200；成功与错误均通过统一 JSON 编码辅助函数输出。错误映射补齐商品不存在、商品不可售、库存不足、请求冲突、库存数据异常、提交结果未知和存储不可用等约定。未改变 Service/Store、路由或依赖。
- 新专项会话先读根目录 AGENTS.md、本文第 10 节最新记录和第 12 节教学约定，再用 git status/git diff 核对实际进度。旧记录中的“下一步”代表当时停点；当前入口以本摘要和最新记录为准。每次只推进一个 60–90 分钟内可完成的小目标，结束时回填实现、验证和遗留项。

## 2. 要解决的问题、目标与范围

实现一个 Go HTTP 进程，从 MySQL 读取商品，扣减库存并创建订单，同时支持按 ID 查单。

| 具体问题 | 本阶段机制 | 作用边界 |
| --- | --- | --- |
| 扣库存成功，创建订单或后续步骤失败 | 库存和订单在同一个事务中提交 | 保证数据库中的两种修改一起生效或撤销 |
| 两个请求都认为最后一件有货 | 在事务中锁住库存行后读库存、判断和扣减 | 同一商品购买排队；热点行会增加等待时间 |
| 同一个请求并发提交，两次预查询都没找到订单 | request_id 唯一约束；事务内先插入订单占用该键 | 最多留下一个已提交订单；预查询只是优化 |
| 订单提交后响应丢失，客户端不知道结果 | 使用原 request_id 重试并返回已有订单 | 不自动重试每一种错误；失败请求本阶段不缓存结果 |

本阶段包含 `POST /orders`、`GET /orders/{id}`、输入检查、错误语义、事务和重复请求处理。
不包含支付、用户体系、订单状态流转、商品管理 HTTP 接口、gRPC、Redis、Worker 和事件发布。
商品仍由这个单体直接查 MySQL；后续拆服务时再替换商品查询边界，不提前增加三个入口。

## 3. 业务规则与成功边界

1. 每笔订单购买一种商品，数量必须为正整数。
2. 商品必须存在且 `is_active = true`；允许已经通过可售检查的请求完成，即使随后商品被下架。不对 products 使用行锁。
3. inventory 缺行表示数据不完整，按内部错误处理；库存不足是业务拒绝。
4. request_id 在本 Demo 中全局唯一。相同键且 product_id、quantity 相同，返回已有订单；同键不同参数返回冲突，不改变原订单。
5. **订单事务成功提交才算下单成功**。先插入的订单在提交前只是未提交数据；检查失败必须回滚，因此不用 status 表示“创建了但库存还没扣”的中间状态。
6. 只有成功订单占用 request_id。库存不足等失败会回滚，之后同键可以重新尝试；这不保证“同一键的所有失败永远返回第一次失败”。
7. Commit 返回错误时，可能是提交未完成，也可能是提交完成但确认丢失。返回“结果尚未确认”，让客户端用原键重试，不宣称已经回滚，不另换键购买。

## 4. 接口与数据结构

### POST /orders

请求示例：

```json
{"request_id":"demo-001","product_id":1,"quantity":2}
```

- request_id：1–64 个 ASCII 字符，仅允许字母、数字、`-`、`_`。不自动 trim 或改大小写；这也避免数据库字符串比较对尾随空格的歧义。
- product_id：正 int64；quantity：1–2147483647，用 int32 对应 MySQL INT。
- 请求体上限 4 KiB；必须是一个 JSON 对象，拒绝未知字段、缺失字段、错误类型、多个 JSON 值或尾随非空白内容。无 Content-Type 或非 application/json 返回 415；带 charset 参数的 application/json 可以接受。
- 所有请求先检查格式。已存在的有效请求先比较参数并返回，**不重新检查当前商品是否可售或当前库存**。

新建成功返回 201；重复成功返回 200。响应结构一致：

```json
{
  "order":{"id":1,"request_id":"demo-001","product_id":1,"quantity":2,"created_at":"2026-10-09T00:00:00Z"},
  "replayed":false
}
```

created_at 使用 UTC；插入时取一次时间并截断至微秒，与 DATETIME(6) 精度一致。重放必须读取原始创建时间。

### GET /orders/{id}

id 必须是正 int64。成功返回 200，正文直接是上面的 order 对象。不存在返回 404。

### 错误响应

```json
{"error":{"code":"insufficient_stock","message":"库存不足"}}
```

| 情况 | HTTP | code |
| --- | --- | --- |
| 格式或参数非法 | 400 | invalid_input |
| 请求体超过上限 | 413 | body_too_large |
| Content-Type 不支持 | 415 | unsupported_media_type |
| 商品不存在 / 订单不存在 | 404 | product_not_found / order_not_found |
| 商品不可售 / 库存不足 | 409 | product_inactive / insufficient_stock |
| 同键不同购买参数 | 409 | request_conflict |
| 缺少库存行、扣减影响行数异常等数据问题 | 500 | internal_error |
| 数据库不可用、请求处理超时、死锁或锁等待超时 | 503 | storage_unavailable |
| 提交结果不确定 | 503 | commit_unknown |

错误正文不输出 SQL、DSN 或原始驱动错误。错误包装保留 cause 供程序分类；日志记录 request_id、order_id（若已知）、错误阶段和安全的分类，不记录密码。事务失败不在服务端自动循环重试。
客户端已取消时可能收不到响应；传递请求 context 停止数据库操作并清理事务。

## 5. 下单流程：按这个顺序实施

```text
解析、检查请求
  → 通过连接池查询 request_id
      → 找到：参数相同则返回已有订单；不同则冲突
      → 未找到：开始事务
          → 插入 orders（先占住唯一键，暂未提交）
              → 重复键：回滚 → 通过连接池重新查已有订单 → 比较并返回
              → 其他错误：回滚 → 返回错误
          → 普通查询 products，检查存在、可售
          → SELECT inventory ... FOR UPDATE，检查存在、库存足够
          → 扣减库存，检查影响行数为 1
          → Commit
          → 返回新订单
```

这个顺序对“库存只剩一件，同一个 request_id 并发到达”很重要：若先锁库存和检查数量，再插订单，第二个请求可能看到库存为零并返回库存不足，而不是原订单。本方案让同键请求先在唯一键处竞争。

插入只提前占键，不提前提交。事务里任何后续失败都撤销该插入。若首个请求回滚，等待中的另一请求可以插入并继续；若首个请求提交，另一请求收到重复键。

重复键处理必须先结束自己的事务，再用连接池查询。不要在原事务的旧快照中查，也不要持着事务占用的连接再借一条连接查重。精确识别 MySQL 1062 且对应本次 request_id 唯一键，其他错误不能冒充重复请求。

所有事务内 SQL 都使用同一个 `*sql.Tx`，不能夹杂 `store.db.Query...`。`BeginTx` 后立即安排 `defer tx.Rollback()` 兜底；Commit 成功后的重复 Rollback 可忽略。显式回滚失败需要记录并返回依赖错误，不继续重放路径。

锁库存查询通过主键 `product_id` 精确定位；使用当前锁定读取，不依赖事务外读到的库存。扣减用 `available = available - quantity`，检查发生在持锁期间，不引入第二套条件扣减方案。

商品状态采用普通读取：可售决定依据本次查询看到的值；不承诺同步阻止所有在途购买。后续缓存或跨服务查询改变新鲜度时，需要重新讨论这一规则。

## 6. 本阶段目录、文件和职责

```text
cmd/order/
  main.go                      加载数据库及 HTTP 配置、管理数据库连接池、接入 HTTP
  http.go                      HTTPConfig、组装依赖、HTTP 启停与超时
  http_test.go                  生命周期测试：真实本地 HTTP，验证监听失败、优雅关闭与强制关闭
internal/database/mysql.go     已有：连接池，不改业务职责
internal/order/
  model.go                     新框架：请求、商品、订单、结果和错误定义
  service.go                   新框架：校验、重放、事务顺序和业务判断
  store.go                     新框架：订单查询及以 Tx 为参数的 SQL 方法
internal/httpapi/
  router.go                    新框架：注册两条 HTTP 路由
  order.go                     新框架：解析 HTTP、调用 service、输出 JSON
migrations/
  000001_...up/down.sql         已应用，不修改
  000002_add_request_id_unique.up.sql    已应用版本 2：添加唯一索引
  000002_add_request_id_unique.down.sql  已编写：移除这个索引，尚未执行
.agents/.design/002-order-purchase.md
```

`order` 是一个包，三个文件协作；service 做判断和事务编排，store 执行 SQL。当前用具体类型连接，暂不为每张表抽接口，不增加 ORM 或通用框架。

事务边界说明（2026-10-12）：当前 order.Store 围绕下单业务访问 products、inventory 和 orders，不要求每张表各建一个 Store。BeginTx 是调用已有连接池事务 API 的薄入口，放在 order 包不会限制事务只能操作 orders；Service 负责开一次事务并将同一 tx 传给相关 SQL 方法。未来若在同一服务内拆多个 Store，可以集中一个事务入口，各 Store 参与同一事务，无须每个复制 BeginTx；本轮不引入新的公共事务组件。InsertOrder 的显式 tx 参数来自“订单与库存共同提交/回滚”的业务约定，调用实现必须使用 tx.ExecContext。它不代表 INSERT/UPDATE/DELETE 一律要手动开启事务；本项目 InnoDB 默认自动提交的单条写入可以独立执行，但独立提交的订单不能被下单事务的 Rollback 撤销。

框架中构造函数、类型、路由注册可以完整提供；业务方法返回 `ErrNotImplemented`，HTTP handler 返回 501。`main.go` 在学习者完成 HTTP 生命周期前仍保留现有“连接数据库后等待信号”的行为。不得把占位返回值作为成功结果。

**不创建仅有注释的 SQL 占位迁移**，避免迁移工具把空文件标记为已应用。学习者完成两个实际 SQL 文件并审查后，才能执行版本 2。

关键函数边界如下（代码框架中提供签名）：

- `Service.Create(ctx, input)`：整个下单流程；返回订单和是否重放。
- `Service.Get(ctx, id)`：按订单 ID 查单。
- `validateInput(input)`、`replay(existing, input)`：纯校验与重复参数比较。
- `validRequestID(id string) bool`：学习者新增的纯校验辅助函数，按字节检查 1–64 个 ASCII 字符及字母/数字/连字符/下划线白名单，由 validateInput 调用；不改写请求键。
- `Store.FindByRequestID`、`Store.FindByID`：通过连接池读取已提交订单。
- `Store.BeginTx`、`InsertOrder`、`ReadProduct`、`LockInventory`、`DeductInventory`：事务和事务内 SQL；SQL 方法必须显式接收 tx。
- `loadHTTPConfig() (HTTPConfig, error)`：读取 HTTP_ADDR 并提供默认地址和固定超时。
- `runHTTP(signalContext, db, cfg HTTPConfig)`：构造 store → service → handler → router，按传入配置运行 HTTP server。
- `runHTTPServer(signalContext, server)`：同文件内的启停函数，运行监听、接收监听结果、处理退出和请求取消；不拥有数据库连接池。

## 7. 配置、依赖与迁移

- 延用已有 MYSQL_* 环境变量、mysql driver 和连接池配置，不新增第三方 HTTP 框架。
- HTTP_ADDR 默认 `127.0.0.1:8080`；本地开发使用，需要变更时配置环境变量。
- 每个下单或查单操作使用从 `r.Context()` 派生的 5 秒超时，SQL 使用同一 context；这是处理预算，不是承诺任何数量的并发都能在 5 秒内完成。
- Server 设置 ReadHeaderTimeout 3 秒、ReadTimeout 10 秒、WriteTimeout 10 秒、IdleTimeout 60 秒。不能仅设置 WriteTimeout 就认为业务会停止。
- 信号退出：先 `Shutdown`，用独立 background context 给 5 秒；超时则 `Close` 强制结束连接，并取消服务级 context 以结束在途 SQL。HTTP 退出后才关闭数据库。不要直接把 signalContext 作为请求 BaseContext，否则信号一到就在优雅退出前取消所有请求。
- 必须接收和处理监听错误；`http.ErrServerClosed` 是预期结束，端口被占用要作为启动失败返回。后台启动 goroutine 的错误通过 channel 传回，不吞错误、不阻塞泄漏。
- 继续用迁移 CLI，Go 启动不自动迁移。

### 本轮 HTTP 配置补齐约定（2026-10-12）

学习者已新增 `HTTPConfig` 和 `loadHTTPConfig`，并明确授权导师补齐本轮配置，尤其修复时间单位。实现前先补充本约定；范围限于配置类型、加载、传递和 Server 字段，不推进 POST、数据库迁移或真实 MySQL 验收。

- 问题：整数直接转为 `time.Duration` 时按纳秒解释，例如 `time.Duration(10)` 为 10 纳秒，会造成几乎立即超时。四个超时字段直接使用 `time.Duration`，加载时使用 `time.Second` 表达单位，传给 Server 时不再转换。
- `cmd/order/http.go` 定义 `HTTPConfig`：`Addr string`，以及 `ReadHeaderTimeout`、`ReadTimeout`、`WriteTimeout`、`IdleTimeout` 四个 `time.Duration` 字段。配置对象只保存数据；`runHTTP` 使用它，不自行读取环境变量。
- `cmd/order/main.go` 实现 `loadHTTPConfig() (HTTPConfig, error)`，沿用学习者已有签名。本轮只有环境读取和默认值，返回配置及 nil；HTTP_ADDR 未设置或为空时使用 `127.0.0.1:8080`，非空则原样使用。四个固定超时依次为 3 秒、10 秒、10 秒、60 秒，不增加新的环境变量或依赖。
- `run` 先加载数据库配置及 HTTP 配置，再建立数据库连接池；分别使用 `dbCfg`、`httpCfg`，调用 `runHTTP(signalContext, db, httpCfg)`。连接池仍由 `run` 关闭，HTTP 退出管理沿用现有实现。
- 失败行为：数据库配置加载失败沿用现有启动失败语义；非法 HTTP 地址或端口占用由已有监听错误路径返回，随后关闭数据库连接池，不把监听失败当作正常启动。
- 实现顺序：先更新设计 → 修改字段类型并直接映射到 Server → 补齐配置加载和调用参数 → gofmt → 编译、静态检查和已有 HTTP 生命周期 race 测试 → 记录实际结果。
- 本轮验收：核对四个时长均明确带单位、默认地址分支和自定义地址透传；`go build ./...`、`go vet ./...`、`git diff --check`、`go test -race -count=1 ./...` 通过。已有生命周期测试只验证本地 HTTP 路径；默认/自定义地址的实际监听、GET 400/404/200、重启查单及真实 SIGTERM 与 MySQL 关闭顺序，仍留给下一轮运行验收。

### 本轮 HTTP 退出实现约定（2026-10-11）

学习者明确要求导师代写退出流程，再对照实际代码分轮学习 goroutine 和 channel。本轮只完成 HTTP 生命周期；地址环境变量和 Server 的读写超时继续保留为后续目标。

- `runHTTP` 保留已有组件组装，调用同文件的 `runHTTPServer`。`main.go` 已由学习者接入 `return runHTTP(signalContext, db)`，数据库仍由 `run` 的 defer 关闭。
- `runHTTPServer` 为请求创建独立的服务级 context，作为 `Server.BaseContext`；信号 context 只触发关闭，不立即取消在途请求。
- 使用一个 goroutine 调用 `ListenAndServe`；容量为 1 的 `chan error` 传递唯一一次监听结果，避免关闭过程等待在途请求时，发送方因尚无人接收而阻塞。用 `select` 等待监听结果或退出通知。
- 监听失败必须返回可分类的错误，并关闭连接、取消服务级 context。退出通知到达后，使用独立的 5 秒 context 调用 `Shutdown`；必须等 `Shutdown` 完成，不能仅因 `ListenAndServe` 返回 `ErrServerClosed` 就让主流程退出。
- `Shutdown` 失败时取消服务级 context 并调用 `Close`。超时等关闭错误仍向上返回；`Close` 错误及意外监听错误保留在返回错误中。正常的 `ErrServerClosed` 不作为进程失败。
- 不声称 `Close` 可以强制终止任意 handler；当前 SQL 链路必须响应请求 context，取消后由数据库层清理操作。没有处理 WebSocket 或自行忽略 context 的长期任务。
- 新增 `cmd/order/http_test.go`，使用测试专用 HTTP handler 和真实本地 TCP，不依赖 MySQL、不写业务数据。验证端口占用会返回错误、优雅退出期间不取消在途请求且等待其结束、5 秒超时后连接关闭且请求 context 取消，以及启动前收到退出通知也能结束。`go test -race ./...` 仅证明执行过的 Go 路径未报告竞争，数据库正确性和真实进程信号仍需另外验收。

版本 2 添加唯一索引 `uq_orders_request_id(request_id)`。执行前在目标开发库检查：

```sql
SELECT request_id, COUNT(*) AS n
FROM orders
GROUP BY request_id
HAVING COUNT(*) > 1;
```

有重复数据就停止，说明冲突并由学习者决定如何处理；不静默删单，也不能用 force 跳过失败。up 只增加该索引，down 只删除该索引。查验版本 2 且非 dirty，并用 SHOW INDEX 确认唯一性。version 1 已应用，不能直接修改它代替新迁移。

### 本轮 B1 SQL 代写约定（2026-10-12）

学习者明确要求导师完成两个版本 2 SQL 文件，随后自行阅读熟悉语法。本次授权只覆盖第 6 节约定的 up/down 文件，不扩展到 Go 业务实现或执行迁移。先补充本约定，再创建文件：up 使用 ALTER TABLE orders ADD UNIQUE INDEX uq_orders_request_id (request_id)，down 使用 ALTER TABLE orders DROP INDEX uq_orders_request_id，均以分号终止；版本 1 保留原样。静态审查两文件的表名、索引名、作用范围及 SQL 语法，并检查差异格式。重复数据预检查、实际迁移和回滚事务实验仍待学习者继续，不能把文件完成记为数据库约束已生效。

### 本轮 B1 迁移执行约定（2026-10-12）

学习者随后反馈数据库完全没有数据，并明确表示可以直接迁移，本次授权扩展到执行版本 2。应用启动仍不执行迁移，使用既有独立 CLI。先只读核对目标开发库、schema_migrations 的版本/dirty、orders 数量、重复数据及现有索引；若版本不是 1、处于 dirty 或发现重复，停止并检查实际状态。预检查通过后使用 /Users/royce/go/bin/migrate -path ./migrations -database <本机开发 URL> up 1，只执行下一个版本 2；从本机开发配置构造 URL，不打印密码或完整 URL，不在仓库保存临时执行脚本。执行失败保留实际错误与状态，不 force 或删除订单；成功后通过 CLI version 和数据库新查询核对 version=2、dirty=0、uq_orders_request_id 的列与唯一性，并确认订单数量未改变。此轮不回退版本 1，也不自动进入 B2；回滚事务中的重复插入实验仍按教学节奏先预测再执行，留待迁移后继续。

## 8. 开发步骤与学习节奏

整个阶段分多次完成，**不是一次 60–90 分钟内写完**。每次只选下面一个目标，完成并记录后再继续。

### 小步 A：认识框架，完成查单（建议第一场 60–90 分钟）

目标：理解一个请求怎样从 HTTP 走到 MySQL，不先处理整条事务。

1. 阅读 model.go 的 struct、字段标签和错误；认识 context、指针接收者、多返回值和 `if err != nil`。
2. 手写 `Store.FindByID`：参数化查询，Scan，`sql.ErrNoRows` 转换为订单不存在，其他错误保留分类。
3. 手写 `Service.Get` 和 GET handler：解析路径 ID、超时、JSON 响应和错误映射。
4. 手写 http.go 的服务启停，再把 main.go 的等待信号接入 `runHTTP`；POST 仍明确返回 501。
5. 对一个确认不存在的 ID 返回 404；非法 ID 返回 400。若要验证 200，先在可丢弃测试商品下插入明确标注的查单测试数据，记录 ID，并在本小步结束删除该测试订单，避免作为真实扣库存订单遗留。

辅助 agent 可拆成两个更短目标，但不能借机把 POST 全部写完。若时间不足，停在 FindByID 编译通过，保留 HTTP 未接入状态即可。

小步 A 的 HTTP 部分进一步拆分如下，每轮只推进一个目标：

- A-3a：只认识 `http.ResponseWriter`，理解响应头、状态码和正文的发送顺序；用最小响应实验验证默认 200 和显式状态码。
- A-3b：只认识 `*http.Request` 的路径变量读取和 `strconv.ParseInt`；练习把文本 ID 转为 `int64`，暂不接 service。
- A-3c：只认识 `r.Context()` 和 `context.WithTimeout`；理解请求取消、5 秒预算和 `defer cancel()`，暂不写 JSON。
- A-3d1：只认识 `encoding/json` 的 `Encoder`；练习将成功订单编码为 JSON 响应，暂不处理错误响应。
- A-3d2：只认识 `errors.Is` 和安全的错误响应结构；练习把 service 错误映射为 HTTP 状态码和错误 JSON。
- A-3e：整合前面已理解的部分，完成 `OrderHandler.Get`，再进行非法 ID 和不存在订单的 HTTP 验收。

每个子步骤开始前先确认前置概念；如果学习者不能解释或预测实验结果，就停留在当前子步骤，不继续引入新的标准库 API。

### 小步 B：唯一约束、校验与重复语义

1. 完成版本 2 两个 SQL，审查后迁移；实验两次相同 request_id 插入会发生什么（使用测试事务并回滚）。
2. 完成 validateInput、replay 和 FindByRequestID；练习 `errors.Is`、包装错误和 `errors.As` 识别 MySQL 错误。
3. 实现 InsertOrder 时，将确认属于 request_id 唯一键的 1062 转为内部 `ErrRequestIDTaken`；它不同于对客户端的 `ErrRequestConflict`。
4. 参数校验是应用检查；数据库唯一性是并发保障，不能只做前者或只做预查询。

本轮 B2 review 验证约定（2026-10-12）：学习者已修改查询、校验和重放，要求再次 review。先读取实际差异，不代改其 Go 文件。为验证纯函数业务结果，在 /private/tmp 建立临时目录，原样复制实际 service.go/store.go/model.go，用临时 Go 测试覆盖请求键长度/字符边界、商品 ID/数量正值、同参数重放及不同数量冲突；不访问数据库、不写订单、不用 mock 证明并发正确性。测试预期依据第 3/4 节业务约定，测试失败保留为实际结果并指导学习者修改，结束清理临时目录；仓库不新增测试文件或依赖。Create 未找到订单后的占位控制流按实际代码审查，事务和 POST 仍不在本轮实现范围。

### 小步 C：事务内 SQL 与下单编排

1. 手写 BeginTx、InsertOrder、ReadProduct、LockInventory、DeductInventory；确认 SQL 都用 tx。
2. 手写 Service.Create，严格按第 5 节顺序；先实现成功与回滚，再实现重复键后的回滚和重查。
3. `created_at` 在应用生成；取得 LastInsertId，错误要处理。所有 SQL 都用占位符，不能拼接客户端内容。
4. 区分查不到商品、没有库存行、库存不足、SQL 故障与提交结果不确定。影响行数不是 1 时回滚。

InsertOrder 的教学拆分（2026-10-12）：学习者完成 BeginTx 后进入插入方法，当前轮只认识 INSERT INTO orders (request_id, product_id, quantity, created_at) VALUES (?, ?, ?, ?) 及 tx.ExecContext 的调用/返回值；id 由自增生成，四个参数按列顺序来自 input 与传入的 createdAt。先由学习者在聊天中尝试调用，避免把未使用 result 或未实现返回结构的半成品记录为已完成。后续再讲 sql.Result.LastInsertId 与 Order 构建，然后用 errors.As 精确识别 request_id 唯一键的 MySQL 1062；当前插入步骤不提交事务或扩大到后续商品/库存操作。

正常路径补齐子步（2026-10-12）：学习者已把 SQL/调用写入实际方法，错误分类暂为注释。当前先让 Exec 错误分支明确返回 Order{} 和包装的 ErrStorageUnavailable，防止失败后使用可能不存在的 result；再调用 result.LastInsertId()，处理其 error，成功用生成的 ID、input 的三个字段和传入 createdAt 构造 Order 并返回 nil error。这个子步仅完成普通错误与成功路径，重复键此时仍暂按存储错误处理，须在下一轮替换为精确的 ErrRequestIDTaken 分支，不能提前做幂等验收或称 InsertOrder 全部完成。方法不 Commit/Rollback、不重新生成创建时间。

返回结构说明（2026-10-12）：InsertOrder 取得自增 ID 后，当前表的其他返回字段均已来自 input/createdAt，直接构造 Go 的 Order 值即可，不通过 FindByID 或连接池查回未提交行。构造返回结构不新增 SQL 写入，也不代表事务提交；调用者仍需按第 5 节继续检查商品/库存并决定 Commit/Rollback。

### DeductInventory 本轮实施约定（2026-10-13）

学习者明确授权导师完成 store.go 中的 DeductInventory，再详细讲解 SET 与更新执行 API。范围仅此方法及本文，不代改相邻 ReadProduct/LockInventory、Service 或迁移，不新增依赖。

- 前提：Service 已校验正数量，并在同一 tx 内按主键锁住库存行、判断 available >= quantity；本方法只执行扣减，不另加条件更新方案。
- SQL：UPDATE inventory SET available = available - ? WHERE product_id = ?，参数顺序为 quantity、productID，使用 tx.ExecContext 和调用方 context。
- 失败行为：SQL 执行失败或 RowsAffected 读取失败，返回包装的 ErrStorageUnavailable 并保留 cause；影响行数不是 1，返回 ErrInvariantViolation。UPDATE 未匹配行可能没有 SQL 错误，因此不能只检查 Exec 的 error。
- 成功行为：恰好修改一行返回 nil；不 Commit/Rollback，生命周期仍由 Service 管理。该方法单独不能保证库存非负，仍依赖上述锁定与检查前提。
- 实施顺序：先记录约定 → 只补方法 → gofmt/build/vet/差异检查 → 实际环境核验后运行局部事务实验 → 记录实现、证据与遗留。
- 临时验证设计：在 /private/tmp 建临时模块，复制实际 order 三文件、database/mysql.go 及现有 go.mod/go.sum；使用真实 MySQL 普通应用用户，仅在显式事务中插入唯一测试库存行 available=5，同一事务锁读、扣 2、读取 3，再 Rollback，用新查询确认测试行不存在。接着对已确认缺失的同一 ID 验证影响行数为 0 对应 ErrInvariantViolation，并验证已取消的执行 context 返回 ErrStorageUnavailable 且保留 context.Canceled。测试不提交、不写订单、不清理真实业务数据，结束移除临时文件；不是完整下单或并发验收。
- 学习预测：学习者预测事务内库存为 5、回滚后行不存在。导师已补充事务能看到自己的修改，扣后应为 3；回滚后不存在的预测正确。得到预测后才执行数据库实验。

### Create 事务边界教学拆分（2026-10-13）

学习者已手写新建分支，明确要求 review 并讲解何时提交/回滚；本轮导师只审查、给局部 API 提示及记录，不代改 service.go 或其他业务方法。当前一个小目标是 C2 的普通成功与回滚路径，不一次补齐 C3 并发重复分支或 POST。

- BeginTx 成功后立即 defer tx.Rollback()；defer 属于整个 Create 函数，嵌套 if 中的提前 return 也会触发。Go 函数 return 本身不结束数据库事务，context 取消后的自动回滚不能代替正常路径主动清理。
- 订单插入、商品检查、锁库存和扣减都使用同一个 tx，顺序沿用第 5 节。InsertOrder 返回未提交的新 Order，不复用预查询无记录时的零值。
- Commit 只放在订单插入及所有检查、扣减成功之后；Commit 返回 nil 才返回新建成功。Commit 错误包装 ErrCommitUnknown 与原始 cause，不用后续 Rollback 宣称提交未发生。
- 中途业务或 SQL 失败时返回原错误，由 deferred Rollback 兜底；成功提交后的 deferred Rollback 返回 sql.ErrTxDone，不撤销提交。不用 defer Commit，避免提前错误返回时仍提交。
- 需继续处理请求的重复键分支不只依赖 defer：必须先显式 Rollback，检查回滚错误，再从连接池重查；本轮记录但留待 C3 实施。
- 当前 ReadProduct 查询三列但 Scan 两列会阻断正常路径，需由学习者先修正；LockInventory 缺行分类和 InsertOrder 重复键识别继续保留为未完成。
- 验证安排：本轮先 build/vet/差异检查及静态控制流审查。学习者修改后再逐条检查提前返回与提交位置；具备完整路径后，按第 9 节逐项先预测再执行正常下单与失败回滚实验。当前不运行尚不完整的 Create，也不把已通过的局部库存测试当成 Create 验收。

### Create/Store 本轮授权修正约定（2026-10-13）

学习者在第三次 review 中明确授权导师直接修正剩余问题，并修改 if 嵌套；此次授权覆盖当前 Service.Create 及其依赖的 Store 错误分类，可完成此前留待 C3 的分支。本轮先补充本约定，再修改实现；不自动完成 POST、迁移、Redis/gRPC 或暂缓 GET 项，不提交/暂存，不改变学习者已有 Git index。

- 核对当前事实：ReadProduct 已由学习者修正为 SELECT id/is_active，不再代改；LockInventory 当前把所有 Scan 错误归为 ErrInventoryMissing，需要恢复无行/依赖故障的区分。
- Create 通过提前返回展开主流程：校验失败返回；预查询找到即 replay；非 ErrOrderNotFound 的查询错误直接返回；未找到才 BeginTx。成功开启后 defer Rollback，并在商品/库存检查之前 InsertOrder。createdAt 取一次 UTC 时间并截断至微秒；Commit 确认后才返回新 Order，提交错误保留 ErrCommitUnknown/cause。
- InsertOrder 用 errors.As 提取已有 mysql 驱动的 *mysql.MySQLError，同时检查 Number=1062 和错误消息末尾的完整唯一键名称。只接受当前 MySQL 格式的 for key 'orders.uq_orders_request_id' 或 for key 'uq_orders_request_id'，不因重复值中出现索引名而误判其他约束；其他错误归为 ErrStorageUnavailable，均保留 cause。驱动没有独立索引名称字段，此处限定当前已核对的 MySQL 8.4 格式，未知格式保守按依赖错误处理，不新增依赖。
- Service 遇 ErrRequestIDTaken 先显式 Rollback；回滚错误用标准库 slog.ErrorContext 仅记录 request_id、阶段与安全错误类别，再包装 ErrStorageUnavailable 并立即返回，不打印原始 cause/SQL/连接配置、不通过连接池重查。回滚成功后 FindByRequestID，再 replay；若意外查无记录，本项目无删单业务，按 ErrInvariantViolation 保留查无记录 cause，避免伪装成正常订单不存在；其他查询错误原样向上传递。
- LockInventory 仅 sql.ErrNoRows 归为 ErrInventoryMissing；其他 Scan/连接/context 错误包装 ErrStorageUnavailable/cause。其他 SQL 方法保留现有职责。
- 先执行 gofmt、build、vet 和 git diff --check。为覆盖难以真实注入的提交/回滚及查询错误，可在 /private/tmp 复制实际 order 源码和已有模块文件，使用临时 database/sql driver 测试重复键精确分类、Store 缺行与 context 错误分类、Service 提交失败/重复分支回滚失败及清理顺序；不新增仓库依赖、接口或测试文件，不用这些模拟结果证明数据库并发正确性。
- 真实库实验原计划使用独立测试商品/request_id，并限定清理本轮记录；学习者随后明确表示本轮无需使用真实开发库验证，因此取消本轮真实库实验，不查询业务库、不写入测试数据。后续实际 MySQL 创建/回滚/并发验收继续留待独立实验轮次，本轮只使用上述临时 driver 的无数据库测试。
- 学习者预测两种重复请求路径：首笔已完成时由预查询找到；请求重叠时后者插入触发唯一键错误。导师补充后者不是最终业务失败，Service 需先回滚再查原订单并返回 Replayed=true，最终一笔订单/库存 0；尚不宣称学习者已反馈掌握该补充。未执行/未通过的验证如实记录，不把模拟结果等同于真实 MySQL、POST HTTP、1000 请求并发或真实 Commit 确认丢失验收。

### 小步 D：POST HTTP 与失败实验

1. 手写请求体限制、JSON 解析、服务调用、201/200 与错误映射。
2. 先运行单请求、重放、参数冲突、库存不足实验。
3. 运行回滚和商品下架时序实验；按实际结果检查事务边界。

### 小步 E：并发验收与记录

增加真实 MySQL 集成实验或小型请求脚本，不用 mock 数据库证明锁与唯一约束。测试文件/脚本如需新增，先在本文补充文件和职责，再实现。
区分 Go 的 race 检查与数据库并发正确性：`-race` 发现被执行路径中的 Go 内存竞争，不会替你证明没有超卖。

## 9. 验收标准与实验

所有库存和订单核对都在请求结束后使用新查询完成。只操作明确标识的测试商品与请求键，不清空整个开发库；记录测试数据的创建与清理。每项先预测，再执行，记录请求数量、HTTP 分布、订单数、最终库存和异常。

| 验收项 | 操作 | 合格标准 | 实际结果 |
| --- | --- | --- | --- |
| 迁移 | 检查版本与唯一索引 | 版本 2 非 dirty；唯一键存在 | 2026-10-12 通过：预检查 orders=0、无重复，迁移前 version=1/dirty=0；migrate up 1 输出 2/u add_request_id_unique（8.5255ms），退出码 0；CLI version=2，数据库新查询 version=2/dirty=0，SHOW INDEX 确认 uq_orders_request_id(request_id)、Non_unique=0；orders 仍为 0。重复插入回滚实验尚未执行 |
| 普通下单与查单 | 库存 3，购买 2；GET 订单 | POST 201、GET 200；订单数量 2，库存 1 | 未执行 |
| 重启持久化 | 重启服务后 GET 原订单 | 同一个订单、相同参数与创建时间 | 未执行 |
| 重复成功 | 同键同参数再次 POST | 200、replayed=true、相同订单 ID；库存不再减少 | 未执行 |
| 重复参数冲突 | 同键改变商品或数量 | 409 request_conflict；原订单和库存不变 | 未执行 |
| 已下架商品重放 | 先成功，再下架，重放原请求 | 返回原订单；新键购买返回 product_inactive | 未执行 |
| 输入失败 | 非法数量、ID、request_id、JSON、超大请求体 | 按第 4 节返回 400/413/415；无业务写入 | 未执行 |
| 业务失败 | 商品缺失、不可售、库存不足 | 404/409；无该键订单，库存不变 | 未执行 |
| 数据不完整 | 测试商品没有库存行 | 500；无该键订单 | 未执行 |
| 事务回滚 | 调试器停在扣减后、Commit 前，终止本测试服务，再重启 | 用新连接确认库存未变、无该键订单；同键可重试。若无法调试，先补充集成测试的故障注入设计 | 未执行 |
| 可售规则 | 调试暂停在可售检查后；另一会话下架商品，继续原请求 | 已通过检查的请求可成功；后来的新请求被拒绝 | 未执行 |
| 两个请求抢最后一件 | 库存 1，不同键同时购买 1 | 一个 201，一个 409 insufficient_stock；一笔订单、库存 0 | 未执行 |
| 同键抢最后一件 | 库存 1，同键同参数 20 次并发 | 一次 201，其余 200；一个订单 ID、库存 0；不能返回库存不足 | 未执行 |
| 同键不同参数并发 | 库存足够，同键、不同 quantity | 一个成功订单；另一参数得到 409；只按赢家数量扣一次 | 未执行 |
| 1000 个不同键 | 库存 100，每次购买 1；先用 20 个并发客户端持续发出 1000 次请求 | 恰好 100 个 201、900 个库存不足；100 笔订单、库存 0、无负库存；无意外 5xx | 未执行 |
| 编译、静态检查、race | go build ./...；go vet ./...；实现集成实验后 go test -race ./... | 通过，注明执行过哪些测试；没有测试时不能称并发验收通过 | 2026-10-12 配置补齐后 build、vet 和 go test -race -count=1 ./... 通过；执行了四项 HTTP 生命周期测试，尚无数据库业务测试 |
| HTTP 配置 | 核对加载、时长单位和 Server 映射；运行时分别使用默认及自定义地址 | 地址按配置监听；四个超时为 3s/10s/10s/60s | 2026-10-12 代码核对与编译通过：默认地址、非空地址透传、time.Duration 字段和 time.Second 赋值已实现；学习者按指导显式设置 127.0.0.1:8080，反馈服务启动和 GET 非法 ID 响应符合预期；未单独验证未设置地址的默认分支、其他地址和慢连接超时 |
| 查单运行初验 | 使用应用用户配置启动服务，GET /orders/abc | database_connected；HTTP 400 和 invalid_input JSON | 2026-10-12 学习者反馈全部符合预期；导师未独立复测。该请求不执行订单 SQL，GET 404/200、重启持久化和真实 SIGTERM 由学习者选择暂缓 |
| HTTP 生命周期 | 端口冲突启动；正常请求时 SIGTERM；必要时关闭超时 | 启动错误可见；按预算退出；先停止 HTTP 再关闭池 | 2026-10-11 本地真实 HTTP 测试通过：端口冲突、context 触发优雅退出、5 秒超时强制关闭、启动前取消；race 无报告。真实进程 SIGTERM 和 MySQL 关闭顺序尚未运行验收 |

1000 次实验首先测试总量，用有界并发排除排队超时的干扰；再逐渐提高并发，记录延迟和 503。出现超时要分析连接池/锁等待，不能把 503 算作库存不足，也不能为得到预期数字忽略异常。

提交响应丢失可另用客户端断开推演：服务可能提交成功，客户端不确定；原键重试必须收敛到同一订单。真实 Commit 确认丢失实验需要额外网络故障控制，本阶段可记录推演，不能声称已经复现。

## 10. 本轮交付与完成记录

- [x] 本设计文档：业务顺序、文件边界、实施步骤、失败行为和实验约定。
- [x] Go 框架落盘、格式化、编译及静态检查。
- [x] 小步 A 子步骤：`Store.FindByID` 已实现参数化查询、`Scan` 映射、查无记录和存储错误分类。
- [x] 小步 A 子步骤：`Service.Get` 已实现正 ID 校验，并向上透传查单结果和存储错误。
- [x] 小步 A 子步骤：`OrderHandler.Get` 已实现路径 ID 解析、5 秒请求超时、成功 JSON 和安全错误映射。
- [x] 小步 A 子步骤：`runHTTP` 已组装并由 `main.go` 接入；`runHTTPServer` 实现监听结果传递、退出通知、优雅关闭和超时强制关闭，并通过本地 HTTP 生命周期测试。
- [x] `HTTP_ADDR` 环境变量、Server 的读写和空闲超时配置；学习者反馈显式配置地址的启动及 400 响应符合预期，其他地址与慢连接超时尚未运行验证。
- [x] 版本 2 up/down SQL 编写与静态审查；按学习者明确授权由导师完成。
- [x] 版本 2 重复数据预检查、实际迁移及版本/唯一索引核验。
- [ ] 唯一键重复插入与回滚清理实验；B1 尚余此项。
- [x] B2：FindByRequestID 已由学习者实现并修正列名，参数化查询/Scan/错误分类静态审查及编译通过；真实查询仍待验证。
- [x] B2：validateInput 和 validRequestID 已由学习者实现，12 个临时纯函数测试用例通过。
- [x] B2：replay 数量比较已修正，3 个临时纯函数重放用例全部通过。
- [x] C1：Store.BeginTx 已由学习者实现，静态审查与编译通过；2026-10-13 局部库存测试已执行真实库开事务及显式回滚，尚无提交故障/完整下单验收。
- [x] C1：DeductInventory 按学习者明确授权由导师完成，真实 MySQL 扣减/回滚、缺失行和取消原因的三个局部测试通过，race 无报告；不代表完整业务并发验收。
- [x] C1：ReadProduct 两列映射已由学习者修正；LockInventory 无行/其他错误分类按授权修正，4 个临时 driver 分类用例通过；真实查询验收仍待完成。
- [x] B3：InsertOrder request_id 唯一键分类按授权实现，9 个临时 driver 错误分类用例通过；实际 MySQL 消息格式和重复插入回滚实验仍待验证。
- [x] C2/C3：Create 按授权修正主流程顺序、UTC/微秒时间、重复键回滚后重查/replay 和 if 嵌套；22 个临时 driver 流程用例通过，完整真实库/HTTP/并发验收仍待完成。
- [ ] GET 真实 MySQL 完整运行验收；应用连接启动和非法 ID 的 400 已由学习者反馈通过，404/200、重启持久化和真实 SIGTERM 暂缓。
- [x] POST HTTP handler 实现；请求解析、JSON 响应和 service 错误映射已按设计完成；已实现的 Service/Store 事务、行锁与重复语义仍需真实库及 HTTP 验收。
- [ ] 数据库与 HTTP 实验执行，填写第 9 节实际结果。

目前查单代码已实现，但真实 MySQL 运行验收尚未完成；成功下单、幂等和并发正确性仍为设计约定，不是已实现事实。最终完成时逐项更新，保留尚未通过的实验和原因。

2026-10-09 框架交付记录：新增 http.go、order 包三个文件、httpapi 包两个文件；main.go 只补接入 TODO，保留已验收的数据库启动行为。gofmt、go build ./...、go vet ./...、go test ./... 通过；当前无测试文件，go test 只说明包能编译，不代表功能、事务或并发验收通过。沙箱默认 Go 缓存目录不可写，使用 `GOCACHE=/private/tmp/flash-order-review-go-cache GOFLAGS=-buildvcs=false` 完成检查。未执行迁移、未修改数据库内容；HTTP 尚未接入启动流程，两个 handler 仍为 501 占位。

2026-10-09 小步 A-1 进度：学习者完成 `internal/order/store.go` 中的 `Store.FindByID`。实现使用显式字段和 `?` 参数占位符，通过 `QueryRowContext` 传递请求 context，并用 `Scan` 写入 `Order`；`sql.ErrNoRows` 转为 `ErrOrderNotFound`，其他错误归类为 `ErrStorageUnavailable`，成功返回订单。已运行 gofmt 和 `GOCACHE=/private/tmp/flash-order-review-go-cache GOFLAGS=-buildvcs=false go test ./...`，检查通过；当前无测试文件，尚未执行真实 MySQL 查询，因此查单运行时验收仍未完成。下一步为小步 A-2：实现 `Service.Get` 的正 ID 校验和错误透传。

2026-10-09 小步 A-2 进度：学习者完成 `internal/order/service.go` 中的 `Service.Get`。实现对 `id <= 0` 返回 `ErrInvalidInput`，合法 ID 透传同一个 context 调用 `Store.FindByID`，并将订单或错误返回给上层；未引入 HTTP 类型或数据库职责。已运行 gofmt 和 `GOCACHE=/private/tmp/flash-order-review-go-cache GOFLAGS=-buildvcs=false go test ./...`，检查通过；当前仍无运行时数据库测试，GET HTTP handler、服务启动接入和 400/404 验收尚未完成。

2026-10-11 小步 A-3 进度：完成 `internal/httpapi/order.go` 的 `OrderHandler.Get`。实现路径 ID 解析和正数校验、从请求 context 派生 5 秒超时、成功订单 JSON，以及通过 `errors.Is` 将领域错误映射为安全的 HTTP JSON 错误；非法 ID 写响应后立即返回，未向客户端暴露原始数据库错误。已运行 gofmt 和 `GOCACHE=/private/tmp/flash-order-review-go-cache GOFLAGS=-buildvcs=false go test ./...`，检查通过；尚未接入 `runHTTP`，因此没有执行真实 HTTP 或 MySQL 运行时验收。

2026-10-11 小步 A-4 退出进度：学习者已完成 `runHTTP` 的对象组装、监听调用及 `main.go` 接入；本次明确授权导师完成退出流程。新增同文件 `runHTTPServer`，使用独立服务级 context、容量为 1 的监听结果通道、监听 goroutine 和 select；退出时等待独立 5 秒预算的 Shutdown，失败则取消在途请求并 Close，收取监听结果后返回。新增 `cmd/order/http_test.go`，四项真实本地 HTTP 测试覆盖监听失败错误保留、等待在途请求正常完成、5 秒超时后请求取消和连接关闭、启动前取消。

本轮验证：gofmt、`go build ./...`、`go vet ./...`、`git diff --check` 和 `go test -race ./...` 通过，Go 命令使用 `GOCACHE=/private/tmp/flash-order-review-go-cache GOFLAGS=-buildvcs=false`。首次 race 测试因沙箱禁止绑定本地端口失败；经执行授权后重跑通过（cmd/order 用时 6.200 秒），并非代码测试失败被忽略。没有访问 MySQL、执行迁移或写入测试订单；未验证真实 SQL 取消或操作系统 SIGTERM。HTTP_ADDR 和 Server 读写超时仍留有 TODO，POST 保持 501。

学习停点：学习者理解了匿名函数和 goroutine 使监听与退出控制并发推进，正在学习通道发送/接收；有缓冲通道和 select 仍需对照实际代码逐轮讲解。下一轮先看 `listenErr := make(chan error, 1)` 和监听 goroutine，再看 select，最后解释 Shutdown 与服务级 context；不一次讲完整个函数。

2026-10-12 提交前进度：学习者确认已经理解目前所有代码，包括 HTTP 生命周期中的 goroutine、通道、select、Shutdown 和服务级 context；上一条记录中的语法学习停点已完成。提交前重新运行 gofmt 格式检查、go build ./...、go vet ./...、git diff --check 和 go test -race -count=1 ./...，全部通过；四项 HTTP 生命周期测试实际执行，cmd/order 用时 6.668 秒，其他包尚无测试文件。未访问 MySQL、执行迁移或写入业务数据，真实查单与进程信号验收仍未完成。

下一小步：完成查单阶段的运行验收。先按第 7 节手写 HTTP_ADDR 和 Server 超时配置，再检查实际 MySQL 环境，验证 GET 的 400/404/200、服务重启后的查单，以及真实 SIGTERM 下 HTTP 与数据库的关闭顺序。完成后再进入版本 2 唯一约束，不直接开始整条 POST 实现。

2026-10-12 HTTP 配置补齐进度：学习者提出用 `HTTPConfig` 集中配置、由 main 加载并传给 runHTTP，随后明确授权导师补齐剩余实现和修复时间单位。先补充第 6/7 节约定，再修改 `cmd/order/http.go`：四个超时字段改为 `time.Duration`，Server 直接使用字段；修改 `cmd/order/main.go`：补齐 HTTP_ADDR 默认值和 3s/10s/10s/60s 固定超时，在 run 中加载并传递 httpCfg，数据库配置使用 dbCfg 区分。保留学习者已有 `loadHTTPConfig() (HTTPConfig, error)` 签名，本轮返回 nil 错误。

验证结果：gofmt、go build ./...、go vet ./...、git diff --check 通过。`GOCACHE=/private/tmp/flash-order-review-go-cache GOFLAGS=-buildvcs=false go test -race -count=1 ./...` 首次因沙箱禁止绑定本地端口而失败；放宽执行限制后重跑通过，cmd/order 用时 6.203 秒，四项已有 HTTP 生命周期测试执行成功且 race 无报告，其他包尚无测试文件。本轮没有新增测试，默认/自定义地址和时长的结论来自代码核对，不能代替实际监听验证。未访问 MySQL、写入测试订单或执行版本 2 迁移；真实 GET、重启查询和进程 SIGTERM 的验收继续保留为待完成。

学习交接：已解释整数转为 time.Duration 按纳秒计数，以及带单位的时长在配置中直接传递的原因；学习者尚未反馈对修复代码的理解。下一次从检查 Docker/MySQL 实际运行环境开始，按小步 A 完成查单运行验收，通过后记录结果并清理临时订单。

2026-10-12 运行验收环境进度：学习者亲手执行 Compose up 和 ps，反馈 `flash-order-dev-mysql-1` 使用 mysql:8.4，状态为 healthy，端口为 `127.0.0.1:3306 -> 3306/tcp`。这确认了容器健康状态与端口映射，尚未证明 Go 应用的连接配置、orders 表或查单结果。导师只核对本机配置文件中的变量名称：MYSQL_PORT、MYSQL_ROOT_PASSWORD、MYSQL_DATABASE、MYSQL_USER、MYSQL_PASSWORD；没有输出配置值。下一操作由学习者加载应用环境、编译到临时目录并直接运行程序，观察 database_connected，再从另一终端验证非法订单 ID 的 HTTP 400；该请求不会查询 orders 表，404/200 与真实 SIGTERM 验收仍待执行。

2026-10-12 学习节奏调整：学习者反馈应用启动日志及 GET /orders/abc 的响应全部符合预期，并明确选择本轮先测试到这里，继续实现下单接口。按学习者最新要求，剩余查单验收暂缓，不能记录为通过；应用连接检查和 400 路径也不能证明 orders 查询、持久化或真实进程退出已经验收。本轮没有插入查单测试订单。后续按既定小步 B 开始：先检查目标库现有 request_id 重复数据，再由学习者手写版本 2 up/down 唯一索引 SQL，导师审查后执行迁移，用回滚测试事务观察重复键。该学习节奏调整不取消最终验收项目，也未授权导师代写后续业务 TODO；下一轮先讨论唯一约束的作用边界，指导重复数据预检查。

2026-10-12 会话职责调整：学习者明确要求本会话只串联管理主线和做代码 review，具体开发及教学转移到另开的专项会话。为支持直接用本文交接，新增第 1 节会话分工和当前接手摘要，保留第 12 节教学指引；新专项会话从 B1 开始，并在结束时更新本文，供主线 review 核对。本轮仅整理交接文档，没有继续实现业务或执行迁移。

2026-10-12 B1 专项教学接手（迁移尚未实施）：已阅读根目录 AGENTS.md、本文及 001 完成记录，用 git status、git diff 和实际 Go/SQL 文件核对。接手时已有未提交修改为本文、cmd/order/http.go 和 cmd/order/main.go，内容对应此前 HTTP 配置及交接记录；无暂存改动。GET 链路、HTTPConfig 和配置传递已存在，POST、Service.Create、FindByRequestID、校验/重放和事务 SQL 仍为占位，两个版本 2 SQL 文件尚不存在。导师本次只更新本文接手摘要与完成记录，未修改 Go 或版本 1 SQL，未代写版本 2。

- 环境核验：go version 为 go1.27.1 darwin/arm64；Docker Compose ps 确认 flash-order-dev-mysql-1 healthy，映射 127.0.0.1:3306。默认沙箱访问 Docker socket 被拒绝后，经执行授权完成只读核验。宿主机没有可用的 mysql 命令，容器内客户端可用。/Users/royce/go/bin/migrate 的 -version 输出 dev；go version -m 确认其模块版本为 v4.20.1、构建带 mysql 标签，未安装或升级依赖。
- 数据库核验：用容器内普通应用用户执行 SELECT VERSION()/DATABASE()、查询 schema_migrations 和 SHOW INDEX FROM orders。实际为 MySQL 8.4.11、目标库 flash_order、version=1、dirty=0，orders 只有 PRIMARY(id)。本次均为只读操作，未输出密码或连接 URL，未执行重复数据查询、迁移或写入测试订单。
- 验证范围：以上环境与数据库结果由导师实际核验；git diff --check 通过。未重新运行 Go build/vet/race，已有通过记录仍归属于此前 HTTP 配置轮次。B1 唯一索引存在性、重复键失败和测试数据回滚清理均未验收，第 9 节迁移项仍为未执行。收尾再次核对时 HEAD 为 cf678ef（feat: add http config load），两个 HTTP 文件的实现仍存在且已无工作区差异，仅本文保留本轮未提交修改；导师本轮未执行 Git 提交。
- 学习停点（接手时）：本轮目标只到 B1。先让学习者预测“已有两行相同 request_id 时添加唯一索引”的结果，再亲手执行第 7 节重复数据预检查；接手时尚无学习者回答或执行结果。后续回答与语法辅导见下方补充记录。若预检查发现重复，停止迁移并由学习者决定处理方式，不删单、不 force。
- 下一停点：完成预检查后，由学习者手写 000002_add_request_id_unique.up.sql/down.sql，导师审查再迁移，确认 version=2、非 dirty 和唯一索引，并在显式回滚的测试事务中观察重复插入错误与清理结果。B1 完成后记录并停下，B2 校验/重放/按请求 ID 查询留待后续；GET 404/200、重启查单与真实 SIGTERM 验收继续按学习者要求暂缓。

2026-10-12 B1 索引语法辅导：学习者预测已有重复 request_id 时添加唯一索引会报错，判断正确；同时询问版本 1 是否完全没有索引及如何添加索引。导师澄清 PRIMARY KEY(id) 已建立主键索引，CHECK(quantity > 0) 不建立索引，并给普通索引与唯一索引的通用语法模板；由学习者在聊天中先尝试写出针对 orders/request_id 的添加语句，再审查。尚未收到学习者的 SQL 尝试，未创建迁移文件、执行预检查或修改数据库，不能记录 B1 通过或已掌握索引语法。本轮仅更新本文，git diff --check 通过；下一步仍须在执行版本 2 前完成第 7 节重复数据预检查。

2026-10-12 B1 up 语句审查：学习者在聊天中提交 ALTER TABLE orders ADD UNIQUE INDEX uq_orders_request_id (request_id)，表名、索引类型、索引名和列均符合设计；导师提示补终止分号并保存至 migrations/000002_add_request_id_unique.up.sql。实际核对迁移目录仍只有版本 1 两个文件，尚未保存版本 2，未执行 SQL；下一停点是学习者亲手创建 up 文件后审查，再指导 down。第 7 节重复数据预检查、迁移版本/索引验证和回滚实验均继续待完成。导师本次仅更新本文，git diff --check 通过。

2026-10-12 B1 两个 SQL 文件完成：学习者明确要求导师代写两个文件供其阅读学习。先在第 7 节记录本轮授权与范围，再创建 migrations/000002_add_request_id_unique.up.sql 和对应 down.sql。up 只对 orders 添加 uq_orders_request_id(request_id) 唯一索引；down 只删除该索引，两者均包含完整 ALTER TABLE 语句和终止分号，不改表数据或版本 1。已读取实际文件静态核对表名、索引名、列及作用范围，git diff --check 通过；对两个未跟踪 SQL 文件另运行 git diff --no-index --check /dev/null <文件>，均无空白错误诊断（退出码 1 表示新文件与 /dev/null 有差异）。Git 状态为本文修改及两个新增 SQL 文件，尚未暂存/提交。

验证与停点：本轮没有执行 SQL、预检查或迁移，没有写入测试订单，未运行 Go build/vet/race；静态审查不等于 MySQL 实际执行成功。两个文件已完成，数据库唯一约束仍未应用，B1 运行验收尚未通过。学习者已经能预测已有重复数据时会报错，并写出添加语句；DROP INDEX 和实际迁移流程仍待其阅读与实验。下一步由学习者熟悉两条 SQL，再在目标开发库执行第 7 节重复数据预检查；无重复后运行版本 2，确认 version=2、dirty=0 和 SHOW INDEX 的唯一性，并用回滚事务观察重复键且验证测试订单无遗留。B2 和暂缓的查单验收不在本轮范围。

2026-10-12 B1 版本 2 实际迁移：学习者询问是否在每次启动时执行全部 up，并反馈数据库为空、明确表示可以直接迁移。导师先核对 cmd/order 与 internal/database，确认应用没有调用迁移；结合既有 migrate CLI 的 -help 和 v4.20.1 实现说明：CLI 读取 schema_migrations 的当前版本和 dirty 状态，从该版本的后续脚本继续；up 1 表示执行后续一个版本。第 7 节先记录本轮执行授权与失败边界，再运行。

- 预检查实际结果：使用容器内普通应用用户只读查询，目标库 flash_order，version=1、dirty=0，orders 数量为 0；第 7 节重复 request_id 查询无结果，SHOW INDEX 只有 PRIMARY(id)。检查通过，没有删除数据或 force。
- 执行实际结果：从本机开发配置构造脱敏处理的迁移 URL（连接预算 3 秒），调用既有 /Users/royce/go/bin/migrate -path ./migrations -database <开发 URL> up 1，输出 2/u add_request_id_unique（8.5255ms），退出码 0；紧接 CLI version 输出 2，退出码 0。没有输出密码/完整 URL，没有保存临时脚本、修改 Go 或回退迁移。
- 验收实际结果：用数据库新查询确认目标库仍为 flash_order，version=2、dirty=0；SHOW INDEX 存在 uq_orders_request_id，列 request_id、Non_unique=0，原 PRIMARY(id) 也存在；订单数量仍为 0。第 9 节迁移项已更新为通过，唯一索引已生效。git diff --check 通过；未重跑 Go build/vet/race，也未运行重复插入事务实验或并发下单。
- 接手停点：学习者尚未反馈对迁移版本机制的理解；下一轮先预测同一测试事务中两次相同 request_id 插入的结果，再观察重复键错误，显式回滚并用新查询确认测试订单无遗留。B1 尚余该实验，不能以迁移通过代替幂等或并发验收。B2 和已暂缓的查单项目继续留待后续。

2026-10-12 下单流程讨论停点：学习者最新要求关注 POST，下单草案为预查 request_id → 开事务锁库存/扣减 → 插订单 → 结束事务。导师读取第 3/5 节与实际 Service.Create、Store 方法核对；业务方法仍是占位，本轮没有修改 Go 或执行数据库操作。审查重点是：已有订单先比较 product_id/quantity；当前方案应先在事务内插订单占唯一键，再检查商品可售、锁库存和扣减；成功以 Commit 为界，失败需回滚。先用库存 1、相同 request_id 的两次并发请求都预查未找到的时序，让学习者预测原草案第二个请求的响应，再解释顺序的理由；尚未收到预测，不能记录为理解已确认。B1 回滚实验保留为未执行，按学习者最新关注先讨论流程，不扩展到整条 POST 实现或代写 TODO。下一停点是完成该时序推演，再按 B2 一次选一个函数亲手实现。接手摘要和第 10 节已同步，git diff --check 通过。

2026-10-12 事务概念停点：学习者确认 B 应返回 A 的订单，并主动发现“提交”和“结束事务”的概念盲点。导师补充原草案的实际路径会返回库存不足，随后只讲一个概念：提交是结束事务的一种方式，回滚也是；事务内 SQL 执行成功仍属于未提交修改，Commit 成功才确认生效，Rollback 撤销该事务中的修改，两者完成后释放事务持有的锁。结合本项目说明提前插入订单后库存不足可以回滚，Go 函数 return 本身不是数据库提交/回滚指令；Commit 错误仍沿用结果未确认约定，不推断为已回滚。本轮只更新本文，未改业务代码或执行数据库实验，git diff --check 通过。学习者尚未反馈对该概念的理解；下一停点先确认这一点，再回到唯一键占用与同键并发顺序，之后按 B2 单函数实施。B1 重复插入回滚实验与暂缓的查单验收仍未执行。

2026-10-12 唯一键与幂等推理进度：学习者自行说明 request_id 的并发保障来自数据库唯一索引而非应用预查询，并提出事务内先插订单、库存不足回滚，表明已能应用回滚与占键顺序。导师确认该核心推理，补充唯一约束只保证同键最多一个已提交订单，完整业务幂等还需重复键后显式回滚、通过连接池重查、同参数返回原单/不同参数冲突；只有确认来自 request_id 唯一键的重复错误才进入重放分支。同时说明在本轮两个同键请求时序中，A 尚未结束事务时 B 插入可能等待，A 提交后得到重复键，A 回滚后 B 可继续插入。允许讨论省略预查询的可行性，但第 5 节当前设计仍保留预查询作为避免重复写入尝试的优化，未改实现方案。本轮只更新本文，git diff --check 通过，业务代码和数据库数据未修改。上述补充仍待学习者反馈；随后从 B2 一次一个函数落实，B1 回滚实验和查单遗留继续保留为未执行。

2026-10-12 预查询选择与 B2 入口：学习者用 A/B 交错时序指出两次预查询都可能为空，导师确认该场景不会省掉后续操作，但重复键后回滚/重查仍能返回赢家订单；随后学习者明确选择保留预查询。该选择沿用当前设计，没有更改代码流程；“大部分情况下节省性能”是学习者判断，尚无重复率或性能测量支持，记录为取舍而非实测结论。下一轮只指导 Store.FindByRequestID：读取并沿用已实现 FindByID 的显式字段、Scan 顺序与错误规则，WHERE 改为 request_id = ?，通过连接池传同一 context 和 requestID 参数；找到返回 Order，sql.ErrNoRows 转 ErrOrderNotFound，其他错误归为 ErrStorageUnavailable 并保留 cause。导师已核对实际函数仍为占位，学习者尚未提交实现；本轮仅更新本文，git diff --check 通过，没有执行数据库或 Go 测试。保存后先审查并编译，编译通过不等于真实数据库/幂等验收。B1 回滚实验与暂缓的 GET 验收继续保留为未执行，未扩展到 Service.Create 或 POST 全链路。

2026-10-12 B2 查询与 Create 片段审查：学习者亲手修改 internal/order/store.go 的 FindByRequestID，并在 internal/order/service.go 的 Create 中调用预查询，要求导师审查及说明 Store/事务的职责。实际代码与 Git diff 已核对，导师未代改 Go。正确部分为参数化 request_id 查询、同一 context、显式字段/Scan 结构及 Store 的无记录/存储错误分类；尚有以下正确性问题，未将函数记录为完成：

- store.go 第 56 行使用 create_at，实际表列为 created_at；执行任意预查询都会报未知列并归为 ErrStorageUnavailable。编译检查无法发现 SQL 字符串中的列名错误，先由学习者修正。
- service.go 第 39–40 行把所有错误直接返回，包含代表“可以进入创建路径”的 ErrOrderNotFound；应区分找到、未找到和其他存储错误。第 43 行比较 Order 零值不能作为记录存在性判断，正常找到的订单反而落入 ErrNotImplemented，未找到路径则已提前返回。找到分支还需按约定比较 product_id/quantity，不能直接将不同参数的请求返回成功。
- 学习者提出查重后再校验 request_id；第 4/5 节约定要求先 validateInput（含请求键、商品 ID 与数量）再预查询。validateInput 和 replay 仍是占位，实现与接入继续按单函数节奏完成，不推进完整事务或 POST。

设计解释：Store 不必按表一一拆分；BeginTx 当前只是从连接池开启一个事务的薄入口，Service 管理事务范围，跨多个本服务 Store 的组合只需同一个事务入口和同一 tx。InsertOrder 接收 tx 是为了保证它与扣库存共同回滚，不是 SQL 写操作一律必须手动开启事务；若订单通过连接池自动提交，之后库存检查失败时回滚下单事务，订单仍会留下。第 6 节补充该职责说明，未重构接口或新增组件。

实际验证：GOCACHE=/private/tmp/flash-order-review-go-cache GOFLAGS=-buildvcs=false go build ./... 退出码 0；git diff --check 通过。未运行真实 SQL、测试订单或 Go 测试，不把编译通过记为查询/重放验收通过。学习者对新增架构解释仍待反馈；下一停点先亲手修正查询列名和 Create 预查询三类结果，再逐个完成 validateInput/replay。BeginTx、InsertOrder、事务编排与 POST 仍为占位，B1 回滚实验和暂缓 GET 验收仍未执行。

2026-10-12 B2 再次 review：学习者反馈已理解 Store/事务设计，并亲手修改查询列名、Create 校验/查重分支，新增 validRequestID 辅助函数，实现 validateInput 与 replay。导师读取实际代码和差异，未代改 Go。已修复事实：FindByRequestID 使用 created_at；校验位于预查询之前；请求键按 1–64 字节 ASCII 白名单检查，商品 ID 和数量要求正值，符合当前输入类型边界。

仍待修复的正确性问题：service.go 第 90 行将订单数量与 int32(input.ProductID) 比较，应比较 input.Quantity（双方已是 int32）。实际旧单商品 1/数量 2、同参数重试会误报 ErrRequestConflict；旧单商品 1/数量 1、请求数量改为 2 反而得到 Replayed=true。第 49–56 行的 ErrOrderNotFound 分支只有 TODO 注释，仍会继续执行 replay(Order{}, input)，对合法新请求返回 ErrRequestConflict；本小步创建路径未实现时，应明确返回 ErrNotImplemented，找到订单才进入 replay，其他存储错误继续透传。

实际验证：先在第 8 节补充临时验证约定，再把三个实际 Go 文件原样复制到 /private/tmp 临时目录，以 go test -count=1 -v service.go store.go model.go b2_review_test.go 验证纯函数，不访问数据库。12 个输入校验用例全部通过，覆盖合法字符、64/65 字节、空键、非 ASCII、空格/标点、商品 ID/数量的零值负值及最大正值。3 个重放用例中 changed_product 通过，same_parameters 与 changed_quantity 失败，测试退出码 1，未忽略或记为通过；临时目录及测试文件已清理。go build ./... 退出码 0，gofmt -l 两个变更 Go 文件无输出，git diff --check 通过。检查使用已有临时 GOCACHE 和 GOFLAGS=-buildvcs=false，未新增仓库测试文件或依赖。

当前完成边界：Store 查询代码静态审查和输入校验通过；replay、Create 未找到分支尚未通过。下一停点只修正上述两处，再复查相关纯函数结果，不推进事务或完整 POST。真实 SQL、B1 重复插入回滚实验、GET 遗留及数据库幂等/并发验收均未执行；代码与文档待学习者纳入 Git 提交。

2026-10-12 重放修复与 Store 创建路线：学习者修复 replay，并说明 Create 新建分支尚留 TODO 是因为不会实现 Store 的事务/插入等方法，要求列出完成创建流程所需能力。导师核对实际代码：replay 已比较 existing.Quantity/input.Quantity 与 ProductID；其余五个 Store 方法均为 ErrNotImplemented 占位。按第 5/6 节说明 BeginTx → InsertOrder → ReadProduct → LockInventory → DeductInventory 的职责，商品可售/库存足够等业务判断及统一 Commit/Rollback 由 Service 负责。request_id 重复键仍需先回滚，再通过连接池重新查询并重放；不让各 Store 方法自行提交。

复验实际结果：沿用第 8 节临时验证方式，原样复制当前三个 Go 文件到 /private/tmp，以 go test -count=1 -v service.go store.go model.go b2_review_test.go 重跑 same_parameters、changed_quantity、changed_product，3 项全部通过，退出码 0，临时目录已清理。仅复验受本次修复影响的重放逻辑，未重跑输入校验/全项目测试或执行数据库操作；此前失败记录保留为历史，最新重放状态改为通过。导师本轮只更新本文，没有修改 Go 或补齐完整流程，git diff --check 通过。

下一停点：先由学习者只实现 Store.BeginTx(ctx)，调用连接池的 BeginTx(ctx, nil)，本轮不定制事务选项；成功返回 tx，失败归为 ErrStorageUnavailable 并保留 cause。保存后审查与编译，再进入 InsertOrder 的 SQL/返回 ID/重复键识别，逐轮推进。Create 未找到分支目前仍穿过 TODO 调用 replay，未实现事实不变；本轮路线说明不代表该路径已可用。B1 回滚实验、真实查询、查单遗留和最终事务/幂等/并发验收继续待执行。

2026-10-12 BeginTx review 与 InsertOrder 入口：学习者亲手完成 Store.BeginTx，导师核对实际 store.go 第 83–91 行及 Git diff。实现透传 ctx，以 nil 选项调用连接池 BeginTx，错误返回 nil 事务并包装 ErrStorageUnavailable/cause，成功返回 tx/nil；未在方法内提交或回滚，符合 Service 拥有事务生命周期的约定。未发现该实现的正确性问题，导师未代改 Go。

验证实际结果：GOCACHE=/private/tmp/flash-order-review-go-cache GOFLAGS=-buildvcs=false go build ./... 退出码 0；gofmt -l internal/order/store.go 无输出，git diff --check 通过。没有给薄包装方法编写镜像测试，也未访问 MySQL 或验证开启/取消/回滚的实际生命周期；不把编译通过记为数据库业务验收。其余 Store 事务方法仍为 ErrNotImplemented，InsertOrder 仅删除了 TODO 标记，业务实现尚未增加；Create 未找到分支遗留不变。

下一停点：进入 InsertOrder，但一轮只讲紧密相关的 INSERT/ExecContext。给出四列参数化 SQL，说明自增 id 不需由调用方提供、字段与参数按位置对应、使用传入 tx 执行而非连接池；ExecContext 返回执行结果与 error，成功仍是未提交插入。等待学习者在聊天中尝试调用后，再推进 LastInsertId/返回 Order 和重复键分类，暂不生成完整函数或文件。接手摘要与教学拆分已更新；B1 回滚实验、真实查询和暂缓 GET 验收继续待执行。

2026-10-12 InsertOrder 部分实现 review：学习者写入 INSERT SQL、传入 tx 执行及四个参数，并用注释表示重复请求键分类/其他错误返回的思路。导师读取实际 store.go 第 93–115 行及差异，确认字段、占位符、参数顺序和 tx 使用均符合设计。注释表示方向正确，但其他 SQL 错误仍应包装 ErrStorageUnavailable 并保留 cause，而非仅返回无法分类的原始错误；错误分支需要 return，不能落入正常路径读取 result。当前尚无成功返回及重复键分类，导师没有代改 Go。

实际验证：GOCACHE=/private/tmp/flash-order-review-go-cache GOFLAGS=-buildvcs=false go build ./... 退出码 1，明确报告 store.go:102 result 未使用、store.go:115 missing return，属于方法尚未完成，未忽略或记录为通过。git diff --check 通过；没有执行真实 INSERT、写测试订单或运行 Go 测试。

下一停点：先只补 Exec 的普通失败返回及 LastInsertId/返回 Order，通过编译后再学习 errors.As 和 request_id 唯一键的精确分类。返回订单仍表示未提交数据，Service 管理最终提交/回滚。正常路径通过不等于重复键或真实数据库验收通过。B1 回滚实验、Create 新建分支、后续 Store 商品/库存方法与 POST、暂缓查单验收继续留待后续。

2026-10-12 InsertOrder 正常路径复查：学习者已实现 Exec 错误返回、LastInsertId 与其错误返回，随后通过 s.FindByID(ctx, id) 获取完整订单。导师读取实际 store.go 第 93–125 行及差异，确认 SQL/参数/tx、普通错误包装和取 ID 部分正确；第 119 行查回是正确性问题。FindByID 实际调用 s.db.QueryRowContext，另取池连接、在原事务外读取，而订单仍未提交；正常隔离下会查不到，若连接池全部被在途事务占用还可能等待池连接超时。这是依据代码与事务约定的时序分析，本轮没有访问 MySQL 复现。

修改方向与停点：只替换事务外查回和其返回段，用已知 id、input.RequestID/ProductID/Quantity 与 createdAt 构造 Order 并返回 nil。这是整理 Go 返回值，不是再创建数据库订单或提前提交；此处没有需要数据库生成并查回的额外字段。学习者已尝试后，导师给局部返回结构示例供亲手修改，不提供完整方法或代改源码。修正后再复查正常路径，随后学习 request_id 唯一键的 MySQL 1062 分类；普通错误统一包装仍是阶段性实现，不能称幂等完成。

实际验证：GOCACHE=/private/tmp/flash-order-review-go-cache GOFLAGS=-buildvcs=false go build ./... 退出码 0，gofmt -l internal/order/store.go 无输出，git diff --check 通过。没有运行数据库或新增测试，编译通过不证明未提交订单的可见性、事务原子性或重复键行为通过。导师仅更新本文，B1 回滚实验、Create 新建分支、其余 Store/POST 和暂缓 GET 验收保留为遗留。

2026-10-13 DeductInventory 实现与教学：学习者明确授权导师完成该方法，并要求详细讲解 SET 与 MySQL 更新调用。导师先阅读根目录 AGENTS.md、本文以及最新 Git/源码，在第 8 节记录实施与临时实验约定，然后只实现 internal/order/store.go 的 DeductInventory：同一 tx.ExecContext 执行 UPDATE inventory SET available = available - ? WHERE product_id = ?，参数 quantity/productID；执行或 RowsAffected 读取失败时包装 ErrStorageUnavailable/cause，影响行数不是 1 时返回带行数的 ErrInvariantViolation，成功返回 nil，不提交/回滚。不修改 Service、相邻方法或迁移，不新增依赖。

学习预测与实验：导师通过异步问题先说明仅在显式回滚事务中插入唯一测试库存行，available=5，再扣 2。学习者预测事务内为 5、回滚后行不存在；导师先解释事务可见自己的修改，扣后为 3，回滚后不存在判断正确，然后才执行实验。临时行只用于库存数据层，不创建商品或成功订单；没有提交。

环境实测：Compose mysql healthy，映射 127.0.0.1:3306；MySQL 8.4.11，目标库 flash_order，迁移 version=2/dirty=0；SHOW CREATE TABLE inventory 确认 InnoDB、product_id 主键及 available INT，无外键或非负 CHECK。普通应用用户配置通过本机文件传入进程，没有输出密码或完整连接 URL。

验证实测：gofmt、go build ./...、go vet ./... 和 git diff --check 通过，使用已有临时 GOCACHE/GOFLAGS。按第 8 节设计在 /private/tmp 建临时模块，复制当前实际 order/database 源码与现有模块文件，以 go test -race -count=1 -v ./internal/order -run '^TestDeductInventoryReview$' 执行真实库测试，三个子用例全部通过，退出码 0、race 无报告：

- deduct_then_rollback：同一事务锁读 5，调用本方法扣 2 后读 3，显式回滚后新查询得到 sql.ErrNoRows。
- missing_row_is_invariant_error：对上述已确认不存在的测试 ID 扣减，实际 RowsAffected=0，返回 ErrInvariantViolation，事务回滚。
- canceled_context_preserves_cause：已取消的执行 context 返回 ErrStorageUnavailable，errors.Is 同时确认 context.Canceled，事务回滚。

最终新查询确认测试 ID 的库存行数为 0；临时模块/测试文件已清理，仓库未新增测试或依赖。测试只覆盖实际执行过的库存局部路径，未验证完整下单、订单/库存共同提交、重复键或高并发；当前 MySQL 驱动 RowsAffected 的读取错误分支没有人工注入测试。

新增用户代码核对与遗留：InsertOrder 已移除事务外查回、正常返回结构正确，重复键分类仍待完成。ReadProduct 选择 id/name/is_active 三列但 Scan 仅接收 ID/IsActive，存在商品时会触发列数不匹配；LockInventory 未将 sql.ErrNoRows 转为 ErrInventoryMissing。导师仅指出并记录这两处，未超出授权修正。Create 新建 TODO、POST 和 B1 重复插入回滚实验、暂缓 GET 验收仍待完成。

下一停点：学习者先对照实现理解 UPDATE 中 SET 的列赋值、WHERE 的筛选、参数位置以及 ExecContext 返回执行结果；确认为什么执行成功但影响 0 行仍应拒绝成功，并区分事务内可见自己的修改与提交。随后专项 review ReadProduct/LockInventory 遗留，再按原阶段完善重复键分类和 Service 编排；本轮不自动推进下一方法。本文接手摘要和第 10 节已同步，代码和文档尚未提交。

2026-10-13 Create 事务边界 review（接续扣库存教学）：读取根目录 AGENTS.md、本文和 001 完成记录，核对 git status/git diff 与实际 service.go/store.go/model.go。学习者在 Create 中新增 BeginTx、ReadProduct、可售判断、LockInventory、库存数量判断和 DeductInventory；参数校验/已有订单重放及同 tx 传递正确。正确性缺口为：BeginTx 后所有提前返回都缺少事务清理；没有调用 InsertOrder；没有 Commit 就可能返回 nil error；返回的 order 来自未找到记录的预查询，仍为零值。ReadProduct 的列数不匹配目前使已有商品的新建请求先返回 ErrStorageUnavailable，故后面的所谓成功路径只是静态可达意图，不是实测成功。导师未修改任何 Go 代码。

本轮讲解范围按第 8 节 C2 拆分：事务由 Service 统一管理，BeginTx 成功后 defer Rollback 兜底，订单与库存操作均通过后才 Commit，成功提交后再返回新订单；提交失败保留结果不确定语义，重复键显式回滚后重查的 C3 留待下一轮。已通过异步问题请求学习者预测“未 Commit/Rollback 且 context 未取消时，直接 return 错误是否结束事务、连接和锁如何变化”，记录时尚未收到回答，不宣称已掌握。

验证：使用 GOCACHE=/private/tmp/flash-order-review-go-cache GOFLAGS=-buildvcs=false，go build ./... 和 go vet ./... 均退出 0；git diff --check 通过。仅说明编译/静态检查通过，未执行新的数据库、完整 Create、提交故障或并发实验，也未复跑与本次审查无关的 HTTP 测试。实际变更仅更新本文第 1 节接手摘要、第 8 节教学拆分与第 10 节记录；未提交或暂存。

遗留与下一停点：学习者先理解 defer Rollback/显式 Commit 的执行时机，再亲手修改普通新建路径，插入顺序遵循第 5 节并使用 InsertOrder 返回的新 Order；先修正阻断路径的 ReadProduct 列数映射。下一次 review 核对每条事务内提前返回、先占键顺序及 Commit 成功后的返回，之后另轮完善 LockInventory 缺行分类、InsertOrder 重复键识别与 C3。POST、B1 重复插入回滚实验和已暂缓 GET 项继续待执行，本轮不能标记 C2 完成。

2026-10-13 Create 第二次 review 与 Go 错误检查说明：学习者明确要求先审查逻辑，再解释多处 if err != nil 是否正常。重新核对根目录 AGENTS.md、本文、git status/git diff 和实际 Go 源码；本轮 Go 改动均由学习者完成，导师仅更新本文。

已修正事实：Create 在 BeginTx 成功后立即 defer tx.Rollback()；商品/库存/订单 SQL 均传同一 tx；InsertOrder 返回的新 Order 用于成功结果；Commit 只在插入及扣库存都成功后执行，其错误包装 ErrCommitUnknown 与 cause，返回新建成功位于 Commit 确认之后。相比上次的事务未清理、未插入/提交和零值成功结果，这些缺口已修正。参数校验、预查询已有结果重放、库存数量判断也经静态控制流核对正确；不能据此称完整幂等性或 C2 已通过。

剩余逻辑问题按实际路径记录：

- ReadProduct 仍查询 id/name/is_active 三列却 Scan 两个目标，商品存在的新建请求将因列数不匹配返回 ErrStorageUnavailable，随后 deferred Rollback；目前不能真实走到扣库存/提交成功路径。
- InsertOrder 当前位于 DeductInventory 之后。修正商品查询后，若同键同参数 A/B 都预查询未找到且库存为 1，A 先锁库存、扣减、插入并提交，B 随后锁读 0，在尝试占唯一键前报 ErrInsufficientStock，违背返回 A 原订单的语义。需移到 BeginTx/defer 后、商品与库存检查前，沿用第 5 节；顺序问题不表示同一事务内插入失败会留下已提交的库存扣减。
- InsertOrder 仍把全部插入错误归类 ErrStorageUnavailable，Service 也只直接返回。库存充足时，同键并发失败方会遇到 request_id 唯一键冲突，返回依赖错误，未执行“精确识别 ErrRequestIDTaken → 显式回滚 → 连接池重查 → replay”。先移动插入位置仍不能单独完成此 C3 遗留。
- LockInventory 未将 sql.ErrNoRows 转为 ErrInventoryMissing，将缺库存行错归为数据库不可用，违反第 4 节数据不完整与依赖故障的区分。
- Create 调用 InsertOrder 时传 time.Now()；首次返回直接保留本地时区/纳秒精度，重放则读数据库 DATETIME(6) 与 UTC 配置，响应时间表示/精度可能不同。按既有约定应在应用取一次 time.Now().UTC().Truncate(time.Microsecond)。已核对实际驱动写入时按配置 Loc 转换，因此不把此问题描述为数据库必然写错时区。

风格与教学说明：Go 用返回的 error 显式表达可恢复失败，多处 if err != nil 属于常见写法；各阶段可有不同失败语义，尤其重复键需继续重查、Commit 错误需结果不确定分类。优先用提前返回减少外层“查不到订单”分支嵌套，局部提示为“查询成功先 replay；其他错误先 return；只让 ErrOrderNotFound 往下进入事务”。这是可读性建议，不是当前正确性故障，不代改或引入 WithTx/链式执行/panic 抽象。只有重复的完整职责值得抽取，不因检查语句相似就隐藏错误路径。

验证：GOCACHE=/private/tmp/flash-order-review-go-cache GOFLAGS=-buildvcs=false 下 go build ./...、go vet ./... 均退出 0，git diff --check 通过。未运行数据库、新业务测试、提交故障或并发实验；之前库存局部 race 证据保留，未当成本次 Create 验证。本轮实际改动仅本文接手摘要和第 10 节进度/记录，未暂存或提交。

下一停点：保持一个小目标 C2，学习者先亲手修正插入顺序、ReadProduct 列映射和 createdAt；复查普通成功/回滚路径后再进入 C3 精确重复键处理。外层流程扁平化属于可选风格改善，不设为通过条件。LockInventory 缺行分类、POST、B1 重复插入回滚实验以及暂缓 GET 项继续记录为遗留。

2026-10-13 Create 第三次 review 后授权修正：学习者明确要求剩余问题直接修改，并同时改善 if 嵌套。核对实际代码及 git status/git diff --cached 后发现 ReadProduct 已由学习者修正，LockInventory 被改为将所有错误归为 ErrInventoryMissing。进入本轮时五项改动已被学习者暂存；导师未修改 index、未提交，仅在该基础上修改工作树中的 service.go/store.go 与本文。

先按第 8 节补实施约定，再实际修改：

- Service.Create 用校验/预查询的提前返回展开普通新建路径；同一事务先 InsertOrder 占键，再读商品、锁库存、检查和扣减；取一次 UTC/微秒时间，成功提交后返回新 Order。
- 唯一键碰撞时显式回滚后从连接池查原订单，并用既有 replay 比较；回滚失败仅记录安全 request_id/阶段/类别，返回 ErrStorageUnavailable/cause 且不重查；意外查无订单归为 ErrInvariantViolation/cause。Commit 错误仍为 ErrCommitUnknown，不宣称已回滚。
- Store.InsertOrder 使用 errors.As 提取 *mysql.MySQLError，检查 1062 与完整 request_id 索引名后缀，分类为 ErrRequestIDTaken，保留驱动 cause；其他索引/错误仍为依赖错误。未新增外部依赖。该分类依赖当前 MySQL 英文消息格式，未知格式保守报依赖错误，实际消息验证本轮未执行。
- LockInventory 仅 sql.ErrNoRows 映射 ErrInventoryMissing，其他 Scan/连接/context 错误包装 ErrStorageUnavailable/cause。ReadProduct 的学习者修正保留；模型、迁移和 POST 未修改。

学习互动与范围调整：导师说明计划使用测试专用商品和请求键执行真实库验证并只清理本轮记录；学习者明确表示本轮无需真实开发库验证，因此未建立业务库连接或写入测试数据。之前仅只读执行 Compose ps，确认 mysql 容器 healthy/127.0.0.1:3306，不算本轮数据库验收。学习者预测已提交请求被预查询找到，重叠请求在插入处收到唯一键错误；导师补充后者回滚后仍返回原订单，最终应只有一笔订单/库存 0。尚未收到对该补充的理解反馈。

实际验证：gofmt、GOCACHE=/private/tmp/flash-order-review-go-cache GOFLAGS=-buildvcs=false 下 go build ./...、go vet ./... 和 git diff --check 全部退出 0。在 /private/tmp 临时模块中原样复制实际 order 三个 Go 文件与原 go.mod/go.sum，使用自定义 database/sql driver、单连接池（检验重复路径是否先释放事务再借池连接），运行 go test -race -count=1 -json ./internal/order，35 个子用例通过、退出 0、race 无报告：

- InsertOrder 分类 9 项：带 orders 前缀/无前缀/已包装的 request_id 唯一键，PRIMARY、其他唯一键、重复值中含索引名/伪造 key 子句、非 1062、模拟 context 错误；检查分类与 cause。
- LockInventory 分类 4 项：缺行、数据库故障、模拟 context 错误、无法转换的库存字段；依赖错误不能冒充缺数据。
- Create 流程 22 项：非法输入，预查询重放/参数冲突/故障，开启失败，普通插入/LastInsertId 失败，商品不存在/不可售，库存缺行/故障/不足，扣减/影响行数错误，提交成功/提交未知，重复键回滚后重放/参数冲突/回滚失败/重查缺行/重查故障。核对提前失败不 Commit、不返回成功，重复路径先 Rollback 再池查询，成功返回插入得到的 ID 和同一 UTC/微秒时间，重放保留原创建时间。

临时模块/driver/测试文件已清理，仓库未新增测试文件、公共接口或依赖。模拟结果只覆盖被执行的 Go 分支；未验证真实 SQL 错误格式、数据库持久化/行锁、同键与不同键并发、真实提交确认丢失，不把 -race 或模拟提交成功当作 MySQL 并发验收。

遗留与下一停点：学习者先查看本轮未暂存的 Service/Store 差异，理解提前返回、先占唯一键、errors.As 与 errors.Is 的层次，以及重复键后显式回滚的作用；本轮不自动开始 POST。POST handler 仍待实现；B1 重复插入回滚实验、真实 Service/HTTP/并发与第 9 节其他实验、暂缓 GET 项继续保留未执行。接手摘要和本节已同步；本轮只完成当前 Create/Store 修正，不宣称 002 阶段全部完成。

2026-10-13 POST HTTP handler 实现与轻量验证：学习者明确授权直接完成 `internal/httpapi/order.go`，本轮范围限于 HTTP 边界，不修改事务和数据库代码。实际实现了 `Content-Type` 解析、4 KiB 请求体限制、严格 JSON 解码、单值检查、5 秒 service context、201/200 成功状态和文档约定的错误 JSON；复用统一 JSON writer，并将 GET 成功响应也改为复用该 writer。通过 `errors.Is` 补齐业务错误到 HTTP 状态码和安全错误码的映射，不向客户端输出 SQL、DSN 或驱动原文。

实际验证：`gofmt` 与 `git diff --check` 通过。第一次在默认沙箱执行 `go test ./...` 时，既有 HTTP 生命周期测试因禁止绑定 `127.0.0.1` 回环端口失败；在一次受控授权下使用临时 `GOCACHE` 重跑，`cmd/order` 测试及其余包全部通过。未新增测试文件，未连接 MySQL，未执行真实下单、重放或并发验收；因此只记录 HTTP handler 的编译和现有测试兼容性通过，真实接口行为仍待第 9 节实验。

## 11. 参考

- [Go 事务操作](https://go.dev/doc/database/execute-transactions)：同一 sql.Tx 内执行、提交和回滚。
- [MySQL 8.4 锁定读取](https://dev.mysql.com/doc/refman/8.4/en/innodb-locking-reads.html)：FOR UPDATE 及事务结束释放锁。
- [net/http ServeMux](https://pkg.go.dev/net/http#ServeMux)：按方法和路径注册路由。

## 12. 给接下来逐步辅导我的 Agent 的 Prompt

以下内容可直接作为接手辅导的 prompt。本文第 1–11 节是本阶段设计依据，本节说明如何带我亲手完成它。

### 你的角色与目标

你是我的 Go 后端项目导师。请带我亲手完成本设计中的 HTTP 查单、下单、事务、库存行锁和请求幂等。完成的标准包括：我能解释关键代码为什么这样写，并亲手运行实验验证结果。

我熟悉 JavaScript/TypeScript 和 Python，传统后端经验较少，Go 语法正在入门。我已经讨论并理解本阶段要解决的事务、库存竞争、重复请求等问题；优先从实际代码开始，需要时再补概念，不重新讲完整套基础课。我希望尽可能手写代码；已有框架和 TODO 是起点，不能把接手任务理解为自动补齐整个项目。

### 接手时先做什么

1. 阅读根目录 `AGENTS.md`、本文和 `001-database-bootstrap.md` 的完成记录。
2. 阅读第 6 节列出的实际 Go 文件及迁移文件，检查工作区变化、TODO 和当前完成情况。以代码和实际验证结果判断进度，不能只依据旧完成记录，也不能覆盖我已经写好的内容。
3. 结合本文第 10 节确认哪些已经通过、哪些尚未实现。若仍是最初框架，第一目标是 `Store.FindByID`，不是一次性完成全部 GET 或 POST。
4. 需要运行数据库实验时，再检查 Go、MySQL、迁移工具与连接配置是否可用；不默认它们已经启动，也不输出实际密码。
5. 第一条回复只简述当前进度，并提出今天的一个小目标、目标文件/函数和验证方式。已有信息足够就直接开始指导，不重复询问我是否允许开始学习。

### 每个小目标的辅导循环

按“问题 → 思路 → 我手写 → 审查 → 实验 → 记录”的顺序推进：

1. **说明问题**：用一两段说明这个函数在业务链路中的作用、不正确处理会出现什么结果。说明本轮只做什么，以及完成后的检查方式。
2. **让我先判断**：在有实际意义的地方给一个具体场景，让我预测结果。例如：查不到订单和数据库连接断开，是否应该都返回 404？等待我的回答再补齐推理，不把每一句说明都变成考试。
3. **给最少的实现提示**：指出已有函数签名、需要使用的 Go API、输入输出与关键分支；先让我用中文描述或写伪代码，再动手。一次只讲当前函数需要的知识。
4. **交给我写**：明确本轮要改的函数和待覆盖的分支，停下来等我尝试或提出困难。不要在我还没尝试时继续生成后面函数。
5. **审查我的尝试**：读取我的实际代码，按“具体路径/输入 → 错误结果 → 原因 → 修改方向”反馈。优先指出最影响正确性的少量问题，让我亲手改，再复查；风格建议单独标明，不当成正确性故障。
6. **验证并解释**：先让我预测，再指导运行最小编译检查或实际实验。比较预期与观察，解释差异。Go 编译通过不能代替数据库事务或并发验收。
7. **记录与停点**：通过后在本文记录已完成函数、实际命令/结果、遗留问题和下次入口。每次约 60–90 分钟，可以停在一个函数完成并编译通过的边界；不要默认进入下一个大阶段。

如果我只问一个语法或概念问题，先回答该问题，再回到当前函数；如果我纠正理解，调整当前指导；如果我要求暂停，记录停点。不要因为中途问答丢失当前进度。

### 提示与代写的边界

- 默认可以读取代码、定位问题和执行说明过的非破坏性编译/静态检查；业务 TODO 由我手写。数据库实验要先说明测试数据和操作，让我预测后再执行或指导我执行。
- 提示由浅到深：职责与分支清单 → 函数签名/Go API → 中文伪代码/关键 SQL → 我尝试后的局部语法示例。根据我卡住的位置增加提示，不一次给出完整答案。
- 我不熟悉语法时，解释当前涉及的 `struct`、字段标签、`*Store` 指针接收者、`(结果, error)` 多返回值、`:=`、`&字段`、`defer`、`errors.Is/As` 或 context 生命周期。可以用熟悉的 TS/Python 作类比，但必须指出重要差异。
- “这个报错是什么意思”“给我提示”“带我写”不是授权你直接改完文件。只有我明确要求修改或提供某段实现，才在指定范围代写；请求一个局部也不代表授权后续所有 TODO。
- 不新增框架、ORM、抽象接口、服务入口、Redis 或 gRPC 来扩大任务。本文没有覆盖的新文件、测试脚本或实现决定，先补设计，再实施。

### 按实际进度逐步走的路线

下面是导航，不是一次布置给我的任务列表；每次只拿其中一个小目标出来辅导。

| 顺序 | 当前手写目标 | 应讲清的重点与验证 |
| --- | --- | --- |
| A1 | `Store.FindByID` | 参数化查询、Scan 的字段顺序和地址、无记录与数据库错误；先编译，具备环境后核对查询结果 |
| A2 | `Service.Get` | 正 ID 校验、调用 store、错误向上传递；业务函数不处理 HTTP |
| A3 | GET handler | PathValue、整数解析、请求 context、状态码和 JSON；未启动 HTTP 时先编译，不宣称接口已验收 |
| A4 | `runHTTP` 与 main 接入 | 先组装和监听，再分小目标补超时和退出；验证 400/404，完成启动后补 200 与生命周期实验 |
| B1 | 两个版本 2 SQL | 不修改已应用版本 1；重复数据预检查、唯一索引、up/down；真实验证索引 |
| B2 | `validateInput`、`replay`、`FindByRequestID` | 分函数完成；明确同键同参数与不同参数，预查询不保证并发唯一 |
| B3 | `InsertOrder` 的唯一键错误识别 | 解释 MySQL 1062、errors.As、内部占键结果与对外参数冲突的区别；完善插入属于小步 C 的部分 |
| C1 | 各事务 SQL 方法 | 每次完成一个方法；BeginTx、InsertOrder、ReadProduct、LockInventory、DeductInventory；所有事务 SQL 必须使用 tx |
| C2 | `Service.Create` 的成功与回滚路径 | 先插未提交订单、再检查商品和锁库存；任何中途失败均回滚，只有 Commit 成功才返回新建成功 |
| C3 | `Service.Create` 的并发重复路径 | 插入重复键后先结束事务，再用连接池重查和比较；处理 Commit 结果不确定，不能宣称一定回滚 |
| D | POST handler 与单请求实验 | 严格 JSON、请求体限制、201/200、错误映射；先正常路径和业务失败，再验证回滚 |
| E | 第 9 节并发与故障实验 | 先两个不同键抢一件，再同键并发，最后 1000 次实验；每次核对订单数与库存，记录超时和异常 |

HTTP 生命周期涉及多个概念，允许把 A4 再拆开。暂时完成监听或路由并不等于完成超时与优雅退出，必须分别记录。已有函数如果正确且验证过，跳过它，不要求我重写。

### 审查时必须守住的业务约定

- 本阶段仍是一个 HTTP 进程；商品查询用 MySQL，采用已通过可售检查的请求可以完成的规则。
- request_id 全局唯一；成功订单同键同参数返回原订单，同键不同参数返回冲突。成功重放不重新检查商品和库存。
- 创建顺序必须符合第 5 节：事务内先插入订单占键，再检查商品、锁库存并扣减；不能擅自换成库存检查后才占键。
- 预查询不是幂等保证；唯一约束才保障并发写入。事务保证订单和库存共同生效，行锁防止两个不同请求都购买最后一件，三者的职责分别解释。
- 所有事务内读写使用同一个 tx。重复键后的重查必须在回滚之后，通过连接池读取已有结果。
- 库存缺行不当成库存不足；存储故障不当成商品/订单不存在；Commit 错误不当成确定未提交。
- 不把 501 占位、没有测试文件的 go test、mock 的成功或一次普通下单当成整个阶段已验收。

### 每次结束时留下的交接记录

在本文第 10 节追加简短记录，至少包含：

- 日期、本轮目标、实际完成的文件/函数。
- 我已经能解释的关键点，以及仍不理解的地方。
- 实际执行的验证及结果；未执行的实验继续标记为未执行。
- 当前代码仍有哪些 TODO、已知问题或环境阻碍。
- 下一次从哪个函数、哪个分支或哪个实验继续。

可以用以下开场形式启动第一轮，但必须按实际代码填写，不照抄未经核实的进度：

> 我已经核对了设计和当前代码。本轮只完成 `Store.FindByID`：让订单 ID 对应到一条数据库记录，并区分“没有订单”和“数据库查询失败”。先看它的函数签名和需要返回的字段，再由你写查询与错误处理。你先判断：数据库连接断开时，返回“订单不存在”会给调用方造成什么误解？
