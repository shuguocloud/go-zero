package main

import (
	"flag"
	"fmt"

	"github.com/shuguocloud/go-zero/core/conf"
	"github.com/shuguocloud/go-zero/core/service"
	"github.com/shuguocloud/go-zero/tools/goctl/example/rpc/hello/internal/config"
	greetServer "github.com/shuguocloud/go-zero/tools/goctl/example/rpc/hello/internal/server/greet"
	"github.com/shuguocloud/go-zero/tools/goctl/example/rpc/hello/internal/svc"
	"github.com/shuguocloud/go-zero/tools/goctl/example/rpc/hello/pb/hello"
	"github.com/shuguocloud/go-zero/zrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

var configFile = flag.String("f", "etc/hello.yaml", "the config file")

func main() {
	flag.Parse()

	var c config.Config
	conf.MustLoad(*configFile, &c)
	ctx := svc.NewServiceContext(c)

	s := zrpc.MustNewServer(c.RpcServerConf, func(grpcServer *grpc.Server) {
		hello.RegisterGreetServer(grpcServer, greetServer.NewGreetServer(ctx))

		if c.Mode == service.DevMode || c.Mode == service.TestMode {
			reflection.Register(grpcServer)
		}
	})
	defer s.Stop()

	fmt.Printf("Starting rpc server at %s...\n", c.ListenOn)
	s.Start()
}
