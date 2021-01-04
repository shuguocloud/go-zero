package config

import (
	"github.com/shuguocloud/go-zero/core/stores/cache"
	"github.com/shuguocloud/go-zero/zrpc"
)

type Config struct {
	zrpc.RpcServerConf
	DataSource string
	Cache      cache.CacheConf
}
