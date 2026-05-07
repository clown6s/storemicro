package main

import (
	"flag"
)

// 命令行参数（只保留配置文件路径）
var configFile string

func init() {
	flag.StringVar(&configFile, "conf", "configs/config.yaml", "config file path")
}
