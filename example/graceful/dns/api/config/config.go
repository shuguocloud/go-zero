package config

import (
	"github.com/shuguocloud/go-zero/rest"
	"github.com/shuguocloud/go-zero/zrpc"
)

type Config struct {
	rest.RestConf
	Rpc zrpc.RpcClientConf
}
