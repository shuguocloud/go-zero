package service

import (
	"testing"

	"github.com/shuguocloud/go-zero/core/logx"
)

func TestServiceConf(t *testing.T) {
	c := ServiceConf{
		Name: "foo",
		Log: logx.LogConf{
			Mode: "console",
		},
		Mode: "dev",
	}
	c.MustSetUp()
}
