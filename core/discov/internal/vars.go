package internal

import "time"

const (
	// Delimiter is a separator that separates the etcd path.
	Delimiter = '/'

	autoSyncInterval   = time.Minute
	coolDownInterval   = time.Second
	dialTimeout        = 5 * time.Second
	requestTimeout     = 3 * time.Second
	endpointsSeparator = ","
	// tokenTTL 与服务端 simple 的 auth-token-ttl: 86400 或 JWT 的 ttl=24h 对齐。
	// 若服务端改了 TTL，这里需要同步调整，否则预刷新节奏会偏离实际到期时间。
	tokenTTL = 24 * time.Hour
	// tokenRefreshMargin 在 token 到期前多久预刷新，避免到期瞬间所有 watch 同时断连。
	tokenRefreshMargin = 10 * time.Minute
)

var (
	// DialTimeout is the dial timeout.
	DialTimeout = dialTimeout
	// RequestTimeout is the request timeout.
	RequestTimeout = requestTimeout
	// NewClient is used to create etcd clients.
	NewClient = DialClient
)
