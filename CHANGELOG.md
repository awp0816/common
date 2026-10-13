# Changelog

## v0.1.0

- `logger`：基于 zerolog 的日志组件
  - 控制台(带颜色) + 文件输出：按天分目录、按级别分文件、lumberjack 按大小切割、过期目录清理；文件为文本或 JSON
  - 打印函数以 ctx 为参数(`Debug/Info/Warn/Error(ctx)`)，自动带上链路ID；`*gin.Context` 可直接作为 ctx
  - error 及以上可附带调用栈(从业务代码开始，文本输出时换行缩进)
