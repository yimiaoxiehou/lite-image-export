package main

import (
	"time"
)

// SSHConfig SSH配置结构体
type SSHConfig struct {
	Enabled           bool          `toml:"enabled"`            // 是否启用SSH功能
	DefaultTimeout    time.Duration `toml:"default_timeout"`    // 默认超时时间
	MaxConnections    int           `toml:"max_connections"`    // 最大连接数
	CleanupInterval   time.Duration `toml:"cleanup_interval"`   // 清理间隔
	InactiveTimeout   time.Duration `toml:"inactive_timeout"`   // 非活跃超时
	AllowedCommands   []string      `toml:"allowed_commands"`   // 允许执行的命令
	ForbiddenCommands []string      `toml:"forbidden_commands"` // 禁止执行的命令
	KeySize           int           `toml:"key_size"`           // 生成密钥的位数
}

// DefaultSSHConfig 返回默认SSH配置
func DefaultSSHConfig() *SSHConfig {
	return &SSHConfig{
		Enabled:           true,
		DefaultTimeout:    30 * time.Second,
		MaxConnections:    100,
		CleanupInterval:   5 * time.Minute,
		InactiveTimeout:   30 * time.Minute,
		AllowedCommands:   []string{},
		ForbiddenCommands: []string{"rm -rf /", "dd if=/dev/zero", "mkfs", "fdisk"},
		KeySize:           2048,
	}
}

// IsCommandAllowed 检查命令是否被允许执行
func (sc *SSHConfig) IsCommandAllowed(command string) bool {
	// 检查禁止的命令
	for _, forbidden := range sc.ForbiddenCommands {
		if contains(command, forbidden) {
			return false
		}
	}

	// 如果没有设置允许的命令列表，则允许所有未被禁止的命令
	if len(sc.AllowedCommands) == 0 {
		return true
	}

	// 检查允许的命令
	for _, allowed := range sc.AllowedCommands {
		if contains(command, allowed) {
			return true
		}
	}

	return false
}

// contains 检查字符串是否包含子字符串
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr ||
		(len(s) > len(substr) && (s[:len(substr)] == substr ||
			s[len(s)-len(substr):] == substr ||
			containsSubstring(s, substr))))
}

// containsSubstring 检查字符串中间是否包含子字符串
func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
