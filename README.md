# common

Go 项目公共组件库：只放与业务无关、可在多个项目中复用的代码。

```bash
go get github.com/awp0816/common@latest
```

| 包 | 说明 |
|---|---|
| `logger` | 基于 zerolog 的日志：控制台 + 文件(按天分目录 / 按级别分文件 / 按大小切割)、链路ID、调用栈 |

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
    id := c.GetHeader("X-Request-Id")
    if id == "" {
        id = logger.NewRequestID()
    }
    c.Set("request_id", id)                                                          // *gin.Context 作为 ctx 时使用
    c.Request = c.Request.WithContext(logger.WithRequestID(c.Request.Context(), id)) // 传给下层的 c.Request.Context() 也带上
    c.Header("X-Request-Id", id)
    c.Next()
})

// 非 HTTP 场景(定时任务、MQ 消费)
ctx := logger.WithRequestID(context.Background(), logger.NewRequestID())
```

### 输出样式

控制台(带颜色)与文本文件：

```
2026-10-13 17:42:40.949 DBG logdemo/main.go:14 > 查询用户 共 3 条 request_id=8f3a2c1d9e7b4a60
2026-10-13 17:42:40.955 INF logdemo/main.go:15 > 登录成功 request_id=8f3a2c1d9e7b4a60 user=alice
2026-10-13 17:42:40.955 WRN logdemo/main.go:16 > 密码错误 ip=1.2.3.4 request_id=8f3a2c1d9e7b4a60 times=3
2026-10-13 17:42:40.955 ERR logdemo/main.go:22 > 下单失败 error="库存不足" order_id=1001 request_id=8f3a2c1d9e7b4a60
	main.createOrder
		D:/project/logdemo/main.go:22
	main.main
		D:/project/logdemo/main.go:17
2026-10-13 17:42:40.955 INF logdemo/main.go:18 > 定时任务执行完成
```

JSON 文件(`json: true`)：

```
{"level":"info","request_id":"8f3a2c1d9e7b4a60","user":"alice","ts":"2026-10-13 17:42:15.269","caller":"logdemo/main.go:17","msg":"登录成功"}
{"level":"error","request_id":"8f3a2c1d9e7b4a60","error":"库存不足","order_id":1001,"ts":"2026-10-13 17:42:15.269","stacktrace":"main.createOrder\n\tD:/project/logdemo/main.go:24\nmain.main\n\tD:/project/logdemo/main.go:19","caller":"logdemo/main.go:24","msg":"下单失败"}
```

- 字段名：`ts`、`level`、`msg`、`caller`、`request_id`、`error`、`stacktrace`(常量 `logger.FieldXxx`)。
- 调用栈从打印日志的业务代码开始，不含 zerolog 与 Go 运行时内部的帧。
- `Init` 应在启动时调用一次；会修改 zerolog 的全局字段名与时间格式设置。

## 开发

```bash
gofmt -l .        # 必须无输出
go vet ./...
go test ./... -count=1
```

发布：`git tag v0.1.0 && git push origin v0.1.0`，业务项目中 `go get github.com/awp0816/common@v0.1.0`。
