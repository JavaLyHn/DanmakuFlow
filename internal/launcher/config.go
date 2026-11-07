package launcher

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/JavaLyHn/DanmakuFlow/internal/runcontrol"
	mysqldriver "github.com/go-sql-driver/mysql"
)

type MySQLConfig struct {
	Executable string `json:"executable"`
	ConfigFile string `json:"configFile"`
}
type RedisConfig struct {
	Executable string `json:"executable"`
	ConfigFile string `json:"configFile"`
	WorkingDir string `json:"workingDir"`
}
type KafkaConfig struct {
	JavaExecutable string `json:"javaExecutable"`
	Home           string `json:"home"`
	ConfigFile     string `json:"configFile"`
}
type DockerConfig struct {
	Executable  string `json:"executable"`
	Context     string `json:"context"`
	Project     string `json:"project"`
	ComposeFile string `json:"composeFile"`
	EnvFile     string `json:"envFile"`
}
type Config struct {
	Version int          `json:"version"`
	Backend string       `json:"backend,omitempty"`
	RepoDir string       `json:"repoDir"`
	BinDir  string       `json:"binDir"`
	MySQL   MySQLConfig  `json:"mysql"`
	Redis   RedisConfig  `json:"redis"`
	Kafka   KafkaConfig  `json:"kafka"`
	Docker  DockerConfig `json:"docker,omitempty"`
}

func LoadConfig(path string) (Config, error) {
	var c Config
	if err := runcontrol.ReadJSON(path, &c); err != nil {
		return c, fmt.Errorf("请先运行 scripts/setup-local.ps1 配置本机路径与数据库连接：%w", err)
	}
	return c, nil
}
func parseLocalDSN(dsn string) (*mysqldriver.Config, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("未配置数据库连接；请运行 scripts/setup-local.ps1，然后重新打开本地页面入口")
	}
	c, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		return nil, errors.New("DANMAKUFLOW_MYSQL_DSN 格式无效")
	}
	if c.Net != "tcp" || (c.Addr != "127.0.0.1:3306" && c.Addr != "localhost:3306") || strings.TrimSpace(c.DBName) == "" {
		return nil, errors.New("本地启动器需要明确指定 127.0.0.1:3306 上的数据库；请检查本机 DSN 配置")
	}
	return c, nil
}
func configKey(c Config, db *mysqldriver.Config) string {
	target := ""
	if db != nil {
		target = db.Net + "|" + db.Addr + "|" + db.User + "|" + db.DBName
	}
	data, _ := json.Marshal(c)
	if c.Backend == "" || c.Backend == "native" {
		// Keep existing native lifecycle records recoverable across this update.
		data, _ = json.Marshal(struct {
			Version int         `json:"version"`
			RepoDir string      `json:"repoDir"`
			BinDir  string      `json:"binDir"`
			MySQL   MySQLConfig `json:"mysql"`
			Redis   RedisConfig `json:"redis"`
			Kafka   KafkaConfig `json:"kafka"`
		}{c.Version, c.RepoDir, c.BinDir, c.MySQL, c.Redis, c.Kafka})
	}
	sum := sha256.Sum256(append(data, []byte(target)...))
	return hex.EncodeToString(sum[:])
}
func checkPath(path, label string, dir bool) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%s需要绝对路径，请更新本机配置", label)
	}
	s, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s不存在或不可访问", label)
	}
	if s.IsDir() != dir || (!dir && !s.Mode().IsRegular()) {
		return fmt.Errorf("%s路径类型不正确", label)
	}
	return nil
}
func samePath(a, b string) bool {
	for _, p := range []*string{&a, &b} {
		if real, err := filepath.EvalSymlinks(*p); err == nil {
			*p = real
		}
		*p = strings.TrimPrefix(filepath.Clean(*p), `\\?\`)
	}
	return strings.EqualFold(a, b)
}
