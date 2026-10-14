# common

Go 项目公共组件库：只放与业务无关、可在多个项目中复用的代码。

```bash
go get github.com/awp0816/common@latest
```

| 包 | 说明 |
|---|---|
| `logger` | 基于 zerolog 的日志：控制台 + 文件(按天分目录 / 按级别分文件 / 按大小切割)、链路ID、调用栈 |
| `database` | gorm 连接：MySQL / PostgreSQL / SQLite，自动建库、连接池，`debug` 时打印 SQL |
| `redis` | Redis 连接：单机 / 哨兵 / 集群，健康检查 |
| `cache` | 缓存：本地内存(`cache/go-cache`)、本地文件(`cache/file`)、Redis(`cache/redis`)三选一，由配置 `driver` 决定，接口一致 |
| `amqp` | RabbitMQ 发布 + 订阅：断线重连、发布确认、链路ID随消息传递 |
| `token` | JWT 签发 / 校验 / 滑动续期 + Redis 白名单(可撤销、踢下线)，载荷类型自定义 |
| `snowflake` | 雪花ID，起始时间基点持久化到文件 |
| `breaker` | 熔断器(关闭 / 打开 / 半开) |

各组件的配置示例见 [config.example.yaml](config.example.yaml)。

约定：不放业务代码；每个组件自带配置结构；没有 `init` 副作用；仓库根目录不放代码。
版本用 Git 标签发布(`v0.x.y`)，改动记录在 [CHANGELOG.md](CHANGELOG.md)。

## logger

### 配置

```yaml
logger:
  level: info          # debug | info | warn | error
  path: logs           # 日志目录
  filename: app        # 文件名前缀：app.log，按级别分文件时为 app.info.log、app.error.log ...
  json: false          # 文件为 JSON 格式，否则为文本(控制台始终为带颜色的文本)
  console: true        # 输出到控制台
  disableFile: false   # 不写文件
  splitLevel: true     # 按级别分文件
  daily: true          # 按天分目录 logs/2026-01-01/
  maxSize: 100         # 单文件上限(MB)，超过后切割
  maxAge: 30           # 保留天数，按天分目录时过期目录整体删除
  maxBackups: 0        # 切割后保留的旧文件个数，0 不限
  compress: false      # 压缩切割后的旧文件
  caller: true         # 记录调用位置 目录/文件:行号
  stacktrace: true     # error 及以上附带调用栈
```

`logger.Config` 带有 `json`、`yaml`、`mapstructure` 标签，可直接嵌入项目的配置结构。

### 使用

```go
import "github.com/awp0816/common/logger"

if err := logger.Init(cfg.Logger); err != nil {
    panic(err)
}
defer logger.Close()

logger.Info(ctx).Str("user", "alice").Msg("登录成功")      // 自动带上 ctx 中的 request_id
logger.Error(ctx).Err(err).Int64("order_id", 1001).Msg("下单失败")
logger.Debug(ctx).Msgf("共 %d 条", n)
logger.Ctx(ctx)                                             // *zerolog.Logger，需要 With() 等原生用法时
logger.L                                                    // 全局 zerolog.Logger
```

### 链路ID(request_id)

```go
// gin：在最前面的中间件里设置一次，之后 handler 里直接把 c 当 ctx 传入
r.Use(func(c *gin.Context) {
    id := logger.RequestIDFrom(c.GetHeader("X-Request-Id")) // 请求头是合法 UUID 时沿用，否则生成新的
    c.Set("request_id", id)                                                          // *gin.Context 作为 ctx 时使用
    c.Request = c.Request.WithContext(logger.WithRequestID(c.Request.Context(), id)) // 传给下层的 c.Request.Context() 也带上
    c.Header("X-Request-Id", id)
    c.Next()
})

// 非 HTTP 场景(定时任务、MQ 消费)
ctx := logger.WithRequestID(context.Background(), logger.NewRequestID())
```

链路ID为 UUID(v4)：`logger.NewRequestID()` 生成，如 `3368607b-2c9b-4d03-9d07-3238c5bad607`。

### 输出样式

控制台(带颜色)与文本文件：

```
2026-10-13 17:42:40.949 DBG logdemo/main.go:14 > 查询用户 共 3 条 request_id=3368607b-2c9b-4d03-9d07-3238c5bad607
2026-10-13 17:42:40.955 INF logdemo/main.go:15 > 登录成功 request_id=3368607b-2c9b-4d03-9d07-3238c5bad607 user=alice
2026-10-13 17:42:40.955 WRN logdemo/main.go:16 > 密码错误 ip=1.2.3.4 request_id=3368607b-2c9b-4d03-9d07-3238c5bad607 times=3
2026-10-13 17:42:40.955 ERR logdemo/main.go:22 > 下单失败 error="库存不足" order_id=1001 request_id=3368607b-2c9b-4d03-9d07-3238c5bad607
	main.createOrder
		D:/project/logdemo/main.go:22
	main.main
		D:/project/logdemo/main.go:17
2026-10-13 17:42:40.955 INF logdemo/main.go:18 > 定时任务执行完成
```

JSON 文件(`json: true`)：

```
{"level":"info","request_id":"3368607b-2c9b-4d03-9d07-3238c5bad607","user":"alice","ts":"2026-10-13 17:42:15.269","caller":"logdemo/main.go:17","msg":"登录成功"}
{"level":"error","request_id":"3368607b-2c9b-4d03-9d07-3238c5bad607","error":"库存不足","order_id":1001,"ts":"2026-10-13 17:42:15.269","stacktrace":"main.createOrder\n\tD:/project/logdemo/main.go:24\nmain.main\n\tD:/project/logdemo/main.go:19","caller":"logdemo/main.go:24","msg":"下单失败"}
```

- 字段名：`ts`、`level`、`msg`、`caller`、`request_id`、`error`、`stacktrace`(常量 `logger.FieldXxx`)。
- 调用栈从打印日志的业务代码开始，不含 zerolog 与 Go 运行时内部的帧。
- `Init` 应在启动时调用一次；会修改 zerolog 的全局字段名与时间格式设置。

## database

```go
db, err := database.Open(cfg.Database,
    func(c *gorm.Config) { ... },                               // 可选：修改 gorm 配置(命名策略、Logger 等)
)
defer database.Close(db)
n, err := database.ExecScript(db, initSQL)                       // 事务内执行 SQL 脚本(初始化数据)
```

- 连接池 `maxIdleConns`、`maxOpenConns`、`maxLifetime` 与 `type` 没有默认值，必须配置(为 0 时交给 Go 标准库：不保留空闲连接、连接数不限、连接永不过期)。
- mysql 未指定长度的字符串字段默认为 `varchar(255)`，可用 `stringSize` 修改；已有数据库改用本组件时应与原来的值一致，否则启动迁移时会修改这些列的长度。
- 默认：表名单数、迁移不建外键、单条写操作不开默认事务、`TranslateError`(唯一键冲突返回 `gorm.ErrDuplicatedKey`)。
- SQL 日志：`debug: true` 时用 gorm 自带的日志把 SQL 打印到控制台(含耗时、行数，超过 200ms 标为慢 SQL)，否则不打印；不写入日志文件。SQL 执行失败只返回错误，由业务代码按需记录。

## redis / cache

```go
r, err := redis.New(cfg.Redis)
defer r.Close()
r.StartHealthCheck(5 * time.Second)              // 不可用时 IsConnected() 为 false，缓存操作直接返回 cache.ErrUnavailable

c, err := cache.New(cfg.Cache, r)                // driver 不是 redis 时 r 可传 nil
defer c.Close()                                  // file 关闭文件，memory / redis 无操作
err = cache.SetJSON(ctx, c, "user:1", u, time.Minute)
err = cache.GetJSON(ctx, c, "user:1", &u)        // 未命中返回 cache.ErrMiss
ok, err := c.SetNX(ctx, "lock:order:1", "1", 10*time.Second)
n, err := c.Incr(ctx, "login:fail:alice", 15*time.Minute)
```

- 三种驱动行为一致：ttl 为 0 表示不过期；未命中返回 `cache.ErrMiss`；对类型不符的 key 操作返回 `cache.ErrWrongType`。业务代码只依赖 `cache.Cache` 接口，切换只改配置。

| driver | 数据存在哪 | 服务重启后 | 多实例共享 | 适用 |
|---|---|---|---|---|
| `memory`(默认) | 服务进程内存 | 丢失 | 否 | 单实例、开发测试、丢了可重新加载的数据(字典、配置) |
| `file` | 本地文件(`path`，bbolt) | 仍在 | 否 | 单实例、不想装 Redis 又要重启不丢(如登录令牌白名单) |
| `redis` | Redis | 仍在 | 是 | 多实例部署 |

- `file`：每次写入都落盘，写入比 `memory` 慢(毫秒级)，读取很快；同一文件只能被一个进程打开(打开超时 3 秒报错)，同机多实例需配置不同的 `path`；过期数据后台每分钟清理。
- Redis 特有的列表、发布订阅：直接使用 `cacheredis.NewStore(r)`(`import cacheredis "github.com/awp0816/common/cache/redis"`)的 `ListPush`、`ListPopAll`、`Publish`、`Subscribe`。
- 缓存操作失败只返回错误、不记日志，由业务代码按需记录(未命中一般不用记)。

## amqp

```go
c, err := amqp.New(cfg.Amqp)
defer c.Close()
route := amqp.Route{Exchange: "order", ExchangeKind: amqp.ExchangeDirect, Queue: "order.created", RoutingKey: "created", Durable: true}

err = c.PublishJSON(ctx, route, order, amqp.WithMessageID(orderNo))   // 发布(confirm: true 时等待服务端确认)

go c.Subscribe(ctx, route, func(ctx context.Context, d amqp.Delivery) error { // 订阅，阻塞直到 ctx 取消
    logger.Info(ctx).Msg("收到订单")   // ctx 带有发布方的 request_id
    return nil                          // 返回错误：首次投递重新入队，重复投递丢弃
})
```

- 发布与订阅使用同一个 `Route`；发布时 `Queue` 不为空会同时声明队列并绑定，没有消费者时消息也不会丢。
- 订阅时 `Queue` 为空：声明本实例独占的临时队列，配合 `fanout` 实现每个实例都收到的广播。
- 断线后自动重连(指数退避)，订阅自动恢复，发布通道自动重建；消息逐条处理，handler panic 视为失败。

## token

```go
type Session struct {                                    // 项目自定义的载荷
    Username string `json:"uname"`
    RoleKey  string `json:"role"`
}
m, err := token.New[Session](cfg.Token, c)               // c 为上面的 cache.Cache；传 nil 时为无状态 JWT
tok, claims, err := m.Issue(ctx, userID, Session{...})
claims, renewed, err := m.Verify(ctx, tok)               // renewed 不为空时下发新 token(如响应头)
err = m.Revoke(ctx, claims)                              // 退出登录
err = m.RevokeUser(ctx, userID)                          // 停用、改密时踢下线
```

错误：`token.ErrInvalid`、`token.ErrExpired`、`token.ErrRevoked`，项目中转换为自己的错误码。

## snowflake / breaker

```go
err := snowflake.InitConfig(cfg.Snowflake)   // 启动时一次
id := snowflake.GenInt64()

b := breaker.New(breaker.Settings{Name: "ipgeo", ConsecutiveFailures: 5, Timeout: 30 * time.Second, MaxRequests: 1})
err = b.Execute(func() error { return callThirdParty() })   // 打开状态直接返回 breaker.ErrOpenState
```

## 开发

```bash
gofmt -l .        # 必须无输出
go vet ./...
go test ./... -count=1
```

`amqp` 的收发集成测试需要 RabbitMQ：设置环境变量 `AMQP_URL=amqp://guest:guest@127.0.0.1:5672/` 后运行，否则跳过。

发布：`git tag v0.1.0 && git push origin v0.1.0`，业务项目中 `go get github.com/awp0816/common@v0.1.0`。
