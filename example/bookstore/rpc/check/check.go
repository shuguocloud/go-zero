// Code generated by goctl. DO NOT EDIT!
// Source: check.proto

package main

import (
	"flag"
	"fmt"

	"bookstore/rpc/check/check"
	"bookstore/rpc/check/internal/config"
	"bookstore/rpc/check/internal/server"
	"bookstore/rpc/check/internal/svc"

	"github.com/shuguocloud/go-zero/core/conf"
	"github.com/shuguocloud/go-zero/core/logx"
	"github.com/shuguocloud/go-zero/zrpc"
	"google.golang.org/grpc"
)

var configFile = flag.String("f", "etc/check.yaml", "the config file")

func main() {
	flag.Parse()

	var c config.Config
	conf.MustLoad(*configFile, &c)
	ctx := svc.NewServiceContext(c)
	checkerSrv := server.NewCheckerServer(ctx)

	s, err := zrpc.NewServer(c.RpcServerConf, func(grpcServer *grpc.Server) {
		check.RegisterCheckerServer(grpcServer, checkerSrv)
	})
	logx.Must(err)

	fmt.Printf("Starting rpc server at %s...\n", c.ListenOn)
	s.Start()
}
