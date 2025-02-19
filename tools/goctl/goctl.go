package main

import (
	"github.com/shuguocloud/go-zero/core/load"
	"github.com/shuguocloud/go-zero/core/logx"
	"github.com/shuguocloud/go-zero/tools/goctl/cmd"
)

func main() {
	logx.Disable()
	load.Disable()
	cmd.Execute()
}
