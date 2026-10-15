# 004：Redis 商品缓存与并发保护设计

## 1. 状态、目标与学习约定

- 日期：2026-10-15。状态：设计稿；本轮整理类型与文件职责、确定实施默认值，没有修改应用、Compose、依赖或数据库，没有执行缓存实验。
- 前置：`001-database-bootstrap.md`、`002-order-purchase.md` 和 `003-order-learning-experiments.md`。学习者最新反馈已完成此前测试；本轮不独立复验，也不因历史记录尚未同步而重新安排旧阶段。
- 目标：观察热门商品重复读取，再加入缓存；通过实验理解命中、回源、过期、失效、请求合并、回源限流和熔断。
- 当前源码事实：商品仍由 `Store.ReadProduct(ctx, tx, productID)` 在下单事务内查询；尚未接入 Redis。本文是接下来要实现的目标，不是已实现事实。
- 单次约 60–90 分钟，只推进一个小目标。由学习者手写，Agent 解释、渐进提示和 review；本文不授权代写整个阶段。
- 第 2 节记录会话已明确的业务规则；第 3 节规定事务边界；第 6 节集中定义接口、结构体与文件归属。学习者本轮已授权导师决定剩余配置，第 7 节据此确定实施默认值，后续 Agent 无需逐项再次索取确认。未覆盖的决定先更新本文，再改实现。

### 范围

本阶段仍是一个 Go HTTP 服务。加入 Redis 商品缓存，保持 MySQL 的库存、订单、请求唯一键和成功边界。

不在本阶段加入支付、多商品下单、商品服务/gRPC、通知 Worker、Redis Streams、Redis 扣库存、分布式锁、强一致缓存、CDC 或 outbox。下单后通知仍是后续目标，另写设计。

本稿对 002 的阶段性调整只有：商品信息读取位置和可售检查的数据来源。事务原子性、行锁、幂等和提交结果不确定的处理继续保留。

## 2. 已确定的业务规则

| 项目 | 约定 |
| --- | --- |
| 缓存字段 | 只缓存商品 `id`、`is_active`，不缓存购买判断所依赖的库存数量 |
| 原始数据 | MySQL 保存商品原始数据；Redis 是可删除、可重新生成的副本 |
| 查询方式 | cache-aside：先 Redis，未命中才查 MySQL，成功查到商品后回填 |
| 可售判断 | 命中 Redis 时直接使用其中的 is_active，不再查询 MySQL 二次核对 |
| 不可售命中 | is_active=false 也是命中，不是缓存未命中 |
| TTL | 基础值 30 秒；每次回填设置，读取命中不续期 |
| 更新失效 | 先更新 MySQL，事务提交成功后 DEL 对应缓存，不直接更新缓存副本 |
| 陈旧容忍 | 接受刚上架时暂时误拒，也接受刚下架时新请求暂时放行 |
| 购买正确性 | 库存读取与扣减、订单插入仍由 MySQL 事务与行锁保证；request_id 仍由唯一约束兜底 |
| 订单成功 | 订单与库存事务 Commit 成功才返回新建成功；通知不属于本阶段 |

TTL 从实际缓存写入开始计算。普通“提交后删除”仍可能发生旧查询晚回填，因此不能保证“商品更新后最多 30 秒内所有读取必定新鲜”，也不声称强一致。

不对这套机制做生产容量承诺。减少的是商品查询；现有幂等查询、订单写入、库存行锁和扣减不会因缓存命中而消失。

## 3. 事务边界

### 3.1 顺序

```text
校验请求
  → 用 request_id 预查已提交订单
      → 找到：比较参数，重放或返回冲突
      → 未找到：事务外准备商品查询结果
          → 查询依赖故障 / 回源受限 / 请求已取消：返回错误，不开事务
          → 查到商品，或确认商品不存在：保留结果
              → BeginTx
              → INSERT orders，占住 request_id（尚未提交）
                  → 重复键：先 Rollback，再用池查询赢家并 replay
                  → 其他错误：回滚并返回
                  → 插入成功：根据保留的结果判断存在、可售
                      → 不符合：回滚订单插入
                      → 符合：SELECT inventory ... FOR UPDATE
                          → 检查数量 → 扣减 → Commit → 返回新订单
```

**读取商品提前，最终业务拒绝仍在成功占键之后。** 例如 A 预查询没找到订单，随后 B 用相同键下单成功，而 A 读到商品已下架；A 不能直接用“不可售”覆盖已有成功结果，要先通过唯一键竞争进入原订单重放。

商品读取返回 `ErrProductNotFound` 时先保留这个业务结果；商品可售为 false 时也先保留。依赖故障、取消或过载不是商品不存在，不必为它们开启下单事务。故障期间客户端可能仍需使用原键重试确认结果。

### 3.2 查询对象与连接

- 商品回源调用共享 `*sql.DB` 的 QueryRowContext 并 Scan，不接收订单 `*sql.Tx`。
- Scan 完成后查询连接归还池，再开始订单事务；不要求这两次使用同一物理连接。
- 下单事务内所有订单和库存 SQL 仍使用同一个 tx；不从事务内的缓存层再次借池连接。
- singleflight 只共享 Product 值或商品读取错误，不共享 tx、连接或一次购买的执行结果。
- 原实现 tx.QueryRowContext 复用已有事务连接，并没有额外借连接的问题；风险来自“已经持有事务连接，再调用独立 db 查询”的错误组合。
- 现有池上限为 5。保持一个池，不通过增大池或再建一个池掩盖资源等待；新结构避免 5 个事务占满池后又各自等待池连接查询商品。

### 3.3 取舍

优势是事务不等待 Redis、共享查询或回源名额，商品读取也可跨请求复用。代价是商品查询与库存事务不属于同一快照、重叠重复请求可能提前多读一次商品、不可售请求仍可能插入后回滚。当前接受一次商品检查结果后继续下单，且已接受缓存陈旧，这些取舍符合规则。

即使尚未加入 Redis，普通商品 SELECT 也可以提前：把它放在事务内不等于锁住商品行。库存必须继续在事务内锁定读取，不能一起提前后直接用于扣减。

## 4. 缓存数据与错误语义

本节规定缓存数据格式与返回语义；接口、结构体和方法签名统一放在第 6 节。不增加 HTTP 接口或数据库迁移。

### 4.1 Key 与值

- Key：`flash-order:product:v1:<product_id>`。v1 标识编码版本，不是数据库实时版本号，不提供一致性保证。
- 类型：Redis String；值为 JSON，例如 `{"id":123,"is_active":false}`。
- 两个字段都必须存在、类型正确，id 为正且等于查询 ID。解码时要能区分“缺少 is_active”与合法 false，不能让 Go 零值掩盖损坏数据。
- 只回填 MySQL 成功读到的商品；基础版不缓存商品不存在，穿透保护留为后续讨论项。
- 使用一次带过期参数的 SET 写入值与 TTL，避免先 SET 后 EXPIRE 中途失败留下永不过期的 key。
- 缓存损坏视为缓存不可用：记录安全分类后回源，不转为“不可售”或“商品不存在”；成功回填会替换坏值。

### 4.2 返回值与错误类别

缓存 Get 的 bool 表示 key 是否有效命中，和 Product.IsActive 无关。未命中是 found=false、err=nil；访问失败是 err!=nil；只有商品回源确认无行才返回 ErrProductNotFound。

ProductReader 对业务层保留错误类别：商品不存在返回 ErrProductNotFound；源查询故障、共享任务预算耗尽等包装 ErrStorageUnavailable 并保留 cause；回源过载返回 ErrProductLookupBusy。等待者取消保留 context cause，不能落入默认 500 或被当成商品不存在；客户端已断开时不保证能收到响应。Redis 层错误在读取器中触发降级，不直接成为商品可售结论。

Service 构造改为接收 Store 和 ProductReader；使用小接口是为了明确商品查询边界及可控实验，不为每张表建立通用接口。Order、CreateInput、HTTP 成功结构保持原约定。

### 4.3 商品更新

当前没有商品管理接口，先不增加。学习实验在明确的测试商品上执行“更新 SQL 并提交 → 调用缓存删除/执行对应 DEL”。直接执行 SQL 不会自动通知 Redis。

Delete 删除了 0 个 key 也是成功；更新失败不执行后续 DEL；DEL 失败时数据库已经提交，不能报告它已回滚。本阶段记录失败并依靠 TTL，不增加自动删除重试或重试队列；旧值晚回填竞态见 TODO.md，延期处理。

## 5. 商品读取与并发保护

### 5.1 基础 cache-aside

1. 从请求 context 派生短超时，读取 Redis。
2. 有效命中直接返回，包括 IsActive=false。
3. 未命中、缓存损坏或 Redis 故障进入 MySQL 回源；用原请求的剩余预算，不能继续使用已被取消的 Redis 子 context。
4. MySQL 返回的错误保留分类；无行仍为商品不存在。
5. 查到商品后尝试 SET+TTL；回填失败只影响缓存，仍返回已经查到的商品，不把它伪装为购买失败。

基础版先观察故障与集中回源，再按后续小步加入下面的保护。基础版尚不能宣称具备完整雪崩保护。

### 5.2 同 key 请求合并

- 选用 `golang.org/x/sync/singleflight`，一份共享 Group 放在 CachedProductReader 结构体内；启动时创建一次，不在 handler 每次调用中创建。
- 正常命中仍可直接返回；未命中/故障时按商品 key 进入 DoChan，同进程内重叠调用共享结果。
- 取得执行权的函数，在允许访问 Redis 时再检查一次缓存，避免第一次未命中后其他调用已经完成回填。
- 确需回源才申请名额：SQL/Scan 完成即释放，再回填 Redis。等待共享结果的请求不各自申请名额。
- 不合并订单创建、扣库存或不同商品；不宣称跨实例全局只查一次；不把 singleflight 当作持久缓存。

### 5.3 共享查询的取消与生命周期

- 等待者用 DoChan 返回的结果通道与自己的 ctx.Done 做 select，可以独立结束等待。
- 执行共享查询不能直接使用第一个 HTTP 请求的可取消 context，否则它退出会取消其他请求仍需要的查询。
- 使用读取器管理的服务级工作 context，加 1 秒总超时；任一等待者退出不取消共享工作。所有等待者都退出时，基础实现仍允许查询在短预算内完成，不增加引用计数取消机制。
- 读取器 Close 时拒绝新工作、取消工作根 context，并等待已登记的共享任务结束；不能留下无限期后台任务。
- HTTP 优雅退出期间允许在途请求/共享读取完成。HTTP 停止后关闭读取器并等待共享任务，再关闭 Redis 客户端，最后由 main 关闭 MySQL。不能在收到信号时立刻取消所有共享读取，否则破坏现有优雅退出目标。

### 5.4 回源并发限制

- 使用容量为 2 的进程内 channel 信号量，不依赖 Redis。
- 位于 singleflight 真正执行的回源路径中，SQL 前申请；不能占着事务连接再等名额。
- 名额耗尽立即返回过载错误，不增加等待队列。名额必须在成功、失败和取消时释放。
- 名额仅覆盖商品 SQL 与结果读取；可用局部 helper/defer 保证返回后已释放，再执行 Redis 回填。
- 这不限制所有库存事务、订单查询或全服务流量；不等于每秒限流。连接池大小保持现有值，池等待仍受查询 context 限制。
- 新增 `ErrProductLookupBusy`，HTTP 映射 503、code=`product_lookup_busy`；不映射为库存不足。

### 5.5 Redis 熔断

- Go 进程内共享状态机：Closed（正常）、Open（跳过）、Half-Open（少量探测），使用短临界区协调状态，不持锁执行网络操作。
- 请求触发恢复：Open 冷却后到来的第一个允许请求取得探测资格；无需另建定时探测 goroutine。没有流量时保持状态即可。
- 同一个 ProductCache 的 GET/SET/DEL 共用一个熔断器。Open 时跳过这三种命令；GET 转受控回源，SET/DEL 跳过并记录。商品更新后的 DEL 被跳过仍是缓存失效失败，不得当成数据库回滚。
- Half-Open 只允许一个 GET 探测；其他缓存访问跳过。探测成功恢复 Closed，失败重新计时进入 Open。
- 正常 Redis 命令成功清零连续失败计数；GET 正常未命中也算通信成功，合法 false 不计故障。坏 JSON 单独记录数据问题，不算 Redis 通信故障。连接失败、操作自身超时、连接池耗尽及 Redis 返回的命令错误计为失败；父 context 取消或到期、中途因关闭取消，不计失败也不清零。跳过的命令不计数。
- 冷却、探测和计数是客户端保护，不修复 Redis，也不减少所有 MySQL 需求；回源限制必须仍然执行。
- 实现时区分不同状态代次，避免上一轮尚未结束的请求覆盖新探测结果；并发实验不能放出多个探测。

### 5.6 TTL 随机偏移

本阶段关闭 TTL 随机偏移，固定 30 秒，便于观察过期与数据陈旧。以后确需观察多 key 同时失效时，另行补设计和实验，不作为当前阶段的待定项。

随机偏移只分散过期时点，不解决单热门 key 的重叠回源、Redis 整体故障或缓存一致性。

## 6. 目录、文件与职责

本节是接口、结构体、持有关系和文件归属的统一入口。以下均为目标设计，分 P0–P5 逐步实现，不要求一次创建全部框架；方法签名用于教学，不是要求 Agent 代写完整代码。

### 6.1 类型与文件对应

| 文件 | 接口 / 结构体 | 主要字段、依赖与职责 | 引入阶段 |
| --- | --- | --- | --- |
| `internal/order/model.go` | `Product`：已有结构体 | 保留 `ID int64`、`IsActive bool`。同文件新增错误值 `ErrProductLookupBusy`，它不是接口或结构体 | 保留；P4 加错误 |
| `internal/order/service.go` | `ProductReader`：新增接口；`Service`：已有结构体 | 接口只约定 GetProduct；Service 持有 `store *Store` 和 `products ProductReader`，编排订单与库存事务，不处理 Redis 细节 | P1 |
| `internal/order/store.go` | `Store`：已有结构体，也是 ProductReader 的直接读取实现 | 持有共享 `db *sql.DB`。ReadProduct 去掉 tx 参数；新增 GetProduct 薄适配调用 ReadProduct，复用同一份查询 SQL | P1 |
| `internal/order/product_cache.go` | `ProductCache`：新增具体结构体 | 持有 `client *redis.Client`、`breaker *RedisBreaker`、TTL 和单次操作超时。只处理 key、JSON、GET/SET/DEL 与熔断准入，不查 MySQL | P2；P5 加 breaker |
| `internal/order/product_reader.go` | `CachedProductReader`：新增具体结构体，也是 ProductReader 的缓存读取实现 | 持有 `cache *ProductCache`、`source ProductReader`、`flights singleflight.Group`、`limiter *ProductLimiter` 及共享任务生命周期状态。编排缓存、回源、合并和名额 | P2；P3/P4 逐步加保护 |
| `internal/order/product_limiter.go` | `ProductLimiter`：新增具体结构体 | 持有 `slots chan struct{}`，容量 2；提供非阻塞申请和释放，仅保护商品源查询 | P4 |
| `internal/order/redis_breaker.go` | `RedisBreaker`：新增具体结构体 | 持有互斥锁、状态、连续失败数、冷却起点、探测标记、状态代次及可替换时钟。管理准入和结果反馈，不负责回源 | P5 |
| `internal/rediscache/redis.go` | `Config`：新增配置结构体；`redis.Client`：第三方结构体 | Config 包含地址、DB、用户名、密码；客户端工厂统一应用第 7 节的固定连接参数，返回共享 `*redis.Client` | P0 |
| `cmd/order/redis.go` | 不新增业务接口或结构体 | 从环境变量加载、校验并返回 `rediscache.Config`；非法配置报错，数值默认值按第 7 节执行 | P0 |

`ProductReader` 是行为约定，`CachedProductReader` 是有字段、能创建实例的实现；不能把二者都叫接口。Go 通过方法集合隐式满足接口，不写 implements。这里不为 ProductCache、Limiter、Breaker 各增加一套同名接口。

`source ProductReader` 在真实启动时指向同一个 `*Store`；这样单元实验可以注入受控读取器，暂停 SQL 替身、计数或制造失败，无须为了测试再增加另一套商品源接口。不要将 CachedProductReader 自己注入为自己的 source，否则会递归调用。

### 6.2 业务接口与数据访问签名

定义在 `internal/order/service.go`，由消费它的订单业务声明：

```go
type ProductReader interface {
    GetProduct(ctx context.Context, productID int64) (Product, error)
}

type Service struct {
    store    *Store
    products ProductReader
}

func NewService(store *Store, products ProductReader) *Service
```

定义在 `internal/order/store.go`：

```go
func (s *Store) ReadProduct(ctx context.Context, productID int64) (Product, error)
func (s *Store) GetProduct(ctx context.Context, productID int64) (Product, error)
```

ReadProduct 从传入 context 派生最长 1 秒的商品查询预算并执行 MySQL 查询，不能延长父预算；GetProduct 只转调它。P1 令 Service 的 products 指向 Store，P2 改为指向 CachedProductReader，Service 的接口和事务流程不用因此再改一遍。

定义在 `internal/order/product_reader.go`：

```go
func (r *CachedProductReader) GetProduct(ctx context.Context, productID int64) (Product, error)
func (r *CachedProductReader) Close() error
```

可分别在两个实现文件中用编译检查表达实现关系：

```go
var _ ProductReader = (*Store)(nil)
var _ ProductReader = (*CachedProductReader)(nil)
```

ProductReader 不包含 Close：业务只需要读商品；共享工作生命周期由启动层持有具体读取器并管理。Store 的适配不新增一份数据库连接池，也不新增第二个商品服务。

### 6.3 缓存与保护组件的设计边界

`internal/order/product_cache.go`：

```go
type ProductCache struct {
    client  *redis.Client
    breaker *RedisBreaker
    ttl     time.Duration
    timeout time.Duration
}

func (c *ProductCache) Get(ctx context.Context, productID int64) (Product, bool, error)
func (c *ProductCache) Set(ctx context.Context, product Product) error
func (c *ProductCache) Delete(ctx context.Context, productID int64) error
```

缓存 JSON 解码使用本文件内的私有结构体，`is_active` 用 `*bool` 判断是否缺失；ID 必须为正且匹配查询 ID。解码后才转成业务 Product，业务模型不为缓存编解码增加 JSON 标签或可空字段。breaker 在 P2 尚未接入时为 nil，表示执行正常短超时调用；P5 统一在 ProductCache 内接入，不能让不同调用点绕过熔断。

`internal/order/product_reader.go` 的主要字段：

```go
type CachedProductReader struct {
    cache   *ProductCache
    source  ProductReader
    flights singleflight.Group
    limiter *ProductLimiter

    workCtx context.Context
    cancel  context.CancelFunc
    mu      sync.Mutex
    closed  bool
    workers sync.WaitGroup
}
```

- P2 先实现 cache-aside；P3 再加入 flights 和共享工作状态；P4 再接入 limiter。尚未接入的保护不提前宣称生效。
- source 的每次调用受 1 秒查询预算约束。P3 共享工作从读取器工作根 context 派生 1 秒总预算，覆盖二次查缓存、回源和回填；等待者自己的 ctx 只决定自己何时离开。
- mu 协调 closed 检查与工作登记；Close 先阻止新工作，再取消并等待。不能出现 Close 已开始 Wait、另一条路径才对同一轮任务 Add 的竞态。DoChan 中实际执行的函数也必须遵守关闭与登记规则。
- limiter 仅在实际 source.GetProduct 前申请，SQL 与 Scan 返回就释放，再调用 cache.Set；不围住缓存 GET，也不让所有等待者分别占名额。

`internal/order/product_limiter.go` 使用容量为 2 的 channel：TryAcquire 立即返回能否取得名额，Release 归还已取得的名额。读取器负责在成功、错误、取消各路径成对释放；未取得名额的调用不能 Release。

`internal/order/redis_breaker.go` 提供两组内部操作：执行前申请准入，执行后反馈成功/失败/忽略。准入结果携带状态代次和是否为探测的信息；反馈只作用于对应代次。状态、准入凭据等私有辅助类型均定义在本文件，不是额外业务接口。只在读写状态时短暂加锁，网络调用在锁外执行；时钟可注入以验证 10 秒冷却而不真的等待。

Open 被跳过的 GET 返回缓存不可用错误，让读取器受控回源；被跳过的 SET/DEL 返回可识别的跳过错误，不能伪装为已经成功写入或删除。ProductCache 不关闭其持有的 Redis 客户端，Limiter/Breaker 也不持有 MySQL。

### 6.4 持有关系与实例生命周期

```text
OrderHandler → Service
                 ├─ store: *Store ──────────────────────→ 共享 *sql.DB
                 └─ products: ProductReader
                      └─ *CachedProductReader
                           ├─ source: ProductReader → 同一个 *Store
                           ├─ cache: *ProductCache
                           │    ├─ client: 共享 *redis.Client
                           │    └─ breaker: *RedisBreaker
                           ├─ flights: singleflight.Group
                           ├─ limiter: *ProductLimiter
                           └─ 工作 context、取消、任务登记与关闭状态
```

上图是 P5 完成后的组合，不是继承关系。main/runHTTP 启动时创建一份 Store、缓存读取器及其保护状态，注入同一个 Service/Handler。不同 HTTP 请求共享这些对象，调用参数、等待 context 和订单 tx 各自独立。

启动层管理资源关闭：HTTP 停止接收并等待在途请求 → CachedProductReader.Close 取消并等待共享工作 → Redis client.Close → main 关闭 MySQL。持有依赖指针不代表负责关闭依赖；Service、Store、ProductCache 不抢先关闭共享连接。

### 6.5 其余文件与验证职责

测试仅验证实际风险，不编写与薄包装实现逐行对应的测试。

```text
.agents/.design/004-redis-product-cache.md   本设计和阶段记录
cmd/order/
  main.go                    MySQL/Redis 生命周期；读取器清理顺序
  http.go                    创建并组装第 6.4 节的一份共享实例
  redis.go                   加载 rediscache.Config，见 6.1
internal/rediscache/redis.go  Config 与 Redis 客户端工厂，见 6.1
internal/order/
  model.go                   Product 与错误值，见 6.1
  store.go                   Store 与直接读取实现，见 6.2；库存/订单 SQL 保留
  service.go                 ProductReader、Service 与第 3 节事务编排
  product_cache.go           ProductCache，见 6.3
  product_reader.go          CachedProductReader，见 6.3
  product_limiter.go         ProductLimiter，见 6.3
  redis_breaker.go           RedisBreaker，见 6.3
  product_cache_test.go      缓存编解码和 false/缺字段/错误 ID 的区别
  product_reader_test.go     命中/回源/失败、同 key 合并与取消，用可控依赖验证
                             同时验证名额上限、立即拒绝及各返回路径释放
  redis_breaker_test.go      状态、冷却、唯一探测、并发和旧请求结果处理
  service_test.go            有针对性地验证新边界与同键结果优先，不以 mock 证明数据库锁
internal/httpapi/order.go     P4 增加 product_lookup_busy/503；保留取消语义
deploy/compose/
  compose.dev.yaml           增加独立 Redis 服务，保留已有 MySQL 数据卷
  .env.dev.example           补 Redis 配置模板
go.mod / go.sum              按实施步骤加入并锁定依赖
.learn/R04/                  后续实验资料，继续学习者已有 .learn 惯例
  README.md                  环境、测试商品/键、命令、实际证据与清理记录
```

新增真实集成测试或 .learn/R04 请求脚本时，先在本文补具体文件、输入输出及清理约定，不临时往生产 handler 添加故障参数。

## 7. 配置、依赖和实施约定

### 7.1 环境与依赖

- Go 模块仍为现有模块；MySQL driver 与表结构不变，不新增迁移。当前 go.mod 为 Go 1.27.1；P0 检查本机实际工具链，不凭文档假定安装状态。
- P0 使用 Redis 镜像 `redis:8.2.10-bookworm`、客户端 `github.com/redis/go-redis/v9 v9.23.0`；P3 加入 `golang.org/x/sync v0.24.0` 并使用其中的 singleflight。两个 Go 模块的 go.mod 均要求 Go 1.26.0，低于项目声明版本；实施时锁入 Compose/go.mod/go.sum，不使用 latest。依据为 [Redis 官方镜像清单](https://github.com/docker-library/official-images/blob/master/library/redis)、[go-redis v9.23.0](https://github.com/redis/go-redis/blob/v9.23.0/go.mod)、[x/sync v0.24.0](https://github.com/golang/sync/blob/v0.24.0/go.mod)。版本已选定，不代表本机已下载或已完成运行验证。
- 沿用现有 Compose，新增独立 Redis 服务并加入 backend 网络。Go 仍在主机运行；Redis 映射 `127.0.0.1:6379:6379`，`REDIS_ADDR=127.0.0.1:6379`、`REDIS_DB=0`。
- 本机开发默认不启用 Redis 认证，REDIS_USERNAME/REDIS_PASSWORD 默认空；加载器保留这两个可选环境变量以连接已有带认证的实例。地址格式、端口范围与 DB 非负整数在本地校验；非法配置启动失败。凭据不写日志，沿用忽略 .env.dev、只提交 example 的规则；Go 不自动读取该文件。
- 本阶段 Redis 仅存可重建缓存：关闭 RDB 快照（save 为空）和 AOF（appendonly no），不加持久化数据卷；`maxmemory=64mb`、`maxmemory-policy=allkeys-lru`，Compose 容器 `mem_limit=256m`。便于学习重建和缓存淘汰；64mb 是 Redis 数据内存阈值，256m 是容器资源上限，两者含义不同。
- Redis 健康检查使用 redis-cli ping，间隔 5 秒、超时 1 秒、重试 3 次；用于观察容器健康，不作为 Go 服务必须等待 Redis 健康的启动条件。
- 以后引入 Streams 时重新设计 Redis 持久化、淘汰和消息保留，不能直接将通知数据放入当前可丢弃的缓存配置。

### 7.2 已确定的学习默认值

以下参数按本机学习选择，优先让超时、过载和熔断行为可观察，不作为生产容量参数。除地址、DB、用户名和密码外，先集中使用代码常量/初始化配置，不为每个数值增加环境变量。

| 项目 | 实施值 | 阶段 |
| --- | --- | --- |
| TTL | 固定 30 秒；命中不续期；随机偏移关闭 | 基础缓存 |
| 单次 Redis GET/SET/DEL 预算 | 各 100ms，受上层剩余预算约束 | 基础缓存 |
| Redis 建连/读/写/池等待 | DialTimeout、ReadTimeout、WriteTimeout、PoolTimeout 均为 100ms；ContextTimeoutEnabled=true | 客户端初始化 |
| Redis 连接池 | PoolSize=5、MaxActiveConns=5、MinIdleConns=0；仅用普通 GET/SET/DEL，不启用 pipeline | 客户端初始化 |
| Redis 命令重试与拨号尝试 | MaxRetries=-1；DialerRetries=1，即每次拨号路径只尝试一次 | 客户端初始化 |
| Redis 启动检查 | Ping 预算 100ms；网络/认证/命令故障记录后降级启动，不阻止 HTTP；MySQL 仍为必要依赖 | 启动 |
| 普通商品回源预算 | 1 秒内，且不超过调用方剩余预算 | 事务外读取 |
| singleflight 共享工作总预算 | 1 秒，包含二次查缓存、SQL、回填尝试 | 请求合并 |
| 商品回源并发名额 | 2，满时立即拒绝；现有 MySQL 池仍为 5 | 回源保护 |
| 熔断阈值与范围 | 同一 ProductCache 的 GET/SET/DEL 共用；连续 5 次符合条件的故障，通信成功清零 | 熔断 |
| 熔断冷却 | 10 秒 | 熔断 |
| Half-Open | 冷却后只放行一次 GET；成功含 miss 则恢复，失败重新 Open | 熔断 |

预算嵌套：每次 Redis 操作使用父 context 的子预算；共享任务只有一份总预算，各阶段不会重新获得完整 1 秒；SQL 前检查剩余时间。回源成功而回填超时仍保留商品结果；等待者自身已经取消时按自己的 context 返回。客户端整体 5 秒请求预算继续有效。

以上客户端字段已对照 [v9.23.0 选项](https://github.com/redis/go-redis/blob/v9.23.0/options.go) 和 [连接池拨号实现](https://github.com/redis/go-redis/blob/v9.23.0/internal/pool/pool.go) 核对：MaxRetries=0 会采用默认重试；DialerRetries 的 1 按该版本循环表示一次尝试。客户端自身可能有连接恢复活动，不能将业务熔断描述为“整个进程绝无 Redis 网络活动”；熔断保证的是业务命令跳过。实际预算通过故障实验验证，不承诺精确到毫秒的墙钟耗时。

### 7.3 已确定的故障策略与推进规则

学习者已在本轮授权导师决定原待确认清单中的技术默认值。本节全部为实施约定，不再保留逐项确认门槛；设计确定不代表实现完成。

| 情况 | 已确定行为及理由 |
| --- | --- |
| 启动 Ping 成功 / 失败 | 配置合法即可创建客户端；Ping 失败保留客户端并记录降级。P5 接入后，成功初始化 Closed，失败直接初始化 Open，从失败时刻起冷却 10 秒，再由请求触发 GET 探测；P0/P2 尚无熔断时按短超时降级 |
| Redis 未命中、故障或缓存损坏 | 进入商品回源；P3 合并、P4 限名额。Redis 故障不等于商品不可售或库存不足；父请求已取消则结束，不再启动回源 |
| 回源没有商品 | 返回 ErrProductNotFound，不写负缓存，按第 3 节在占键之后作业务拒绝；当前先学习正常商品缓存 |
| MySQL 回源失败 / 超时 | 保留 cause 并归为 ErrStorageUnavailable；不把未知结果填入 Redis，不启动订单事务。等待者取消与依赖故障区分 |
| 回源成功，SET 失败 / 被熔断跳过 | 记录缓存未填入，返回已经查到的 Product；不让缓存写失败否定成功的商品查询 |
| MySQL 商品更新成功，DEL 失败 / 被跳过 | 明确数据库已提交、缓存未确认失效；记录错误并等待自然过期，不增加后台重试队列，不声称有严格的更新后 30 秒新鲜上界 |
| 所有共享查询等待者都取消 | 共享任务仍可在 1 秒总预算内完成；不加引用计数取消。读取器关闭会取消并等待任务 |
| 回源名额已满 | 立即 ErrProductLookupBusy，HTTP 503、code=product_lookup_busy，不排队、不启动订单事务 |
| Half-Open 的探测被父 context 取消 | 不判 Redis 成功/失败；归还探测资格，允许后续一个 GET 再探测，不能卡住 probe 标记 |
| TTL 随机偏移、负缓存、删除重试、竞态修复 | 本阶段均不实现；旧值晚回填记录于 TODO.md，后续单独设计 |

接手 Agent 按已确定参数解释原因，再辅导学习者手写当前小步，不为普通配置再次索取决策。仍需检查实际端口占用、工具链、依赖下载和运行结果；这是验证环境，不是把决策重新交给学习者。若实际冲突或验证结果要求改参数，先记录原因并更新本文，再实施。用户明确提出新偏好时同样先更新设计；不能据此一次代写全部 P0–P6。

## 8. 分步开发计划

每个编号可进一步拆成多个教学轮次；不要因为在同一阶段，就一次讲完所有 API。

### P0：检查环境与建立缓存连接

小目标：运行一个可丢弃测试 key 的 SET/GET/TTL/DEL，解释 Redis 是独立服务。

1. 检查实际环境及端口占用，按第 7 节已选版本修改 Compose/example 与依赖，记录安装和启动结果。
2. 实现客户端配置与共享客户端，说明连接池、超时、重试和 Close。
3. 用唯一测试 key 手动验证，读到 false 字符串/JSON 不等于 key 不存在；精确删除测试 key。
4. 不接入下单，不同时写 singleflight/熔断。

### P1：先拆清商品读取与事务边界

小目标：仍然直接读 MySQL，但把商品预读移出订单事务，确认业务边界。

1. 在 service.go 定义 ProductReader，在 store.go 增加 GetProduct 薄适配直接实现接口；不另建第二套商品服务。
2. 修改 ReadProduct 去掉 tx，读完归还池；Service 注入 reader。
3. 预读后保留商品不存在/不可售结果，在占键之后决定业务拒绝；依赖故障先返回。
4. 保留先插订单占键、重复键回滚重查、库存行锁和 CommitUnknown。
5. 定向验证单连接池仍可完成购买、失败回滚、预查询未命中后其他同键订单先提交时优先重放。不重新执行与本改造无关的整套实验。

### P2：基础 cache-aside

小目标：看到第一次 miss 回源、下一次 hit，且库存仍正确。

1. 实现 ProductCache key/JSON 与命中分类；先验证 false 与缺字段。
2. 实现具体结构体 CachedProductReader 的基础 cache-aside，接入 Redis 短超时、回源与失败降级；ProductReader 接口保留。
3. 组装依赖，完善正常启动/关闭；缓存初始化不建业务表。
4. 验证命中、30 秒过期、MySQL 提交后 DEL、陈旧状态和 Redis 停止后受请求预算约束的回源。
5. 停点：能解释缓存作用与边界；此时尚未具备合并、限流和熔断，不报告已经防住雪崩。

### P3：观察击穿，再加请求合并

1. 删除测试热门 key，同批多次调用商品 reader，先观察源查询次数；不靠 HTTP 请求总数猜查询次数。
2. 实现共享 Group/DoChan、二次缓存检查和独立等待取消。
3. 增加共享工作根 context、短总预算与 Close，处理退出。
4. 用可控阻塞数据源制造真实重叠，验证同 key 源读取一次、不同 key 可并行、请求结束后新一批可再次回源；验证一个等待者取消不取消其他人的工作。
5. 所有订单仍独立执行。不要把已有订单请求键当作商品合并 key。

### P4：观察不同 key 集中回源，再加名额限制

1. 解释容量 2、立即拒绝和 503/product_lookup_busy 的取舍，用不同商品 key 制造并发，避免被 P3 自动合并。
2. 只在源查询周围申请/释放名额，满时明确 503；错误、超时、取消均不泄漏名额。
3. 验证最大活跃源查询数不超过限额，缓存命中与合并等待者不占名额，拒绝路径没有订单事务。
4. 不宣称限制了整个服务的 MySQL 请求量。

### P5：观察 Redis 持续失败，再加熔断

1. 先用不可达端点/可控依赖观察重复等待，不启动全量并发压测。
2. 按第 5.5/7 节实现状态机，注入可控时钟测试冷却，不让测试每次真的等待 10 秒。
3. 验证故障阈值、Open 跳过、单探测、失败重新冷却、成功恢复；miss/false 不能熔断。
4. 真实停止再启动 Redis，记录状态变化和源查询，确认进入受控降级及恢复，不使用清空数据库的方式复位。

### P6：一致性与组合回归

观察旧值晚回填窗口，以及缓存命中/故障时原库存和幂等规则仍成立；填写第 9 节。本阶段不修复 TODO.md 中的竞态，不加入 TTL 随机偏移。性能只记录学习观察，不比较生产容量。

## 9. 验收实验

复用 003 的唯一 run_id、独立测试商品、请求键清单、新连接核对和精确清理约定。不能 FLUSHDB、清空开发库、终止非本实验连接。不存在或不可售的回滚仍可能消耗自增 ID，不用 ID 间隔计算订单数。

商品 reader 层的可控源测试用于证明合并/取消/限流；真实 HTTP+MySQL 用于证明订单库存正确。两类证据分别记录，不能把 fake 数据源成功当成数据库并发验收。

| 编号 | 数据/操作 | 验证手段与预期 | 当前结果 |
| --- | --- | --- | --- |
| R00 客户端 | 唯一临时 key，SET+30 秒 TTL，GET，DEL | 真实 Redis 值正确、TTL 有限、删除后未命中；未修改业务数据 | 未执行 |
| R01 事务边界 | 测试商品库存 2，新键买 1；定向将测试池限制为 1 | 真实服务/集成实验能完成，订单 1、库存 1；测试设置结束恢复，不出现持连接再借连接等待 | 未执行 |
| R02 正常缓存 | 可售商品库存 3，先 DEL key；两个不同键各买 1 | 首次商品回源，第二次命中；可控源计数或准确的商品查询日志佐证，不把 orders/inventory SQL 算入；订单 2、库存 1 | 未执行 |
| R03 过期 | 单独调用 reader 填 key，记录 PTTL；读取后再看 PTTL，等过期后再读 | 命中不续期；到期后回源并恢复有限 TTL；不用购买副作用计时 | 未执行 |
| R04 false/坏值 | 商品实际 active=true；依次设置合法 false、缺字段、错误 id、坏 JSON，各用新键请求 | 合法 false 拒绝且无订单；坏缓存回源而非误判 false；坏值成功回填；相关订单/库存按成功次数核对 | 未执行 |
| R05 更新失效 | 填入可售缓存，SQL 下架并提交，DEL | 确认 key 不存在；新购买回源后拒绝、无新订单、库存不变；再上架/提交/DEL 后可购买 | 未执行 |
| R06 接受陈旧 | 可售商品库存 3，填 true 后 SQL 下架但故意不 DEL，立即新键购买 | 缓存仍有效时可买一件；到期后新键被拒；订单 1、库存 2。该实验验证业务选择，不标为缓存 bug | 未执行 |
| R07 重放优先 | 缓存读取准备期间，用可控闸门让另一个相同请求先提交；随后令准备结果不可售或不存在 | 迟到请求在占键竞争后返回原订单；同键不同参数返回冲突；无第二笔订单或重复扣减 | 未执行 |
| R08 热点合并 | 同商品 key、20 次真正重叠的 reader 调用；源查询用闸门暂停 | 活跃的一批只执行一次源查询，共享 Product；缓存健康时可仍有多次最初 GET；下一批结果由缓存决定 | 未执行 |
| R09 等待取消 | 同 key 至少两个等待者，一个提前取消，另一个正常等待；另测全部离开 | 第一人及时结束，第二人仍得结果；共享工作有总预算，全部离开也不无限运行；Close 能取消并等待收尾 | 未执行 |
| R10 回源限额 | 超过名额数量的不同商品 key，用可控源阻塞 | 活跃 SQL 不超过限额，超额明确拒绝；失败/取消释放名额；缓存命中不受名额占用影响 | 未执行 |
| R11 熔断状态 | 注入 Redis 故障和时钟，之后恢复；并发发起冷却后的请求 | 精确观察阈值、跳过、仅一个探测、成功恢复/失败重开；旧调用不能覆盖新状态；miss/false 不计故障 | 未执行 |
| R12 真实 Redis 停止 | 先准备商品及库存，停止本实验 Redis 服务，发出少量不同请求；再启动 | 短超时、有界回源；是否成功/拒绝分开统计；成功数对应订单与扣减；恢复后重新填缓存。不制造生产容量结论 | 未执行 |
| R13 回填/失效失败 | 源查询成功后让 SET 失败；另一次在商品更新提交后让 DEL 失败 | 按 7.3：SET 失败保留商品结果；DEL 失败记录并依靠 TTL，SQL 成功不伪报回滚，坏/旧缓存不续期，日志无凭据；记录具体故障注入办法 | 未执行 |
| R14 旧值晚回填 | A miss 读到旧值后暂停回填；B 更新提交+DEL；继续 A，然后再读 | 用真实查询或可控边界记录顺序，可读到旧值直到过期/失效；承认窗口存在，不误标强一致 | 未执行 |
| R15 购买回归 | 热缓存下：库存 1 两个不同键；再用新商品库存 1 同键 20 次 | 前者一单库存 0，另一请求不足；后者同一订单，一次新建其余重放。若传输超时，保留键核对，不算库存不足 | 未执行 |
| R16 退出与 race | Redis 正常/故障、共享查询执行中，进行受控退出；运行覆盖到新路径的 go test -race | 先 HTTP、再 reader/共享工作、Redis、MySQL；无名额泄漏或无限 goroutine；race 通过不等于 SQL 已验收 | 未执行 |

实际故障注入与 .learn/R04 文件要在执行前写清，优先用测试依赖、测试专用闸门或精确断点，不为实验增加生产 HTTP 参数。需要 Redis 权限时检查实际访问范围；故障后先保存证据，再清理本轮商品、库存、订单和精确缓存 key。

### 学习观察与日志

推荐最小分类：cache_hit、cache_miss、cache_error、source_read、source_rejected、breaker_transition；关联 product_id、request_id（适用时）和安全阶段，不打印完整商品请求或配置。

请求数、Redis 命令数、商品源查询数和创建订单数分别统计。要证明合并先让请求真实重叠；SQL 很快时同批发起 HTTP 不保证命中并发分支。只有需要解释差异才补采集点，不引入监控平台。

## 10. 给接手辅导 Agent 的指引

1. 先读 AGENTS.md、本文、当前源码与 Git diff。用户报告已完成旧实验，尊重最新进度；历史完成表没有同步不代表必须重做整阶段。
2. 区分已确定的业务/技术设计与实际结果；第 7 节普通配置已由学习者授权导师决定，不再将它们列为待确认问题。先解释当前小步的取舍，再辅导手写；遇到文档未覆盖的新决定先补设计。
3. 第一小步从 P0 环境检查/一个临时 key 开始；若已完成且有证据就跳过。用户希望先学事务拆分时可从 P1 开始，不直接补完整缓存层。
4. 每轮先解释一个主要概念，给职责/签名/伪代码，让学习者预测并手写；review 具体路径和错误结果。只有学习者明确要求才局部代写。
5. 不把请求 goroutine 与 handler 实例混淆。Handler、Reader、Group、客户端共享；参数、context、订单事务各请求独立。
6. 第 3 节的两步边界必须保留：事务外准备商品，订单占键成功后再做商品业务拒绝。没有明确的新设计，不将商品不可售提前返回，也不让商品回源重新接收订单 tx。
7. 新机制先看问题再加入；不得在 P0/P2 自动实现全部熔断、限流、负缓存、随机 TTL 或通知 Worker。
8. 验收按第 9 节记录真实证据，失败和未执行不能改写为通过。结束留下已完成函数、理解点、实际验证、遗留和下一入口。

## 11. 完成记录

- [x] 2026-10-15：新增本设计；核对当前 Service、Store、HTTP 组装、MySQL 池和 Compose。确认 Redis 尚未接入，本轮不改实现和依赖、不运行数据库/缓存测试。
- [x] 2026-10-15：按学习者要求，将接口/结构体、方法边界、持有关系与对应文件统一整理到第 6 节；依据本轮授权确定第 7 节版本、超时、连接池、限流、熔断和故障策略，删除逐项确认门槛。仅修改设计文档，未安装依赖、修改应用或运行缓存实验。
- [ ] 第 7 节配置在 P0–P5 对应实现中落盘并通过运行验证。
- [ ] P0 Redis 环境和客户端。
- [ ] P1 商品预读与事务边界。
- [ ] P2 基础 cache-aside。
- [ ] P3 请求合并与取消。
- [ ] P4 回源名额。
- [ ] P5 熔断。
- [ ] P6 组合回归与实验记录。

每次更新记录：日期、用户授权范围、文件/函数、已理解内容、实际命令与结果、测试数据清理、未完成项和下次入口。本文中的函数签名、目录和数值是设计，不代表当前仓库已有对应实现。

