package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config 应用程序配置
type Config struct {
	SSH struct {
		Host     string `json:"host" env:"SSH_HOST"`
		Port     int    `json:"port" env:"SSH_PORT"`
		Username string `json:"username" env:"SSH_USERNAME"`
		Password string `json:"password" env:"SSH_PASSWORD"`
		KeyFile  string `json:"key_file" env:"SSH_KEY_FILE"`
	} `json:"ssh"`

	Download struct {
		MaxRetries    int           `json:"max_retries" env:"DOWNLOAD_MAX_RETRIES"`
		RetryDelay    time.Duration `json:"retry_delay" env:"DOWNLOAD_RETRY_DELAY"`
		OutputDir     string        `json:"output_dir" env:"OUTPUT_DIR"`
		Concurrency   int           `json:"concurrency" env:"DOWNLOAD_CONCURRENCY"`
		DefaultImage  string        `json:"default_image" env:"DEFAULT_IMAGE"`
		DefaultOutput string        `json:"default_output" env:"DEFAULT_OUTPUT"`
	} `json:"download"`

	Logging struct {
		Level  string `json:"level" env:"LOG_LEVEL"`   // DEBUG, INFO, WARN, ERROR, FATAL
		Format string `json:"format" env:"LOG_FORMAT"` // text, json
	} `json:"logging"`
}

// DefaultConfig 返回默认配置
func DefaultConfig() *Config {
	cfg := &Config{}

	// SSH默认配置
	cfg.SSH.Host = "192.168.44.213"
	cfg.SSH.Port = 22
	cfg.SSH.Username = "root"
	cfg.SSH.Password = "Unitech@1998"

	// 下载默认配置
	cfg.Download.MaxRetries = 5
	cfg.Download.RetryDelay = 2 * time.Second
	cfg.Download.OutputDir = "output"
	cfg.Download.Concurrency = 1
	cfg.Download.DefaultImage = "docker.utpf.cn/platform/edgeai-videostreamserver:V1.00.02.03_Omnisky"
	cfg.Download.DefaultOutput = "output.tar"

	// 日志默认配置
	cfg.Logging.Level = "INFO"
	cfg.Logging.Format = "text"

	return cfg
}

// ParseLogLevel 解析日志级别字符串
func ParseLogLevel(level string) LogLevel {
	switch strings.ToUpper(level) {
	case "DEBUG":
		return DEBUG
	case "INFO":
		return INFO
	case "WARN", "WARNING":
		return WARN
	case "ERROR":
		return ERROR
	case "FATAL":
		return FATAL
	default:
		return INFO // 默认INFO级别
	}
}

// LoadConfig 从文件和环境变量加载配置
func LoadConfig(configPath string) (*Config, error) {
	cfg := DefaultConfig()

	// 如果配置文件存在，从文件加载
	if configPath != "" {
		if _, err := os.Stat(configPath); err == nil {
			file, err := os.Open(configPath)
			if err != nil {
				return nil, WrapError(ErrCodeFileOperation, "打开配置文件失败", err)
			}
			defer file.Close()

			decoder := json.NewDecoder(file)
			if err := decoder.Decode(cfg); err != nil {
				return nil, WrapError(ErrCodeConfigError, "解析配置文件失败", err)
			}
		}
	}

	// 从环境变量覆盖配置
	overrideFromEnv(cfg)

	// 验证配置
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

// 从环境变量覆盖配置
func overrideFromEnv(cfg *Config) {
	// SSH配置
	if host := os.Getenv("SSH_HOST"); host != "" {
		cfg.SSH.Host = host
	}

	if portStr := os.Getenv("SSH_PORT"); portStr != "" {
		if port, err := strconv.Atoi(portStr); err == nil {
			cfg.SSH.Port = port
		}
	}

	if username := os.Getenv("SSH_USERNAME"); username != "" {
		cfg.SSH.Username = username
	}

	if password := os.Getenv("SSH_PASSWORD"); password != "" {
		cfg.SSH.Password = password
	}

	if keyFile := os.Getenv("SSH_KEY_FILE"); keyFile != "" {
		cfg.SSH.KeyFile = keyFile
	}

	// 下载配置
	if retriesStr := os.Getenv("DOWNLOAD_MAX_RETRIES"); retriesStr != "" {
		if retries, err := strconv.Atoi(retriesStr); err == nil {
			cfg.Download.MaxRetries = retries
		}
	}

	if delayStr := os.Getenv("DOWNLOAD_RETRY_DELAY"); delayStr != "" {
		if delay, err := strconv.Atoi(delayStr); err == nil {
			cfg.Download.RetryDelay = time.Duration(delay) * time.Second
		}
	}

	if outputDir := os.Getenv("OUTPUT_DIR"); outputDir != "" {
		cfg.Download.OutputDir = outputDir
	}

	if concurrencyStr := os.Getenv("DOWNLOAD_CONCURRENCY"); concurrencyStr != "" {
		if concurrency, err := strconv.Atoi(concurrencyStr); err == nil {
			cfg.Download.Concurrency = concurrency
		}
	}

	if defaultImage := os.Getenv("DEFAULT_IMAGE"); defaultImage != "" {
		cfg.Download.DefaultImage = defaultImage
	}

	if defaultOutput := os.Getenv("DEFAULT_OUTPUT"); defaultOutput != "" {
		cfg.Download.DefaultOutput = defaultOutput
	}
}

// 验证配置
func validateConfig(cfg *Config) error {
	// 验证SSH配置
	if cfg.SSH.Host == "" {
		return WrapError(ErrCodeConfigError, "SSH主机不能为空", nil)
	}

	if cfg.SSH.Port <= 0 || cfg.SSH.Port > 65535 {
		return WrapError(ErrCodeConfigError, fmt.Sprintf("SSH端口无效: %d", cfg.SSH.Port), nil)
	}

	if cfg.SSH.Username == "" {
		return WrapError(ErrCodeConfigError, "SSH用户名不能为空", nil)
	}

	// 密码和密钥文件至少需要一个
	if cfg.SSH.Password == "" && cfg.SSH.KeyFile == "" {
		return WrapError(ErrCodeConfigError, "SSH密码和密钥文件不能同时为空", nil)
	}

	// 如果指定了密钥文件，验证文件存在
	if cfg.SSH.KeyFile != "" {
		if _, err := os.Stat(cfg.SSH.KeyFile); os.IsNotExist(err) {
			return WrapError(ErrCodeConfigError, fmt.Sprintf("SSH密钥文件不存在: %s", cfg.SSH.KeyFile), err)
		}
	}

	// 验证下载配置
	if cfg.Download.MaxRetries < 0 {
		return WrapError(ErrCodeConfigError, fmt.Sprintf("最大重试次数不能为负数: %d", cfg.Download.MaxRetries), nil)
	}

	if cfg.Download.RetryDelay < 0 {
		return WrapError(ErrCodeConfigError, fmt.Sprintf("重试延迟不能为负数: %s", cfg.Download.RetryDelay), nil)
	}

	if cfg.Download.Concurrency <= 0 {
		return WrapError(ErrCodeConfigError, fmt.Sprintf("并发数必须大于0: %d", cfg.Download.Concurrency), nil)
	}

	// 确保输出目录存在或可创建
	if cfg.Download.OutputDir != "" {
		outputDir := cfg.Download.OutputDir
		if _, err := os.Stat(outputDir); os.IsNotExist(err) {
			// 尝试创建目录
			if err := os.MkdirAll(outputDir, 0755); err != nil {
				return WrapError(ErrCodeFileOperation, fmt.Sprintf("无法创建输出目录 %s", outputDir), err)
			}
		}
	}

	return nil
}

// SaveConfig 保存配置到文件
func SaveConfig(cfg *Config, configPath string) error {
	// 确保目录存在
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return WrapError(ErrCodeFileOperation, "创建配置目录失败", err)
	}

	// 将配置序列化为JSON
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return WrapError(ErrCodeConfigError, "序列化配置失败", err)
	}

	// 写入文件
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return WrapError(ErrCodeFileOperation, "写入配置文件失败", err)
	}

	return nil
}
