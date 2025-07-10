package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// SSHConnectRequest SSH连接请求
type SSHConnectRequest struct {
	Host       string `json:"host" binding:"required"`
	Port       int    `json:"port"`
	Username   string `json:"username" binding:"required"`
	Password   string `json:"password"`
	PrivateKey string `json:"private_key"`
	KeyPath    string `json:"key_path"`
	Timeout    int    `json:"timeout"` // 超时时间（秒）
}

// SSHExecuteRequest SSH命令执行请求
type SSHExecuteRequest struct {
	Command string `json:"command" binding:"required"`
	Timeout int    `json:"timeout"` // 超时时间（秒）
}

// SSHFileRequest SSH文件操作请求
type SSHFileRequest struct {
	LocalPath  string `json:"local_path"`
	RemotePath string `json:"remote_path" binding:"required"`
}

// SSHListRequest SSH目录列表请求
type SSHListRequest struct {
	Path string `json:"path" binding:"required"`
}

// initSSHRoutes 初始化SSH路由
func initSSHRoutes(router *gin.Engine) {
	sshGroup := router.Group("/ssh")
	{
		// 连接管理
		sshGroup.POST("/connect", handleSSHConnect)
		sshGroup.GET("/connections", handleSSHListConnections)
		sshGroup.GET("/connections/:id", handleSSHGetConnection)
		sshGroup.DELETE("/connections/:id", handleSSHDisconnect)

		// 命令执行
		sshGroup.POST("/execute/:id", handleSSHExecute)
		sshGroup.POST("/execute-stream/:id", handleSSHExecuteStream)

		// 文件操作
		sshGroup.POST("/upload/:id", handleSSHUpload)
		sshGroup.POST("/download/:id", handleSSHDownload)
		sshGroup.POST("/list/:id", handleSSHListDirectory)

		// 工具接口
		sshGroup.POST("/generate-key", handleSSHGenerateKey)
		sshGroup.GET("/health", handleSSHHealth)
	}

	// 启动连接清理协程
	go startSSHConnectionCleanup()
}

// handleSSHConnect 处理SSH连接请求
func handleSSHConnect(c *gin.Context) {
	var req SSHConnectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "请求参数错误",
			"details": err.Error(),
		})
		return
	}

	// 设置默认值
	if req.Port == 0 {
		req.Port = 22
	}
	if req.Timeout == 0 {
		req.Timeout = 30
	}

	// 创建SSH客户端
	client := NewSSHClient(
		req.Host,
		req.Port,
		req.Username,
		req.Password,
		req.PrivateKey,
		req.KeyPath,
	)

	// 尝试连接
	if err := client.Connect(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "SSH连接失败",
			"details": err.Error(),
		})
		return
	}

	// 生成连接ID
	connectionID := uuid.New().String()

	// 添加到管理器
	GetSSHManager().AddConnection(connectionID, client)

	c.JSON(http.StatusOK, gin.H{
		"connection_id": connectionID,
		"message":       "SSH连接成功",
		"info":          client.GetConnectionInfo(),
	})
}

// handleSSHListConnections 处理列出所有SSH连接请求
func handleSSHListConnections(c *gin.Context) {
	connections := GetSSHManager().ListConnections()
	c.JSON(http.StatusOK, gin.H{
		"connections": connections,
		"count":       len(connections),
	})
}

// handleSSHGetConnection 处理获取SSH连接信息请求
func handleSSHGetConnection(c *gin.Context) {
	connectionID := c.Param("id")
	client, exists := GetSSHManager().GetConnection(connectionID)
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "连接不存在",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"connection_id": connectionID,
		"info":          client.GetConnectionInfo(),
	})
}

// handleSSHDisconnect 处理SSH断开连接请求
func handleSSHDisconnect(c *gin.Context) {
	connectionID := c.Param("id")
	client, exists := GetSSHManager().GetConnection(connectionID)
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "连接不存在",
		})
		return
	}

	if err := client.Disconnect(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "断开连接失败",
			"details": err.Error(),
		})
		return
	}

	GetSSHManager().RemoveConnection(connectionID)

	c.JSON(http.StatusOK, gin.H{
		"message": "SSH连接已断开",
	})
}

// handleSSHExecute 处理SSH命令执行请求
func handleSSHExecute(c *gin.Context) {
	connectionID := c.Param("id")
	client, exists := GetSSHManager().GetConnection(connectionID)
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "连接不存在",
		})
		return
	}

	var req SSHExecuteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "请求参数错误",
			"details": err.Error(),
		})
		return
	}

	// 设置超时上下文
	timeout := time.Duration(req.Timeout) * time.Second
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// 执行命令
	result, err := client.ExecuteCommandWithContext(ctx, req.Command)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "命令执行失败",
			"details": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"connection_id": connectionID,
		"command":       req.Command,
		"result":        result,
	})
}

// handleSSHExecuteStream 处理SSH流式命令执行请求
func handleSSHExecuteStream(c *gin.Context) {
	connectionID := c.Param("id")
	client, exists := GetSSHManager().GetConnection(connectionID)
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "连接不存在",
		})
		return
	}

	var req SSHExecuteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "请求参数错误",
			"details": err.Error(),
		})
		return
	}

	// 设置响应头
	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.Header("Transfer-Encoding", "chunked")
	c.Status(http.StatusOK)

	// 创建输出通道
	outputChan := make(chan string, 100)

	// 启动命令执行
	go func() {
		defer close(outputChan)
		if err := client.ExecuteCommandStream(req.Command, outputChan); err != nil {
			outputChan <- fmt.Sprintf("执行错误: %v", err)
		}
	}()

	// 流式输出结果
	for output := range outputChan {
		if _, err := c.Writer.WriteString(output); err != nil {
			break
		}
		c.Writer.Flush()
	}
}

// handleSSHUpload 处理SSH文件上传请求
func handleSSHUpload(c *gin.Context) {
	connectionID := c.Param("id")
	client, exists := GetSSHManager().GetConnection(connectionID)
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "连接不存在",
		})
		return
	}

	// 获取上传的文件
	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "获取上传文件失败",
			"details": err.Error(),
		})
		return
	}

	// 获取远程路径
	remotePath := c.PostForm("remote_path")
	if remotePath == "" {
		remotePath = file.Filename
	}

	// 创建临时文件
	tempFile, err := os.CreateTemp("", "ssh_upload_*")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "创建临时文件失败",
			"details": err.Error(),
		})
		return
	}
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()

	// 保存上传的文件
	if err := c.SaveUploadedFile(file, tempFile.Name()); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "保存上传文件失败",
			"details": err.Error(),
		})
		return
	}

	// 上传到远程服务器
	if err := client.UploadFile(tempFile.Name(), remotePath); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "文件上传失败",
			"details": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"connection_id": connectionID,
		"message":       "文件上传成功",
		"remote_path":   remotePath,
		"file_size":     file.Size,
	})
}

// handleSSHDownload 处理SSH文件下载请求
func handleSSHDownload(c *gin.Context) {
	connectionID := c.Param("id")
	client, exists := GetSSHManager().GetConnection(connectionID)
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "连接不存在",
		})
		return
	}

	var req SSHFileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "请求参数错误",
			"details": err.Error(),
		})
		return
	}

	// 创建临时文件
	tempFile, err := os.CreateTemp("", "ssh_download_*")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "创建临时文件失败",
			"details": err.Error(),
		})
		return
	}
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()

	// 下载文件
	if err := client.DownloadFile(req.RemotePath, tempFile.Name()); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "文件下载失败",
			"details": err.Error(),
		})
		return
	}

	// 获取文件信息
	fileInfo, err := os.Stat(tempFile.Name())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "获取文件信息失败",
			"details": err.Error(),
		})
		return
	}

	// 设置响应头
	filename := filepath.Base(req.RemotePath)
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Length", strconv.FormatInt(fileInfo.Size(), 10))

	// 发送文件内容
	file, err := os.Open(tempFile.Name())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "打开文件失败",
			"details": err.Error(),
		})
		return
	}
	defer file.Close()

	io.Copy(c.Writer, file)
}

// handleSSHListDirectory 处理SSH目录列表请求
func handleSSHListDirectory(c *gin.Context) {
	connectionID := c.Param("id")
	client, exists := GetSSHManager().GetConnection(connectionID)
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "连接不存在",
		})
		return
	}

	var req SSHListRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "请求参数错误",
			"details": err.Error(),
		})
		return
	}

	// 列出目录内容
	files, err := client.ListDirectory(req.Path)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "列出目录失败",
			"details": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"connection_id": connectionID,
		"path":          req.Path,
		"files":         files,
		"count":         len(files),
	})
}

// handleSSHGenerateKey 处理SSH密钥生成请求
func handleSSHGenerateKey(c *gin.Context) {
	bits := 2048
	if bitsStr := c.PostForm("bits"); bitsStr != "" {
		if b, err := strconv.Atoi(bitsStr); err == nil && b > 0 {
			bits = b
		}
	}

	privateKey, publicKey, err := GenerateKeyPair(bits)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "生成密钥失败",
			"details": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"private_key": privateKey,
		"public_key":  strings.TrimSpace(publicKey),
		"bits":        bits,
	})
}

// handleSSHHealth 处理SSH健康检查请求
func handleSSHHealth(c *gin.Context) {
	connections := GetSSHManager().ListConnections()
	activeConnections := 0
	for _, info := range connections {
		if info.Connected {
			activeConnections++
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"status":             "healthy",
		"total_connections":  len(connections),
		"active_connections": activeConnections,
		"timestamp":          time.Now().Unix(),
	})
}

// startSSHConnectionCleanup 启动SSH连接清理协程
func startSSHConnectionCleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		GetSSHManager().CleanupInactiveConnections(30 * time.Minute)
	}
}
