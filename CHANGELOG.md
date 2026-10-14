# Changelog

## v0.2.0

- 新增 `database`：gorm 连接(MySQL / PostgreSQL / SQLite)，自动建库、连接池；`debug` 时打印 SQL 到控制台；`ExecScript`/`SplitSQL` 执行 SQL 脚本
- 新增 `redis`：单机 / 哨兵 / 集群连接，健康检查(`IsConnected`)
- 新增 `cache`：本地内存(`cache/go-cache`)、本地文件(`cache/file`，基于 bbolt，重启不丢)、Redis(`cache/redis`)三选一(配置 `driver`)，统一接口 `cache.Cache`
- 新增 `amqp`：RabbitMQ 发布(发布确认、消息持久化、自动声明队列)+ 订阅(重连后自动恢复、失败重投一次)，链路ID随消息头传递
- 新增 `token`：JWT 签发 / 校验 / 滑动续期 + Redis 白名单，载荷为泛型
- 新增 `snowflake`、`breaker`
- 新增 `config.example.yaml`

## v0.1.2

- 链路ID 改用 `github.com/google/uuid` 生成与解析，去掉自行实现的 UUID 代码；`RequestIDFrom` 也接受 `{...}`、`urn:uuid:`、不带连字符等 UUID 写法，统一转为小写带连字符的格式

## v0.1.1

- `logger.NewRequestID` 改为生成 UUID(v4)(原为 32 位十六进制)
- 新增 `logger.RequestIDFrom`：外部传入的链路ID是合法 UUID 时沿用(转为小写)，否则生成新的
- 修复 `Close` 之后仍有日志写入时会重新打开日志文件(文件句柄不释放)，现在关闭后的写入直接丢弃

## v0.1.0

- `logger`：基于 zerolog 的日志组件
  - 控制台(带颜色) + 文件输出：按天分目录、按级别分文件、lumberjack 按大小切割、过期目录清理；文件为文本或 JSON
  - 打印函数以 ctx 为参数(`Debug/Info/Warn/Error(ctx)`)，自动带上链路ID；`*gin.Context` 可直接作为 ctx
  - error 及以上可附带调用栈(从业务代码开始，文本输出时换行缩进)
