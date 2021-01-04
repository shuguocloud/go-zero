package config

import "github.com/shuguocloud/go-zero/core/logx"

type Config struct {
	logx.LogConf
	ListenOn string
	FileDir  string
	FilePath string
}
