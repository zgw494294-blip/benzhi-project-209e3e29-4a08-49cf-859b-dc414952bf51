# 衡温冷链偏差调查与放行服务

衡温冷链是一套离线运行的冷链质量合规服务。它把温控规则发布、批次读数接收、偏差识别、调查证据、纠正预防措施和最终放行决定串成一条可追溯业务链路。运行时仅依赖 Go 标准库，数据保存为本地 JSON 快照和追加日志。

## 启动与自检

项目需要 Go 1.22 或更高版本。默认只监听高位回环地址 `127.0.0.1:19081`：

```bash
go run ./cmd/thermoguard -data-dir ./var/data
```

显式地址优先于 `PORT`；未提供 `-addr` 时，`PORT=19123` 会解析为 `127.0.0.1:19123`。

```bash
PORT=19123 go run ./cmd/thermoguard -data-dir ./var/data
go run ./cmd/thermoguard -addr 127.0.0.1:19234 -data-dir ./var/data
```

地址解析会拒绝低位端口、非数字端口、越界端口以及 3000、8080。自检经过相同的地址解析、仓储、规则计算、审计链和后台任务构造路径：

```bash
go run ./cmd/thermoguard -addr 127.0.0.1:19081 -data-dir ./var/self-check -self-check
```

## 业务接口

所有接口位于 `/api/v1`。写请求必须提供非空 `X-Actor`，单条读数也可使用 `Idempotency-Key` 请求头。错误响应包含稳定的 `code`、`message`、`details` 和 `request_id`。

典型处理顺序如下：

1. `POST /policies` 创建草稿，再调用 `POST /policies/{id}/publish` 发布并冻结规则。
2. `POST /lots` 登记批次并绑定已发布规则。
3. 使用 `POST /lots/{id}/readings` 或 `POST /lots/{id}/readings:batch` 接收读数。
4. `POST /lots/{id}/monitoring:close` 结束监测并完成最终重算。
5. 查询 `/lots/{id}/excursions`，为活动偏差创建调查、证据和措施，再提交调查。
6. 查询 `/lots/{id}/release-preview` 的阻断项，完成措施后使用当前版本提交决定。
7. 从 `/lots/{id}/case-export` 导出完整案件包，从 `/lots/{id}/audit` 校验审计链。

创建规则示例：

```bash
curl -X POST http://127.0.0.1:19081/api/v1/policies \
  -H 'Content-Type: application/json' \
  -H 'X-Actor: quality-user' \
  -d '{"name":"2-8 摄氏度规则","min_c":2,"max_c":8,"max_gap_minutes":30,"continuous_minutes":10,"cumulative_minutes":20,"major_delta_c":2,"critical_delta_c":5}'
```

JSON 解码严格拒绝未知字段、多余 JSON 值和超过 1 MiB 的请求体。列表采用稳定排序，批次和审计接口支持游标分页。

## 离线批量导入

导入文件为一行一个对象的 NDJSON。每行必须包含设备、UTC 采样时间、温度、来源和幂等键：

```json
{"device_id":"probe-A","sampled_at":"2026-08-21T03:00:00Z","celsius":5.2,"source":"logger","idempotency_key":"reading-001"}
```

先执行整批预检，不写入数据：

```bash
go run ./cmd/thermoguard-import -data-dir ./var/data -lot lot-00000002 -file ./readings.ndjson -actor quality-user -dry-run
```

去掉 `-dry-run` 后执行原子导入。任一行格式、时间窗、温度或幂等校验失败时，整批均不落盘。命令输出中文摘要和机器可读 JSON。

## 数据与恢复

数据目录包含：

- `state.json`：带 `schema_version` 的当前完整快照。
- `events.ndjson`：每个成功事务一行的追加日志，记录修订号、状态校验和及事务后状态。

服务启动时先读取快照，再重放修订号更高的完整日志记录。文件末尾的不完整行会被忽略，并通过健康接口报告降级；中间损坏、修订号跳跃、状态校验失败或审计哈希断链会阻止启动。不要在服务运行时手工编辑这两个文件。

仓储支持生成带状态校验和与审计头的备份、检查日志连续性，以及在阈值达到后压缩已被快照覆盖的日志。恢复前应保留原数据目录，校验备份格式、状态校验和和审计链后再进行运维替换。

收到 `SIGINT` 或 `SIGTERM` 后，服务先停止接单，再等待后台评估和逾期扫描任务退出。快照使用同目录临时文件、同步写入和原子重命名，避免读到半份 JSON。

## 验证

```bash
go test ./... -count=1
go vet ./...
go build ./...
go test -race ./internal/store ./internal/jobs ./internal/acceptance
```

验收测试会在动态高位回环端口启动真实 HTTP 服务；进程冒烟测试还会先编译服务二进制，再通过 `-addr` 启动并完成健康检查、规则发布、批次登记、读数接收和放行预览。
