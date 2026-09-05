package config

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"
)

type Config struct {
	Server ServerConfig `mapstructure:"server"`
	MySQL  MySQLConfig  `mapstructure:"mysql"`
	Redis  RedisConfig  `mapstructure:"redis"`
	Neo4j  Neo4jConfig  `mapstructure:"neo4j"`
	Queue  QueueConfig  `mapstructure:"queue"`
	LLM    LLMConfig    `mapstructure:"llm"`
}

type ServerConfig struct {
	Port int    `mapstructure:"port"`
	Mode string `mapstructure:"mode"`
}

type MySQLConfig struct {
	DSN          string `mapstructure:"dsn"`
	MaxIdleConns int    `mapstructure:"max_idle_conns"`
	MaxOpenConns int    `mapstructure:"max_open_conns"`
}

type RedisConfig struct {
	Mode          string   `mapstructure:"mode"`
	Addr          string   `mapstructure:"addr"`
	Password      string   `mapstructure:"password"`
	DB            int      `mapstructure:"db"`
	MasterName    string   `mapstructure:"master_name"`
	SentinelAddrs []string `mapstructure:"sentinel_addrs"`
}

func (c RedisConfig) UseSentinel() bool {
	return strings.EqualFold(strings.TrimSpace(c.Mode), "sentinel")
}

// VectorStoreURL returns a direct redis:// URL for components that do not use
// sentinel discovery and still require a direct Redis endpoint.
func (c RedisConfig) VectorStoreURL() string {
	addr := strings.TrimSpace(c.Addr)
	if addr == "" {
		return ""
	}
	if strings.HasPrefix(addr, "redis://") || strings.HasPrefix(addr, "rediss://") {
		return addr
	}
	return "redis://" + addr
}

type Neo4jConfig struct {
	URI      string `mapstructure:"uri"`
	Username string `mapstructure:"username"`
	Password string `mapstructure:"password"`
}

type QueueConfig struct {
	Capacity int `mapstructure:"capacity"`
	Workers  int `mapstructure:"workers"`
	Processes int `mapstructure:"processes"`
}

type LLMConfig struct {
	APIKey         string `mapstructure:"api_key"`
	BaseURL        string `mapstructure:"base_url"`
	Model          string `mapstructure:"model"`
	EmbeddingModel string `mapstructure:"embedding_model"`
}

var AppConfig Config
var ProjectRoot string

// InitConfig 初始化配置，支持从文件和环境变量读取
func InitConfig(configPath string) {
	resolvedConfigPath, projectRoot, err := resolveConfigPath(configPath)
	if err != nil {
		log.Fatalf("Error reading config file: %v", err)
	}

	ProjectRoot = projectRoot
	viper.SetConfigFile(resolvedConfigPath)
	viper.AutomaticEnv()                                   // 读取环境变量
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_")) // 例如 MYSQL_DSN

	if err := viper.ReadInConfig(); err != nil {
		log.Fatalf("Error reading config file: %v", err)
	}

	if err := viper.Unmarshal(&AppConfig); err != nil {
		log.Fatalf("Unable to decode into struct: %v", err)
	}

	log.Println("Configuration loaded successfully")
}

// ResolveProjectPath 将相对项目根目录的路径解析为绝对路径。
func ResolveProjectPath(parts ...string) string {
	if ProjectRoot == "" {
		root, err := findProjectRoot()
		if err != nil {
			return filepath.Join(parts...)
		}
		ProjectRoot = root
	}

	allParts := append([]string{ProjectRoot}, parts...)
	return filepath.Join(allParts...)
}

func resolveConfigPath(configPath string) (string, string, error) {
	if configPath == "" {
		configPath = filepath.Join("config", "config.yaml")
	}
	if filepath.IsAbs(configPath) {
		if !fileExists(configPath) {
			return "", "", fmt.Errorf("config file %q not found", configPath)
		}
		root, err := findProjectRootFrom(filepath.Dir(configPath))
		if err != nil {
			root = filepath.Dir(filepath.Dir(configPath))
		}
		return configPath, root, nil
	}

	relativePath := filepath.FromSlash(configPath)
	for _, startDir := range candidateStartDirs() {
		dir := startDir
		for {
			candidate := filepath.Join(dir, relativePath)
			if fileExists(candidate) {
				return candidate, dir, nil
			}

			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}

	return "", "", fmt.Errorf("config file %q not found from current working directory or executable path", configPath)
}

func findProjectRoot() (string, error) {
	for _, startDir := range candidateStartDirs() {
		if root, err := findProjectRootFrom(startDir); err == nil {
			return root, nil
		}
	}
	return "", fmt.Errorf("project root not found")
}

func findProjectRootFrom(startDir string) (string, error) {
	dir := startDir
	for {
		if fileExists(filepath.Join(dir, "go.mod")) && fileExists(filepath.Join(dir, "config", "config.yaml")) {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return "", fmt.Errorf("project root not found from %q", startDir)
}

func candidateStartDirs() []string {
	var dirs []string
	seen := make(map[string]struct{})

	addDir := func(dir string) {
		if dir == "" {
			return
		}
		cleanDir := filepath.Clean(dir)
		if _, ok := seen[cleanDir]; ok {
			return
		}
		seen[cleanDir] = struct{}{}
		dirs = append(dirs, cleanDir)
	}

	if cwd, err := os.Getwd(); err == nil {
		addDir(cwd)
	}
	if exePath, err := os.Executable(); err == nil {
		addDir(filepath.Dir(exePath))
	}

	return dirs
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
