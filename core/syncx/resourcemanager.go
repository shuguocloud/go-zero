package syncx

import (
	"errors"
	"io"
	"sync"

	"github.com/shuguocloud/go-zero/core/errorx"
)

// ErrManagerClosed is returned when GetResource is called after Close.
var ErrManagerClosed = errors.New("resource manager is closed")

// A ResourceManager is a manager that used to manage resources.
type ResourceManager struct {
	resources    map[string]io.Closer
	singleFlight SingleFlight
	lock         sync.RWMutex
}

// NewResourceManager returns a ResourceManager.
func NewResourceManager() *ResourceManager {
	return &ResourceManager{
		resources:    make(map[string]io.Closer),
		singleFlight: NewSingleFlight(),
	}
}

// Close closes the manager.
// Don't use the ResourceManager after Close() called.
func (manager *ResourceManager) Close() error {
	manager.lock.Lock()
	defer manager.lock.Unlock()

	var be errorx.BatchError
	for _, resource := range manager.resources {
		if err := resource.Close(); err != nil {
			be.Add(err)
		}
	}

	// release resources to avoid using it later
	manager.resources = nil

	return be.Err()
}

// GetResource returns the resource associated with given key.
func (manager *ResourceManager) GetResource(key string, create func() (io.Closer, error)) (
	io.Closer, error) {
	val, err := manager.singleFlight.Do(key, func() (any, error) {
		manager.lock.RLock()
		resource, ok := manager.resources[key]
		manager.lock.RUnlock()
		if ok {
			return resource, nil
		}

		resource, err := create()
		if err != nil {
			return nil, err
		}

		manager.lock.Lock()
		defer manager.lock.Unlock()
		// Audit 029：Close 后 resources 已置 nil，禁止写入避免 nil map panic；
		// 同时关闭刚创建的资源防止泄漏
		if manager.resources == nil {
			_ = resource.Close()
			return nil, ErrManagerClosed
		}
		manager.resources[key] = resource

		return resource, nil
	})
	if err != nil {
		return nil, err
	}

	return val.(io.Closer), nil
}

// Inject injects the resource associated with given key.
func (manager *ResourceManager) Inject(key string, resource io.Closer) {
	manager.lock.Lock()
	defer manager.lock.Unlock()

	// Audit 029：Close 后禁止注入，避免 nil map panic；关闭资源防止泄漏
	if manager.resources == nil {
		_ = resource.Close()
		return
	}

	manager.resources[key] = resource
}
