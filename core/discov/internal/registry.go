package internal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shuguocloud/go-zero/core/lang"
	"github.com/shuguocloud/go-zero/core/logc"
	"github.com/shuguocloud/go-zero/core/mathx"
	"github.com/shuguocloud/go-zero/core/syncx"
	"github.com/shuguocloud/go-zero/core/threading"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	coolDownDeviation = 0.05
	// authRenewCooldown 限制鉴权失效后的连接重建频率。凭据确实错误时，
	// 每个请求都重建连接只会把 etcd 打满，这里强制退避。
	authRenewCooldown = 10 * time.Second
	// authRetryMinBackoff/authRetryMaxBackoff 失败重试的指数退避区间。
	// token 过期未被及时刷新、凭据错误或 etcd 不可达时，固定 1s 重试会让
	// 每个 key 的 load/watch 持续打 etcd（日志刷屏 + CPU 飙升）；
	// 连续失败时按 1s/2s/4s… 递增并封顶，成功一次即复位。
	authRetryMinBackoff = time.Second
	authRetryMaxBackoff = 30 * time.Second
	// reloadCooldown 限制连接状态抖动触发的全量重载频率，避免反复重建全部 watch。
	reloadCooldown = time.Second
	// watchMigrateStagger 全量重载时相邻 watch 的启动间隔。clientv3 每建一条
	// watch 流都会先 Authenticate 一次，多个 key 同时重建会在 etcd 侧形成
	// 鉴权尖峰（key 数越多越明显），这里把启动时刻依次错开摊平。
	watchMigrateStagger = 20 * time.Millisecond
)

var (
	registry = Registry{
		clusters: make(map[string]*cluster),
	}
	connManager      = syncx.NewResourceManager()
	coolDownUnstable = mathx.NewUnstable(coolDownDeviation)
	errClosed        = errors.New("etcd monitor chan has been closed")
)

// A Registry is a registry that manages the etcd client connections.
type Registry struct {
	clusters map[string]*cluster
	lock     sync.RWMutex
}

// GetRegistry returns a global Registry.
func GetRegistry() *Registry {
	return &registry
}

// GetConn returns an etcd client connection associated with given endpoints.
func (r *Registry) GetConn(endpoints []string) (EtcdClient, error) {
	c, _ := r.getOrCreateCluster(endpoints)
	return c.getClient()
}

// Monitor monitors the key on given etcd endpoints, notify with the given UpdateListener.
func (r *Registry) Monitor(endpoints []string, key string, exactMatch bool, l UpdateListener) error {
	wkey := watchKey{
		key:        key,
		exactMatch: exactMatch,
	}

	c, exists := r.getOrCreateCluster(endpoints)
	// if exists, the existing values should be updated to the listener.
	if exists {
		c.lock.Lock()
		watcher, ok := c.watchers[wkey]
		if ok {
			watcher.listeners = append(watcher.listeners, l)
		}
		c.lock.Unlock()

		if ok {
			kvs := c.getCurrent(wkey)
			for _, kv := range kvs {
				l.OnAdd(kv)
			}

			return nil
		}
	}

	return c.monitor(wkey, l)
}

func (r *Registry) Unmonitor(endpoints []string, key string, exactMatch bool, l UpdateListener) {
	c, exists := r.getCluster(endpoints)
	if !exists {
		return
	}

	wkey := watchKey{
		key:        key,
		exactMatch: exactMatch,
	}

	c.lock.Lock()
	defer c.lock.Unlock()

	watcher, ok := c.watchers[wkey]
	if !ok {
		return
	}

	for i, listener := range watcher.listeners {
		if listener == l {
			watcher.listeners = append(watcher.listeners[:i], watcher.listeners[i+1:]...)
			break
		}
	}

	if len(watcher.listeners) == 0 {
		if watcher.cancel != nil {
			watcher.cancel()
		}
		delete(c.watchers, wkey)
	}
}

func (r *Registry) getCluster(endpoints []string) (*cluster, bool) {
	clusterKey := getClusterKey(endpoints)

	r.lock.RLock()
	c, ok := r.clusters[clusterKey]
	r.lock.RUnlock()

	return c, ok
}

func (r *Registry) getOrCreateCluster(endpoints []string) (c *cluster, exists bool) {
	c, exists = r.getCluster(endpoints)
	if !exists {
		clusterKey := getClusterKey(endpoints)

		r.lock.Lock()
		defer r.lock.Unlock()

		// double-check locking
		c, exists = r.clusters[clusterKey]
		if !exists {
			c = newCluster(endpoints)
			r.clusters[clusterKey] = c
		}
	}

	return
}

type (
	watchKey struct {
		key        string
		exactMatch bool
	}

	watchValue struct {
		listeners []UpdateListener
		values    map[string]string
		cancel    context.CancelFunc
	}

	cluster struct {
		endpoints  []string
		key        string
		watchers   map[watchKey]*watchValue
		watchGroup *threading.RoutineGroup
		done       chan lang.PlaceholderType
		lock       sync.RWMutex
		// authClient 保存鉴权失效后重建的连接。一元调用（Get）不会触发 token 重登，
		// 旧连接会一直带着失效 token 重试，因此需要用一条新连接重新认证。
		authLock   sync.Mutex
		authClient EtcdClient
		// authRenewAt 记录最近一次重建鉴权连接的时间，用于限制重建频率。
		authRenewAt time.Time
		// authFailStreak 连续失败次数，用于计算指数退避（原子访问，跨 watch/load goroutine）。
		authFailStreak int64
		// reloadAt 记录最近一次全量重载时间，用于连接状态抖动去抖。
		reloadAt time.Time
		// refreshStop 用于停止 token 预刷新 goroutine。
		refreshStop chan struct{}
	}
)

func newCluster(endpoints []string) *cluster {
	c := &cluster{
		endpoints:   endpoints,
		key:         getClusterKey(endpoints),
		watchers:    make(map[watchKey]*watchValue),
		watchGroup:  threading.NewRoutineGroup(),
		done:        make(chan lang.PlaceholderType),
		refreshStop: make(chan struct{}),
	}
	c.startTokenRefresher()
	return c
}

func (c *cluster) addListener(key watchKey, l UpdateListener) {
	c.lock.Lock()
	defer c.lock.Unlock()

	watcher, ok := c.watchers[key]
	if ok {
		watcher.listeners = append(watcher.listeners, l)
		return
	}

	val := newWatchValue()
	val.listeners = []UpdateListener{l}
	c.watchers[key] = val
}

func (c *cluster) getClient() (EtcdClient, error) {
	c.authLock.Lock()
	authClient := c.authClient
	c.authLock.Unlock()
	if authClient != nil {
		return authClient, nil
	}

	val, err := connManager.GetResource(c.key, func() (io.Closer, error) {
		return c.newClient()
	})
	if err != nil {
		return nil, err
	}

	return val.(EtcdClient), nil
}

// reauth 在检测到鉴权失效时重建一条新连接。
// stale 是检出鉴权失败时正在使用的连接：
//   - 若期间已有其他 goroutine 重建过连接，直接复用，避免并发重复重建；
//   - 若距上次重建不足 authRenewCooldown，返回原连接让调用方退避，
//     避免凭据错误时每秒重建连接、在 etcd 侧形成无效鉴权风暴。
//
// 不替换原有共享连接，避免影响其他 key 上已建立的 watch。
func (c *cluster) reauth(stale EtcdClient) (EtcdClient, error) {
	c.authLock.Lock()
	if c.authClient != nil && c.authClient != stale {
		cli := c.authClient
		c.authLock.Unlock()
		return cli, nil
	}
	if c.authClient != nil && time.Since(c.authRenewAt) < authRenewCooldown {
		cli := c.authClient
		c.authLock.Unlock()
		return cli, nil
	}

	cli, err := c.newClient()
	if err != nil {
		c.authLock.Unlock()
		return nil, err
	}

	c.authClient = cli
	c.authRenewAt = time.Now()
	c.authLock.Unlock()

	// 关闭被替换的连接：其 watchConnState goroutine 随之停止，旧连接上残留的
	// watch/请求也会尽快失败并切到新连接，避免旧连接继续携带过期 token 打 etcd。
	if stale != nil && stale != cli {
		if err := stale.Close(); err != nil {
			logc.Errorf(cli.Ctx(), "关闭旧 etcd 连接失败：%v", err)
		}
	}

	logc.Infof(cli.Ctx(), "etcd 鉴权失效，已重建连接：%v", c.endpoints)

	return cli, nil
}

// nextAuthBackoff 累加连续失败次数并返回指数退避时长，封顶 authRetryMaxBackoff。
func (c *cluster) nextAuthBackoff() time.Duration {
	streak := atomic.AddInt64(&c.authFailStreak, 1)

	backoff := authRetryMinBackoff
	for i := int64(1); i < streak && backoff < authRetryMaxBackoff; i++ {
		backoff *= 2
	}
	if backoff > authRetryMaxBackoff {
		backoff = authRetryMaxBackoff
	}

	return coolDownUnstable.AroundDuration(backoff)
}

// sleepOrDone 在等待期间响应重载信号（c.done 关闭）。返回 false 表示
// 期间发生了重载，调用方应立即退出当前重试循环，避免 reload 持锁 Wait 被
// 长达 authRetryMaxBackoff 的退避阻塞。
func (c *cluster) sleepOrDone(d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-timer.C:
		return true
	case <-c.done:
		return false
	}
}

// resetAuthBackoff 在请求成功后复位退避计数，保证恢复后立即按最小间隔工作。
func (c *cluster) resetAuthBackoff() {
	atomic.StoreInt64(&c.authFailStreak, 0)
}

// Reauth recreates the connection for the given endpoints after an auth error.
func (r *Registry) Reauth(endpoints []string) (EtcdClient, error) {
	c, _ := r.getOrCreateCluster(endpoints)

	stale, err := c.getClient()
	if err != nil {
		return nil, err
	}

	return c.reauth(stale)
}

// IsAuthError reports whether err is an etcd authentication error.
func IsAuthError(err error) bool {
	return isAuthError(err)
}

// startTokenRefresher 在配置了鉴权账号时启动 token 预刷新 goroutine。
// etcd simple token 到期后服务端会直接删除，所有 watch 流同时鉴权失败断开，
// 大量实例同时重建 watch 会瞬间打爆 etcd。这里在到期前主动重建连接并迁移 watch。
func (c *cluster) startTokenRefresher() {
	if _, ok := GetAccount(c.endpoints); !ok {
		return
	}
	go c.runTokenRefresher()
}

func (c *cluster) runTokenRefresher() {
	policy := GetTokenPolicy(c.endpoints)

	// 刷新间隔 = TTL - 提前量，确保在 token 到期前完成重建。
	interval := policy.TTL - policy.Margin
	if interval <= 0 {
		interval = policy.TTL / 2
	}
	// 抖动幅度取提前量的一半：多实例同时启动时，固定周期会让所有实例在同一
	// 时刻刷新，瞬间并发重建连接 + 迁移 watch 会打爆 etcd（惊群）。叠加随机抖动
	// 把各实例刷新时刻错开；幅度小于提前量，保证仍在 token 到期前完成刷新。
	jitterRange := policy.Margin / 2

	timer := time.NewTimer(refreshIntervalWithJitter(interval, jitterRange))
	defer timer.Stop()

	for {
		select {
		case <-timer.C:
			c.refreshToken()
			timer.Reset(refreshIntervalWithJitter(interval, jitterRange))
		case <-c.refreshStop:
			return
		}
	}
}

// refreshIntervalWithJitter 在基准间隔上叠加 [-jitterRange, +jitterRange] 的随机抖动，
// 用于把多个实例的 token 预刷新时刻错开，避免惊群。
func refreshIntervalWithJitter(interval, jitterRange time.Duration) time.Duration {
	if jitterRange <= 0 {
		return interval
	}
	delta := time.Duration(rand.Int63n(int64(2*jitterRange)+1)) - jitterRange
	return interval + delta
}

// refreshToken 主动新建一条带新 token 的连接，并把所有 watch 迁移过去，
// 最后关闭旧连接。这样旧 token 到期时不会引发 watch 断连风暴。
func (c *cluster) refreshToken() {
	cli, err := c.newClient()
	if err != nil {
		logc.Errorf(context.Background(), "etcd token 预刷新：新建客户端失败 %v: %v", c.endpoints, err)
		return
	}

	c.authLock.Lock()
	oldCli := c.authClient
	c.authClient = cli
	c.authRenewAt = time.Now()
	c.authLock.Unlock()

	// 把所有 watch 迁移到新连接
	c.reloadNow(cli)

	// 关闭旧连接，避免连接泄漏
	if oldCli != nil {
		if err := oldCli.Close(); err != nil {
			logc.Errorf(context.Background(), "etcd token 预刷新：关闭旧连接失败 %v", err)
		}
	}

	logc.Infof(context.Background(), "etcd token 预刷新完成，已迁移 watch：%v", c.endpoints)
}

func (c *cluster) getCurrent(key watchKey) []KV {
	c.lock.RLock()
	defer c.lock.RUnlock()

	watcher, ok := c.watchers[key]
	if !ok {
		return nil
	}

	var kvs []KV
	for k, v := range watcher.values {
		kvs = append(kvs, KV{
			Key: k,
			Val: v,
		})
	}

	return kvs
}

func (c *cluster) handleChanges(key watchKey, kvs []KV) {
	c.lock.Lock()
	watcher, ok := c.watchers[key]
	if !ok {
		c.lock.Unlock()
		return
	}

	listeners := append([]UpdateListener(nil), watcher.listeners...)
	// watcher.values cannot be nil
	vals := watcher.values
	newVals := make(map[string]string, len(kvs)+len(vals))
	for _, kv := range kvs {
		newVals[kv.Key] = kv.Val
	}
	add, remove := calculateChanges(vals, newVals)
	watcher.values = newVals
	c.lock.Unlock()

	for _, kv := range add {
		for _, l := range listeners {
			l.OnAdd(kv)
		}
	}
	for _, kv := range remove {
		for _, l := range listeners {
			l.OnDelete(kv)
		}
	}
}

func (c *cluster) handleWatchEvents(ctx context.Context, key watchKey, events []*clientv3.Event) {
	c.lock.RLock()
	watcher, ok := c.watchers[key]
	if !ok {
		c.lock.RUnlock()
		return
	}

	listeners := append([]UpdateListener(nil), watcher.listeners...)
	c.lock.RUnlock()

	for _, ev := range events {
		switch ev.Type {
		case clientv3.EventTypePut:
			c.lock.Lock()
			watcher.values[string(ev.Kv.Key)] = string(ev.Kv.Value)
			c.lock.Unlock()
			for _, l := range listeners {
				l.OnAdd(KV{
					Key: string(ev.Kv.Key),
					Val: string(ev.Kv.Value),
				})
			}
		case clientv3.EventTypeDelete:
			c.lock.Lock()
			delete(watcher.values, string(ev.Kv.Key))
			c.lock.Unlock()
			for _, l := range listeners {
				l.OnDelete(KV{
					Key: string(ev.Kv.Key),
					Val: string(ev.Kv.Value),
				})
			}
		default:
			logc.Errorf(ctx, "Unknown event type: %v", ev.Type)
		}
	}
}

func (c *cluster) load(cli EtcdClient, key watchKey) int64 {
	var resp *clientv3.GetResponse
	for {
		var err error
		ctx, cancel := context.WithTimeout(cli.Ctx(), RequestTimeout)
		if key.exactMatch {
			resp, err = cli.Get(ctx, key.key)
		} else {
			resp, err = cli.Get(ctx, makeKeyPrefix(key.key), clientv3.WithPrefix())
		}

		cancel()
		if err == nil {
			c.resetAuthBackoff()
			break
		}

		// 一元调用不会刷新 token：若携带的 token 已失效（simple token 到期、etcd 重启
		// 导致 token 作废、密码被改），继续用同一连接重试永远不会成功。
		// 这里换一条新连接，clientv3 会在建流/首次认证时重新登录。
		// 只有确实拿到新连接才立即重试；若命中重建冷却（凭据错误等）则走下面的退避，
		// 避免无间隔重试在 etcd 侧形成无效鉴权风暴。
		if isAuthError(err) {
			if fresh, cerr := c.reauth(cli); cerr == nil && fresh != cli {
				cli = fresh
				continue
			}
		}

		// 当前连接可能已被 reauth/预刷新替换并关闭，在废弃连接上重试只会持续失败。
		if cur, cerr := c.getClient(); cerr == nil && cur != cli {
			cli = cur
			continue
		}

		logc.Errorf(cli.Ctx(), "%s, key: %s, exactMatch: %t", err.Error(), key.key, key.exactMatch)
		if !c.sleepOrDone(c.nextAuthBackoff()) {
			return 0
		}
	}

	var kvs []KV
	for _, ev := range resp.Kvs {
		kvs = append(kvs, KV{
			Key: string(ev.Key),
			Val: string(ev.Value),
		})
	}

	c.handleChanges(key, kvs)

	return resp.Header.Revision
}

func (c *cluster) monitor(key watchKey, l UpdateListener) error {
	cli, err := c.getClient()
	if err != nil {
		return err
	}

	c.addListener(key, l)
	rev := c.load(cli, key)
	c.watchGroup.Run(func() {
		c.watch(cli, key, rev)
	})

	return nil
}

func (c *cluster) newClient() (EtcdClient, error) {
	cli, err := NewClient(c.endpoints)
	if err != nil {
		return nil, err
	}

	// 同步取连接再起监听：取不到（如测试 mock、连接尚未建立）就跳过，
	// 避免 goroutine 在调用方返回后才访问客户端。
	if conn := cli.ActiveConnection(); conn != nil {
		go c.watchConnState(cli, conn)
	}

	return cli, nil
}

func (c *cluster) reload(cli EtcdClient) {
	c.reloadWith(cli, false)
}

// reloadNow 强制执行一次全量重载，供 token 预刷新迁移 watch 使用（不能去抖）。
func (c *cluster) reloadNow(cli EtcdClient) {
	c.reloadWith(cli, true)
}

func (c *cluster) reloadWith(cli EtcdClient, force bool) {
	c.lock.Lock()
	// 连接状态抖动（Ready <-> TransientFailure）会在短时间内反复触发 listener，
	// 去抖后只做一次全量重载，避免反复重建全部 watch 形成扫描风暴。
	if !force && time.Since(c.reloadAt) < reloadCooldown {
		c.lock.Unlock()
		return
	}
	c.reloadAt = time.Now()
	// cancel the previous watches
	close(c.done)
	c.watchGroup.Wait()
	var keys []watchKey
	for wk, wval := range c.watchers {
		keys = append(keys, wk)
		if wval.cancel != nil {
			wval.cancel()
		}
	}

	c.done = make(chan lang.PlaceholderType)
	c.watchGroup = threading.NewRoutineGroup()
	c.lock.Unlock()

	// start new watches
	// 按固定间隔依次启动：clientv3 建流前会 Authenticate，同时重建会形成鉴权尖峰。
	for i, key := range keys {
		k := key
		stagger := time.Duration(i) * watchMigrateStagger
		c.watchGroup.Run(func() {
			if stagger > 0 && !c.sleepOrDone(stagger) {
				return
			}
			rev := c.load(cli, k)
			c.watch(cli, k, rev)
		})
	}
}

func (c *cluster) watch(cli EtcdClient, key watchKey, rev int64) {
	for {
		var err error
		rev, err = c.watchStream(cli, key, rev)
		if err == nil {
			c.resetAuthBackoff()
			return
		}

		if rev != 0 && errors.Is(err, rpctypes.ErrCompacted) {
			logc.Errorf(cli.Ctx(), "etcd watch stream has been compacted, try to reload, rev %d", rev)
			rev = c.load(cli, key)
		}

		// 鉴权类错误在原连接上重试没有意义：token 已失效且不会自动重新登录，
		// 持续重试会让 etcd 反复校验过期 token（日志刷屏 + CPU 飙升）。
		// 换一条新连接并重新对齐一次数据后立即恢复 watch。
		if isAuthError(err) {
			if fresh, cerr := c.reauth(cli); cerr == nil && fresh != cli {
				cli = fresh
				rev = c.load(cli, key)
				continue
			}
		}

		// 当前连接可能已被 reauth/预刷新替换并关闭，在废弃连接上重试只会持续失败。
		if cur, cerr := c.getClient(); cerr == nil && cur != cli {
			cli = cur
			continue
		}

		// log the error and retry
		logc.Error(cli.Ctx(), err)
		// 退避后重连：故障持续时（如 auth token 持续失效、etcd 长时间不可达）避免无间隔空转，
		// 否则会反复刷日志并占满 CPU。连续失败时指数退避并封顶，成功一次即复位。
		if !c.sleepOrDone(c.nextAuthBackoff()) {
			return
		}
	}
}

func (c *cluster) watchStream(cli EtcdClient, key watchKey, rev int64) (int64, error) {
	ctx, rch := c.setupWatch(cli, key, rev)

	for {
		select {
		case wresp, ok := <-rch:
			if !ok {
				return rev, errClosed
			}
			if wresp.Canceled {
				return rev, fmt.Errorf("etcd monitor chan has been canceled, error: %w", wresp.Err())
			}
			if wresp.Err() != nil {
				return rev, fmt.Errorf("etcd monitor chan error: %w", wresp.Err())
			}

			// progress notify 响应无事件，仅更新 revision，供断线重连时断点续传。
			if wresp.IsProgressNotify() {
				rev = wresp.Header.Revision
				continue
			}

			c.handleWatchEvents(ctx, key, wresp.Events)
			rev = wresp.Header.Revision
		case <-ctx.Done():
			return rev, nil
		case <-c.done:
			return rev, nil
		}
	}
}

func (c *cluster) setupWatch(cli EtcdClient, key watchKey, rev int64) (context.Context, clientv3.WatchChan) {
	var (
		rch  clientv3.WatchChan
		ops  []clientv3.OpOption
		wkey = key.key
	)

	if !key.exactMatch {
		wkey = makeKeyPrefix(key.key)
		ops = append(ops, clientv3.WithPrefix())
	}
	if rev != 0 {
		ops = append(ops, clientv3.WithRev(rev+1))
	}
	// 请求服务端在无事件时定期发送进度通知。普通事件与进度通知都会推进
	// revision，断线重连可从最后处理位置继续，避免从初次 load 的位置重放。
	ops = append(ops, clientv3.WithProgressNotify())

	ctx, cancel := context.WithCancel(cli.Ctx())
	if watcher, ok := c.watchers[key]; ok {
		watcher.cancel = cancel
	} else {
		val := newWatchValue()
		val.cancel = cancel

		c.lock.Lock()
		c.watchers[key] = val
		c.lock.Unlock()
	}

	rch = cli.Watch(clientv3.WithRequireLeader(ctx), wkey, ops...)

	return ctx, rch
}

func (c *cluster) watchConnState(cli EtcdClient, conn *grpc.ClientConn) {
	watcher := newStateWatcher()
	watcher.addListener(func() {
		// 该连接已被 reauth/预刷新替换时忽略其状态变化，
		// 否则会用携带过期 token 的废弃连接触发一次全量重载。
		if cur, err := c.getClient(); err == nil && cur != cli {
			return
		}
		go c.reload(cli)
	})
	// cli.Ctx() 在客户端 Close 后结束，watcher 随之退出，不残留 goroutine。
	watcher.watch(cli.Ctx(), conn)
}

// DialClient dials an etcd cluster with given endpoints.
func DialClient(endpoints []string) (EtcdClient, error) {
	cfg := clientv3.Config{
		Endpoints:           endpoints,
		DialTimeout:         DialTimeout,
		RejectOldCluster:    true,
		PermitWithoutStream: true,
	}
	// 单端点部署不做成员列表同步。clientv3 的 AutoSyncInterval 会周期性拉取集群成员，
	// 并用成员通告的 advertise-client-urls 整体替换本地 endpoints；一旦通告地址不可达
	// （机器换过 IP、advertise 配错、容器重建），客户端就会丢掉唯一可用地址，
	// 表现为启动初期正常、约一分钟后开始 connection refused，且改配置也无效。
	// 多端点时保留同步，以便自动感知集群成员扩缩。
	if len(endpoints) > 1 {
		cfg.AutoSyncInterval = autoSyncInterval
	}
	if account, ok := GetAccount(endpoints); ok {
		cfg.Username = account.User
		cfg.Password = account.Pass
	}
	if tlsCfg, ok := GetTLS(endpoints); ok {
		cfg.TLS = tlsCfg
	}

	cli, err := clientv3.New(cfg)
	if err != nil {
		return nil, err
	}

	// 此处不再做认证探测：clientv3.New 内部已执行一次 Authenticate（getToken），
	// 凭据错误时会关闭连接并返回错误。额外探测会让每次重建连接（鉴权自愈、
	// token 预刷新）都多打一次 Authenticate，在鉴权失败场景下放大 etcd 压力。

	return cli, nil
}

// isAuthError 判断错误是否属于鉴权类失败。这类错误只能通过重新登录解决，
// 在原连接上重试没有意义。
func isAuthError(err error) bool {
	if err == nil {
		return false
	}
	if status.Code(err) == codes.Unauthenticated {
		return true
	}
	if errors.Is(err, rpctypes.ErrUserEmpty) ||
		errors.Is(err, rpctypes.ErrAuthOldRevision) ||
		errors.Is(err, rpctypes.ErrInvalidAuthToken) {
		return true
	}

	// 字符串兜底：错误被上层二次包装成非 rpctypes 类型时 errors.Is 会失效。
	msg := err.Error()
	return strings.Contains(msg, "invalid auth token") ||
		strings.Contains(msg, "user name is empty")
}

func calculateChanges(oldVals, newVals map[string]string) (add, remove []KV) {
	for k, v := range newVals {
		if val, ok := oldVals[k]; !ok || v != val {
			add = append(add, KV{
				Key: k,
				Val: v,
			})
		}
	}

	for k, v := range oldVals {
		if val, ok := newVals[k]; !ok || v != val {
			remove = append(remove, KV{
				Key: k,
				Val: v,
			})
		}
	}

	return add, remove
}

func getClusterKey(endpoints []string) string {
	sort.Strings(endpoints)
	return strings.Join(endpoints, endpointsSeparator)
}

func makeKeyPrefix(key string) string {
	return fmt.Sprintf("%s%c", key, Delimiter)
}

// NewClient returns a watchValue that make sure values are not nil.
func newWatchValue() *watchValue {
	return &watchValue{
		values: make(map[string]string),
	}
}
