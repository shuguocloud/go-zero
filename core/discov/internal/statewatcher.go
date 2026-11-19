//go:generate mockgen -package internal -destination statewatcher_mock.go -source statewatcher.go etcdConn

package internal

import (
	"context"
	"sync"

	"google.golang.org/grpc/connectivity"
)

type (
	etcdConn interface {
		GetState() connectivity.State
		WaitForStateChange(ctx context.Context, sourceState connectivity.State) bool
	}

	stateWatcher struct {
		disconnected bool
		currentState connectivity.State
		listeners    []func()
		// lock only guards listeners, because only listens can be accessed by other goroutines.
		lock sync.Mutex
	}
)

func newStateWatcher() *stateWatcher {
	return new(stateWatcher)
}

func (sw *stateWatcher) addListener(l func()) {
	sw.lock.Lock()
	sw.listeners = append(sw.listeners, l)
	sw.lock.Unlock()
}

func (sw *stateWatcher) notifyListeners() {
	sw.lock.Lock()
	defer sw.lock.Unlock()

	for _, l := range sw.listeners {
		l()
	}
}

func (sw *stateWatcher) updateState(conn etcdConn) {
	sw.currentState = conn.GetState()
	switch sw.currentState {
	case connectivity.TransientFailure, connectivity.Shutdown:
		sw.disconnected = true
	case connectivity.Ready:
		if sw.disconnected {
			sw.disconnected = false
			sw.notifyListeners()
		}
	}
}

// watch 监听连接状态变化，ctx 结束（连接被关闭）时退出。
// 旧实现使用 context.Background()，且忽略 WaitForStateChange 的 false 返回值，
// 连接被 reauth/预刷新替换后该 goroutine 会永久阻塞在状态等待上，造成泄漏。
func (sw *stateWatcher) watch(ctx context.Context, conn etcdConn) {
	if ctx == nil {
		ctx = context.Background()
	}

	sw.currentState = conn.GetState()
	for {
		if !conn.WaitForStateChange(ctx, sw.currentState) {
			return
		}
		sw.updateState(conn)
	}
}
