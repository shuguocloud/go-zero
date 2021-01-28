package upgrade

import (
	"fmt"

	"github.com/shuguocloud/go-zero/tools/goctl/rpc/execx"
	"github.com/urfave/cli"
)

func Upgrade(_ *cli.Context) error {
	info, err := execx.Run("GO111MODULE=on GOPROXY=https://goproxy.cn/,direct go get -u github.com/shuguocloud/go-zero/tools/goctl", "")
	if err != nil {
		return err
	}

	fmt.Print(info)
	return nil
}
