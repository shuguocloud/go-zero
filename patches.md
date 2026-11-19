# go-zero Fork 补丁清单

本仓库是 `github.com/shuguocloud/go-zero`，在上游基础上维护少量针对 etcd 服务发现的私有补丁。
本文件记录每个补丁的动机、改动位置与同步上游时的注意事项，便于后续 rebase 时逐条核对。

## 一、版本对应关系

| Tag | Commit | 日期 | 摘要 |
| --- | --- | --- | --- |
| `v1.9.0` | `865555c` | 2026-10-27 | 既有补丁：ResourceManager 关闭后写入导致的 panic |
| `v1.9.1` | `0e2290d` | 2026-11-12 | watch 重连增加退避 |
| `v1.9.2` | `8cd3708` | 2026-11-12 | 单端点不再启用 AutoSyncInterval |
| `v1.9.3` | `24ac732` | 2026-11-12 | load() 鉴权失败时重建连接 |
| `v1.9.4` | `1598e75` | 2026-11-12 | 启动期认证探测 |
| `v1.9.5` | `4d34a84` | 2026-11-12 | 仅文档：新增上游核对记录（代码与 `v1.9.4` 相同） |
| `v1.9.6` | — | 2026-11-15 | etcd simple token 到期前预刷新连接，避免 watch 断连风暴 |
| `v1.9.7` | — | 2026-11-15 | watch 加 WithProgressNotify + revision 断点续传，降低鉴权重连恢复开销 |
| `v1.9.8` | — | 2026-11-19 | 鉴权失效后重建连接并加冷却，预刷新叠加抖动，消除过期 token 空转 |

业务项目侧的依赖版本需与本文件的 tag 对应，当前为 `v1.9.8`（含 P1-P7）。
`v1.9.8` 为本次新增补丁，业务侧 `go.mod` 需更新到对应 tag 并刷新 vendor。
本文档可能晚于表中最后一个 tag：后续纯文档修订只在 `main` 上提交，不再单独打 tag。

## 二、补丁明细

### P1 `v1.9.1` — watch 重连增加退避

**位置**：`core/discov/internal/registry.go`，`(*cluster).watch()`

**现象**：etcd 短暂不可用或 token 持续失效时，日志在极短时间内被大量重复错误记录占满，CPU 占用升高。

**根因**：`watchStream` 返回错误后直接进入下一轮循环，没有任何等待；而同文件 `load()` 的重试路径带有 `coolDownUnstable.AroundDuration(coolDownInterval)` 退避。

**改动**：在 `watch()` 重试循环中加入与 `load()` 相同的退避：

```go
// log the error and retry
logc.Error(cli.Ctx(), err)
time.Sleep(coolDownUnstable.AroundDuration(coolDownInterval))
```

**行为变化**：watch 断线后重连延迟约 1 秒（上下浮动 5%）。对服务发现无实质影响。

---

### P2 `v1.9.2` — 单端点不再启用 AutoSyncInterval

**位置**：`core/discov/internal/registry.go`，`DialClient()`

**现象**：单机部署下，服务启动初期一切正常，约 1 分钟后开始持续报
`dial tcp <advertise地址>:2379: connect: connection refused`，且修改项目配置里的 `Etcd.Hosts` 无效。

**根因**：clientv3 的 `AutoSyncInterval` 会周期性拉取集群成员，并用成员通告的
`advertise-client-urls` **整体替换**本地 endpoints（`SetEndpoints` 是替换语义）。
当 etcd 通告的地址不可达（机器换过 IP、advertise 配错、容器重建），客户端会丢掉唯一可用地址。

**改动**：仅在多端点时启用同步：

```go
cfg := clientv3.Config{
    Endpoints:           endpoints,
    DialTimeout:         DialTimeout,
    RejectOldCluster:    true,
    PermitWithoutStream: true,
}
if len(endpoints) > 1 {
    cfg.AutoSyncInterval = autoSyncInterval
}
```

**影响面**：单端点（本项目所有服务）不再被通告地址覆盖；多端点集群行为不变。

**注意**：这是本项目场景下的有意取舍。若将来单端点也需要感知成员变化，应改为
"同步失败时回退到配置地址"，而不是简单恢复无条件同步。

---

### P3 `v1.9.3` — load() 鉴权失败时重建连接

**位置**：`core/discov/internal/registry.go`：`cluster` 结构体、`getClient()`、`load()`，新增 `reauth()` 与 `isAuthError()`

**现象**：token 失效后（simple token 到期、etcd 重启、密码变更），服务发现永久为空，
日志持续输出 `etcdserver: invalid auth token` 或 `user name is empty`，且不会自行恢复。

**根因**：一元调用路径不会刷新 token。`retry_interceptor.go` 中 stream 拦截器在建流前调用
`getToken()`（重新 Authenticate），而 unary 拦截器在 `max == 0` 短路前没有该调用。
而 `monitor()` 的执行顺序是 `load()`（Get，一元）→ `watch()`（stream），
因此 token 失效时 `load()` 永远出不去，也就永远走不到会触发重登的建流步骤。

**改动**：

1. 新增 `isAuthError(err)`：`status.Code(err) == codes.Unauthenticated`、
   `rpctypes.ErrUserEmpty` / `ErrAuthOldRevision`，以及错误文本包含
   `invalid auth token` / `user name is empty` 作为兜底。
2. 新增 `(*cluster).reauth()`：每个 cluster 至多重建一条连接并复用，`authLock` 保护。
3. `load()` 命中鉴权错误时替换本地 `cli` 并 continue，让新连接重新认证。
4. `getClient()` 优先返回 `authClient`（若已重建）。

**为什么不替换原共享连接**：`connManager` 中的连接被同一 cluster 的所有 key 共用，
关闭它会波及已建立的 watch，导致它们带着已关闭的连接空转。因此 `reauth()` 另建一条连接，
原连接保持不动（其 watch 在重建流时会自行重新认证）。

**已知取舍**：`authClient` 在进程生命周期内不会被关闭，鉴权失效一次后该 cluster 会多持有一条连接。
若将来失效频繁触发，需要补充空闲回收。

---

### P4 `v1.9.4` — 启动期认证探测

**位置**：`core/discov/internal/registry.go`，`DialClient()`

**现象**：凭据配置错误时进程正常启动，无任何显式报错，但服务发现始终为空。

**根因**：认证失败只发生在后续 `Get`/`Watch` 中，而 `load()` 的错误处理是无限重试 + 日志，
不阻断启动，很容易被忽略。

**改动**：建连后若配置了账号密码，立即执行一次 `cli.Auth.Authenticate`：

```go
if cfg.Username != "" && cfg.Password != "" {
    probeCtx, cancel := context.WithTimeout(context.Background(), DialTimeout)
    _, authErr := cli.Auth.Authenticate(probeCtx, cfg.Username, cfg.Password)
    cancel()
    if authErr != nil && !strings.Contains(authErr.Error(), "authentication is not enabled") {
        logc.Errorf(context.Background(), "etcd 认证失败，endpoints=%v，user=%s：%v",
            endpoints, cfg.Username, authErr)
    }
}
```

**注意**：这里用 `Authenticate` 而非探测性 `Get`。一元 `Get` 在 token 尚未装载时
会返回 `user name is empty`，会造成每次启动都误报认证失败。

**行为变化**：只记录日志，不阻断启动（保持原有失败语义）；etcd 未开启认证时自动跳过。

---

### P5 `v1.9.6` — simple token 到期前预刷新连接

**位置**：`core/discov/internal/vars.go`（新增常量）、`core/discov/internal/registry.go`
（`cluster` 结构体新增 `refreshStop`、`newCluster` 启动刷新、新增 `startTokenRefresher` /
`runTokenRefresher` / `refreshToken`）

**现象**：etcd 配置 `auth-token: simple` + `auth-token-ttl: 86400` 时，simple token 在服务端内存中存储，
到期后服务端直接删除，所有 watch 流同时鉴权失败断开。大量服务实例同时重建 watch，
瞬间产生大量 Range+Watch 请求打爆 etcd，CPU 持续高位。

**根因**：P3 的 `reauth()` 只在 token 已失效后被动重建连接（触发条件是 `load()` 收到
`Unauthenticated`）。simple token 到期是定时事件，所有实例同一时刻失效，被动重建无法
避免断连风暴。

**改动**：

1. `vars.go` 新增 `tokenTTL = 24h`（与服务端 simple 的 `auth-token-ttl: 86400`
   或 JWT 的 `ttl=24h` 对齐）和 `tokenRefreshMargin = 10min`（到期前 10 分钟预刷新）。
2. `cluster` 结构体新增 `refreshStop chan struct{}`，用于停止刷新 goroutine。
3. `newCluster()` 在配置了鉴权账号时启动 `startTokenRefresher()`。
4. `runTokenRefresher()`：每 `tokenTTL - tokenRefreshMargin`（23h50m）触发一次
   `refreshToken()`，到期前完成连接重建。
5. `refreshToken()`：新建带新 token 的连接 → 替换 `authClient` → `reload()` 把所有
   watch 迁移到新连接 → 关闭旧连接。

**与 P3 的关系**：P3 是被动兜底（token 已失效后重登），P5 是主动预防（到期前换连接）。
两者并存：正常情况下 P5 到期前换连接，watch 不会断；万一 P5 刷新失败或 TTL 配置不一致
导致 token 提前过期，P3 仍能在失效后重建连接恢复服务发现。

**注意**：`tokenTTL` 硬编码为 24h，必须与服务端 `etcd.yaml` 的
`auth-token: simple` + `auth-token-ttl: 86400` 保持一致。若服务端改了 TTL，这里需同步修改，否则
预刷新节奏会偏离实际到期时间。

---

### P6 `v1.9.7` — watch 加 WithProgressNotify + revision 断点续传

**位置**：`core/discov/internal/registry.go`，`setupWatch()`、`watchStream()`、`watch()`

**现象**：watch 断线重连时总是从 `load()` 返回的原始 revision 开始重新监听，
导致重连后重复处理大量已知事件，增加客户端和服务端开销。空闲期间没有普通事件时，
也无法通过进度通知推进已处理位置。

**根因**：
1. `setupWatch()` 未请求服务端发送 progress notify。
2. `watchStream()` 只处理事件但不更新 revision，`watch()` 重连时使用的一直是
   `load()` 返回的初始 revision，导致断线后从很旧的位置重放。

**改动**：

1. `setupWatch()` 增加 `clientv3.WithProgressNotify()`，请求服务端在无事件时
   定期发送进度通知。
2. `watchStream()` 返回值改为 `(int64, error)`，返回最后已处理的 revision：
   - 收到 progress notify 响应时更新 revision（无事件，仅推进位置）
   - 处理完普通事件后更新 revision 为 `wresp.Header.Revision`
3. `watch()` 使用 `watchStream()` 返回的 revision 进行下次重连，并从
   `revision + 1` 继续，避免重复处理已接收事件。
4. 新增回归用例，覆盖 progress notify、普通事件、断线后按最新 revision 重建 watch。

**与 JWT 的关系**：本补丁不解析 simple/JWT token，clientv3 对两种 token 透明处理。
JWT 兼容性由 P3-P5 的鉴权失败重建、启动认证探测和到期前预刷新共同保障；
P6 负责降低任意鉴权模式下 watch 断连后的恢复开销，并不是 JWT 的开关。

**兼容性**：普通事件推进 revision 不依赖 progress notify；即使服务端未发送进度通知，
断点续传仍然有效。进度通知周期由服务端版本与配置决定，客户端不硬编码该周期。

### P7 `v1.9.8` — 鉴权失效后重建连接并加冷却，预刷新叠加抖动

**位置**：`core/discov/internal/registry.go`（`load()`、`reauth()`、`watch()`）、
`core/discov/publisher.go`（`doRegister()`）、`core/discov/internal/vars.go`（既有 `tokenTTL`/`tokenRefreshMargin`）

**现象**：etcd 服务端日志在同一毫秒内成片出现
`auth/jwt.go failed to parse a JWT token: Token is expired` 与
`auth/store.go invalid auth token`，etcd 进程 CPU 冲高；同时客户端 resolver 拿不到地址，
上层出现 `produced zero addresses`。

**根因**：
1. `load()` 在鉴权失败且 `reauth()` 返回缓存连接时无条件 `continue`，无任何退避，
   形成无间隔空转。
2. `reauth()` 只要 `authClient` 非空就永久复用，一旦这条连接同样不可用便无法恢复。
3. `watch()` 与 publisher 续租都在原连接上无限重试已过期的 token。
4. 预刷新周期固定，多实例同时启动会在同一时刻集中重建连接（惊群）。

**改动**：
1. `load()`：仅在确实换到新连接（`fresh != cli`）时立即重试，否则走 `coolDownInterval` 退避。
2. `reauth(stale)`：增加并发复用判断（其他协程已重建则直接复用）与
   `authRenewCooldown = 10s` 重建冷却，凭据确实错误时不至于每秒重建连接。
3. `watch()`：识别鉴权错误后换新连接、重新 `load()` 对齐 revision，再恢复 watch。
4. `publisher.doRegister()`：鉴权错误时经 `Registry.Reauth()` 重建连接并重试一次。
5. 预刷新周期叠加 `±tokenRefreshMargin/2` 随机抖动（`refreshIntervalWithJitter`），
   错开多实例刷新时刻，幅度小于提前量，保证仍在到期前完成刷新（P5 增强）。
6. `isAuthError()` 补 `rpctypes.ErrInvalidAuthToken` 判断。

**兼容性**：期望凭据错误时表现为每秒至多一次退避重试，而非空转；凭据恢复后由
`reauth` 冷却到期自动重建，无需重启进程。

**验证**：`go build ./core/discov/...`、`go vet ./core/discov/...` 通过。

## 三、同步上游时的 rebase 指南

1. 以上游新版本为基线，逐条 cherry-pick `v1.9.1` 至 `v1.9.8`，冲突集中在
   `core/discov/internal/registry.go`，必要时手工重放。
2. 每条补丁重放后核对以下上游实现是否已自带同类修复：
   - `watch()` 是否已有退避
   - `clientv3.Config` 是否已按端点数决定 `AutoSyncInterval`
   - unary 拦截器是否已加入 `getToken`（若上游已补齐，P3 可整条丢弃）
   - 最近一次核对结果见第五节，可直接对照
3. 重放完成后打新 tag（沿用递增规则，如 `v1.10.1`），并同步更新本文件的版本对应表。

## 四、回归验证清单

改动涉及服务发现，每次重放后至少验证：

```bash
go build ./core/discov/...
go vet ./core/discov/internal
gofmt -l core/discov/internal
```

业务侧联调验证：

1. 正常启动：日志无 `etcd 认证失败`，`Etcd` 中注册的实例可被解析。
2. 停止 etcd：日志出现退避重连（约 1 秒一次，而非无间隔刷屏），地址列表不清空。
3. 重启 etcd / 重置 root 密码：出现 `etcd 鉴权失效，已重建连接`，服务发现自动恢复。
4. 故意配置错误密码：启动即出现 `etcd 认证失败，endpoints=… user=…`。

## 五、上游核对记录

### 2026-11-12（go-zero `v1.10.3` / etcd client `v3.7.3`）

| 补丁 | go-zero v1.10.3 | etcd client v3.7.3 | 结论 |
| --- | --- | --- | --- |
| P1 watch 退避 | 已包含 | — | 可丢弃 |
| P2 单端点 AutoSync | 未修复 | — | 保留 |
| P3 load 鉴权重连 | 未修复 | 未修复 | 保留 |
| P4 启动期认证探测 | 未修复 | — | 保留 |

**P1 已被上游吸收。** 上游 `watch()` 已带同样的退避：

```go
// log the error and retry with cooldown to prevent CPU/disk exhaustion
logc.Error(cli.Ctx(), err)
time.Sleep(coolDownUnstable.AroundDuration(coolDownInterval))
```

位置：`core/discov/internal/registry.go`（v1.10.3 第 399-401 行）。下次 rebase 时直接丢弃 P1，无需解冲突。

**P3 根因在 etcd 客户端，上游未修。** etcd v3.7.3 的一元拦截器在短路前没有 `getToken`
（`client/v3/retry_interceptor.go` 第 41-52 行）：

```go
callOpts := reuseOrNewWithCallOptions(intOpts, retryOpts)
// short circuit for simplicity, and avoiding allocations.
if callOpts.max == 0 {
    return invoker(ctx, method, req, reply, cc, grpcOpts...)
}
```

同文件的 stream 拦截器（第 116-118 行）已补上，但只覆盖建流场景：

```go
// getToken automatically. Otherwise, auth token may be invalid after watch reconnection because the token has expired
err := c.getToken(ctx)
```

`go-zero v1.10.3` 的 `discov` 中认证相关代码仅为凭据注入（第 479-481 行），
没有任何鉴权错误识别或连接重建逻辑。

**P2、P4 上游未修复**，继续维护。

**复查触发条件**：etcd 发布说明出现 unary 拦截器鉴权相关改动，或 go-zero `discov`
新增鉴权错误处理时，重新核对 P3；若 etcd 已补齐，P3 整条删除，不再重放。
