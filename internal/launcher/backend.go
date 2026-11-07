package launcher

import "errors"

type LocalRuntime interface {
	Driver
	ConfigKey() string
	LogTail(string) (string, error)
	TrimLogs()
}

func NewLocalRuntime(c Config, configErr error, stateDir string) LocalRuntime {
	if c.Backend == "docker" {
		return NewDockerRuntime(c, configErr, stateDir)
	}
	if c.Backend != "" && c.Backend != "native" {
		configErr = errors.New("不支持的本机运行方式，请重新配置启动器")
	}
	return NewRuntime(c, configErr, stateDir)
}
