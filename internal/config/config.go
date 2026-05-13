package config

import (
	"log"
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
	Addr     string `mapstructure:"addr"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`
}

type Neo4jConfig struct {
	URI      string `mapstructure:"uri"`
	Username string `mapstructure:"username"`
	Password string `mapstructure:"password"`
}

type QueueConfig struct {
	Capacity int `mapstructure:"capacity"`
	Workers  int `mapstructure:"workers"`
}

type LLMConfig struct {
	APIKey         string `mapstructure:"api_key"`
	BaseURL        string `mapstructure:"base_url"`
	Model          string `mapstructure:"model"`
	EmbeddingModel string `mapstructure:"embedding_model"`
}

var AppConfig Config

// InitConfig 初始化配置，支持从文件和环境变量读取
func InitConfig(configPath string) {
	viper.SetConfigFile(configPath)
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
