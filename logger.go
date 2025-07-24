package main

import (
	"fmt"
	"log"
	"os"
	"runtime"
	"strings"
	"time"
)

// LogLevel 日志级别
type LogLevel int

const (
	DEBUG LogLevel = iota
	INFO
	WARN
	ERROR
	FATAL
)

// String 返回日志级别的字符串表示
func (l LogLevel) String() string {
	switch l {
	case DEBUG:
		return "DEBUG"
	case INFO:
		return "INFO"
	case WARN:
		return "WARN"
	case ERROR:
		return "ERROR"
	case FATAL:
		return "FATAL"
	default:
		return "UNKNOWN"
	}
}

// Logger 统一日志记录器
type Logger struct {
	level  LogLevel
	logger *log.Logger
}

// 全局日志记录器
var globalLogger *Logger

// InitLogger 初始化全局日志记录器
func InitLogger(level LogLevel) {
	globalLogger = &Logger{
		level:  level,
		logger: log.New(os.Stdout, "", 0), // 不使用默认前缀，我们自定义格式
	}
}

// SetLogLevel 设置日志级别
func SetLogLevel(level LogLevel) {
	if globalLogger != nil {
		globalLogger.level = level
	}
}

// GetLogger 获取全局日志记录器
func GetLogger() *Logger {
	if globalLogger == nil {
		InitLogger(INFO) // 默认INFO级别
	}
	return globalLogger
}

// formatMessage 格式化日志消息
func (l *Logger) formatMessage(level LogLevel, msg string) string {
	now := time.Now().Format("2006-01-02 15:04:05")

	// 获取调用者信息
	_, file, line, ok := runtime.Caller(3) // 跳过3层调用栈
	var caller string
	if ok {
		// 只保留文件名，不要完整路径
		parts := strings.Split(file, "/")
		filename := parts[len(parts)-1]
		caller = fmt.Sprintf("%s:%d", filename, line)
	} else {
		caller = "unknown"
	}

	return fmt.Sprintf("[%s] %s [%s] %s", now, level.String(), caller, msg)
}

// shouldLog 检查是否应该记录该级别的日志
func (l *Logger) shouldLog(level LogLevel) bool {
	return level >= l.level
}

// Debug 记录DEBUG级别日志
func (l *Logger) Debug(format string, args ...interface{}) {
	if l.shouldLog(DEBUG) {
		msg := fmt.Sprintf(format, args...)
		l.logger.Println(l.formatMessage(DEBUG, msg))
	}
}

// Info 记录INFO级别日志
func (l *Logger) Info(format string, args ...interface{}) {
	if l.shouldLog(INFO) {
		msg := fmt.Sprintf(format, args...)
		l.logger.Println(l.formatMessage(INFO, msg))
	}
}

// Warn 记录WARN级别日志
func (l *Logger) Warn(format string, args ...interface{}) {
	if l.shouldLog(WARN) {
		msg := fmt.Sprintf(format, args...)
		l.logger.Println(l.formatMessage(WARN, msg))
	}
}

// Error 记录ERROR级别日志
func (l *Logger) Error(format string, args ...interface{}) {
	if l.shouldLog(ERROR) {
		msg := fmt.Sprintf(format, args...)
		l.logger.Println(l.formatMessage(ERROR, msg))
	}
}

// Fatal 记录FATAL级别日志并退出程序
func (l *Logger) Fatal(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	l.logger.Println(l.formatMessage(FATAL, msg))
	os.Exit(1)
}

// 全局便捷函数

// LogDebug 全局DEBUG日志
func LogDebug(format string, args ...interface{}) {
	GetLogger().Debug(format, args...)
}

// LogInfo 全局INFO日志
func LogInfo(format string, args ...interface{}) {
	GetLogger().Info(format, args...)
}

// LogWarn 全局WARN日志
func LogWarn(format string, args ...interface{}) {
	GetLogger().Warn(format, args...)
}

// LogError 全局ERROR日志
func LogError(format string, args ...interface{}) {
	GetLogger().Error(format, args...)
}

// LogFatal 全局FATAL日志
func LogFatal(format string, args ...interface{}) {
	GetLogger().Fatal(format, args...)
}

// ErrorCode 错误代码类型
type ErrorCode string

// 错误代码常量
const (
	ErrCodeConfigLoad     ErrorCode = "CONFIG_LOAD_FAILED"     // 配置加载失败
	ErrCodeConfigValidate ErrorCode = "CONFIG_VALIDATE_FAILED" // 配置验证失败
	ErrCodeImageParse     ErrorCode = "IMAGE_PARSE_FAILED"     // 镜像解析失败
	ErrCodeImageDownload  ErrorCode = "IMAGE_DOWNLOAD_FAILED"  // 镜像下载失败
	ErrCodeFileOperation  ErrorCode = "FILE_OPERATION_FAILED"  // 文件操作失败
	ErrCodeNetworkError   ErrorCode = "NETWORK_ERROR"          // 网络错误
	ErrCodeAuthError      ErrorCode = "AUTH_ERROR"             // 认证错误
	ErrCodeChecksumError  ErrorCode = "CHECKSUM_ERROR"         // 校验和错误
	ErrCodeTarAssembly    ErrorCode = "TAR_ASSEMBLY_FAILED"    // TAR组装失败
	ErrCodeConfigError    ErrorCode = "CONFIG_ERROR"
)

// AppError 应用程序错误类型
type AppError struct {
	Code    ErrorCode
	Message string
	Cause   error
}

// Error 实现error接口
func (e *AppError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("[%s] %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

// Unwrap 支持errors.Unwrap
func (e *AppError) Unwrap() error {
	return e.Cause
}

// NewAppError 创建新的应用程序错误
func NewAppError(code ErrorCode, message string, cause error) *AppError {
	return &AppError{
		Code:    code,
		Message: message,
		Cause:   cause,
	}
}

// WrapError 包装错误
func WrapError(code ErrorCode, message string, err error) error {
	return NewAppError(code, message, err)
}
