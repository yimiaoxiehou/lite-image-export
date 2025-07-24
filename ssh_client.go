package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// SSHClient SSH客户端结构体
type SSHClient struct {
	Host         string
	Port         int
	Username     string
	Password     string
	PrivateKey   string
	KeyPath      string
	Timeout      time.Duration
	client       *ssh.Client
	session      *ssh.Session
	mu           sync.RWMutex
	connected    bool
	lastActivity time.Time
}

// SSHCommandResult 命令执行结果
type SSHCommandResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	Duration string `json:"duration"`
	Error    string `json:"error,omitempty"`
}

// SSHConnectionInfo 连接信息
type SSHConnectionInfo struct {
	Host         string    `json:"host"`
	Port         int       `json:"port"`
	Username     string    `json:"username"`
	Connected    bool      `json:"connected"`
	LastActivity time.Time `json:"last_activity"`
	SessionCount int       `json:"session_count"`
}

// SSHManager SSH连接管理器
type SSHManager struct {
	connections map[string]*SSHClient
	mu          sync.RWMutex
}

var globalSSHManager = &SSHManager{
	connections: make(map[string]*SSHClient),
}

// NewSSHClient 创建新的SSH客户端
func NewSSHClient(host string, port int, username, password, privateKey, keyPath string) *SSHClient {
	timeout := 30 * time.Second
	if port == 0 {
		port = 22
	}

	return &SSHClient{
		Host:         host,
		Port:         port,
		Username:     username,
		Password:     password,
		PrivateKey:   privateKey,
		KeyPath:      keyPath,
		Timeout:      timeout,
		connected:    false,
		lastActivity: time.Now(),
	}
}

// Connect 建立SSH连接
func (s *SSHClient) Connect() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.connected && s.client != nil {
		return nil
	}

	config := &ssh.ClientConfig{
		User: s.Username,
		Auth: []ssh.AuthMethod{},
		HostKeyCallback: func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			return nil // 跳过主机密钥验证
		},
		Timeout: s.Timeout,
	}

	// 添加认证方法
	if s.Password != "" {
		config.Auth = append(config.Auth, ssh.Password(s.Password))
	}

	if s.PrivateKey != "" {
		signer, err := s.parsePrivateKey([]byte(s.PrivateKey))
		if err != nil {
			return WrapError(ErrCodeAuthError, "解析私钥失败", err)
		}
		config.Auth = append(config.Auth, ssh.PublicKeys(signer))
	}

	if s.KeyPath != "" {
		signer, err := s.loadPrivateKeyFromFile(s.KeyPath)
		if err != nil {
			return WrapError(ErrCodeAuthError, "加载私钥文件失败", err)
		}
		config.Auth = append(config.Auth, ssh.PublicKeys(signer))
	}

	// 建立连接
	client, err := ssh.Dial("tcp", fmt.Sprintf("%s:%d", s.Host, s.Port), config)
	if err != nil {
		return WrapError(ErrCodeNetworkError, "SSH连接失败", err)
	}

	s.client = client
	s.connected = true
	s.lastActivity = time.Now()

	LogInfo("SSH连接成功: %s@%s:%d", s.Username, s.Host, s.Port)
	return nil
}

// Disconnect 断开SSH连接
func (s *SSHClient) Disconnect() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.session != nil {
		s.session.Close()
		s.session = nil
	}

	if s.client != nil {
		err := s.client.Close()
		s.client = nil
		s.connected = false
		return err
	}

	return nil
}

// ExecuteCommand 执行命令
func (s *SSHClient) ExecuteCommand(command string) (*SSHCommandResult, error) {
	startTime := time.Now()

	// 确保连接存在
	if err := s.Connect(); err != nil {
		return nil, WrapError(ErrCodeNetworkError, "连接失败", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// 创建会话
	session, err := s.client.NewSession()
	if err != nil {
		return nil, WrapError(ErrCodeNetworkError, "创建会话失败", err)
	}
	defer session.Close()

	// 设置输出缓冲区
	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	// 执行命令
	err = session.Run(command)
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*ssh.ExitError); ok {
			exitCode = exitErr.ExitStatus()
		} else {
			return &SSHCommandResult{
				Stdout:   stdout.String(),
				Stderr:   stderr.String(),
				ExitCode: -1,
				Duration: time.Since(startTime).String(),
				Error:    err.Error(),
			}, nil
		}
	}

	s.lastActivity = time.Now()

	return &SSHCommandResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
		Duration: time.Since(startTime).String(),
	}, nil
}

// ExecuteCommandWithContext 带上下文的命令执行
func (s *SSHClient) ExecuteCommandWithContext(ctx context.Context, command string) (*SSHCommandResult, error) {
	// 创建带超时的上下文
	if ctx == nil {
		ctx = context.Background()
	}

	// 使用通道来处理超时
	resultChan := make(chan *SSHCommandResult, 1)
	errChan := make(chan error, 1)

	go func() {
		result, err := s.ExecuteCommand(command)
		if err != nil {
			errChan <- err
			return
		}
		resultChan <- result
	}()

	select {
	case result := <-resultChan:
		return result, nil
	case err := <-errChan:
		return nil, err
	case <-ctx.Done():
		return nil, WrapError(ErrCodeNetworkError, "命令执行超时", ctx.Err())
	}
}

// ExecuteCommandStream 流式执行命令
func (s *SSHClient) ExecuteCommandStream(command string, outputChan chan<- string) error {
	// 确保连接存在
	if err := s.Connect(); err != nil {
		return WrapError(ErrCodeNetworkError, "连接失败", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// 创建会话
	session, err := s.client.NewSession()
	if err != nil {
		return WrapError(ErrCodeNetworkError, "创建会话失败", err)
	}
	defer session.Close()

	// 设置输出管道
	stdout, err := session.StdoutPipe()
	if err != nil {
		return WrapError(ErrCodeNetworkError, "获取stdout管道失败", err)
	}

	stderr, err := session.StderrPipe()
	if err != nil {
		return WrapError(ErrCodeNetworkError, "获取stderr管道失败", err)
	}

	// 启动命令
	if err := session.Start(command); err != nil {
		return WrapError(ErrCodeNetworkError, "启动命令失败", err)
	}

	// 读取输出
	go func() {
		defer close(outputChan)
		buffer := make([]byte, 1024)
		for {
			n, err := stdout.Read(buffer)
			if n > 0 {
				outputChan <- string(buffer[:n])
			}
			if err != nil {
				if err != io.EOF {
					outputChan <- fmt.Sprintf("读取错误: %v", err)
				}
				break
			}
		}
	}()

	// 读取错误输出
	go func() {
		buffer := make([]byte, 1024)
		for {
			n, err := stderr.Read(buffer)
			if n > 0 {
				outputChan <- fmt.Sprintf("STDERR: %s", string(buffer[:n]))
			}
			if err != nil {
				break
			}
		}
	}()

	// 等待命令完成
	err = session.Wait()
	s.lastActivity = time.Now()

	if err != nil {
		if exitErr, ok := err.(*ssh.ExitError); ok {
			outputChan <- fmt.Sprintf("命令退出码: %d", exitErr.ExitStatus())
		} else {
			outputChan <- fmt.Sprintf("命令执行错误: %v", err)
		}
	}

	return nil
}

// ListDirectory 列出目录内容
func (s *SSHClient) ListDirectory(path string) ([]string, error) {
	command := fmt.Sprintf("ls -la %s", path)
	result, err := s.ExecuteCommand(command)
	if err != nil {
		return nil, err
	}

	if result.ExitCode != 0 {
		return nil, WrapError(ErrCodeNetworkError, "列出目录失败", errors.New(result.Stderr))
	}

	lines := strings.Split(strings.TrimSpace(result.Stdout), "\n")
	return lines, nil
}

// UploadFile 上传文件
func (s *SSHClient) UploadFile(localPath, remotePath string) error {
	// 读取本地文件
	data, err := os.ReadFile(localPath)
	if err != nil {
		return WrapError(ErrCodeFileOperation, "读取本地文件失败", err)
	}

	// 创建远程目录
	remoteDir := filepath.Dir(remotePath)
	if remoteDir != "." {
		_, err = s.ExecuteCommand(fmt.Sprintf("mkdir -p %s", remoteDir))
		if err != nil {
			return WrapError(ErrCodeNetworkError, "创建远程目录失败", err)
		}
	}

	// 使用scp上传文件
	session, err := s.client.NewSession()
	if err != nil {
		return WrapError(ErrCodeNetworkError, "创建会话失败", err)
	}
	defer session.Close()

	go func() {
		w, _ := session.StdinPipe()
		defer w.Close()
		fmt.Fprintf(w, "C0644 %d %s\n", len(data), filepath.Base(remotePath))
		w.Write(data)
		fmt.Fprint(w, "\x00")
	}()

	if err := session.Run(fmt.Sprintf("scp -t %s", remotePath)); err != nil {
		return WrapError(ErrCodeNetworkError, "上传文件失败", err)
	}

	return nil
}

// DownloadFile 下载文件
func (s *SSHClient) DownloadFile(remotePath, localPath string) error {
	session, err := s.client.NewSession()
	if err != nil {
		return WrapError(ErrCodeNetworkError, "创建会话失败", err)
	}
	defer session.Close()

	var buffer bytes.Buffer
	session.Stdout = &buffer

	if err := session.Run(fmt.Sprintf("cat %s", remotePath)); err != nil {
		return WrapError(ErrCodeNetworkError, "读取远程文件失败", err)
	}

	if err := os.WriteFile(localPath, buffer.Bytes(), 0644); err != nil {
		return WrapError(ErrCodeFileOperation, "写入本地文件失败", err)
	}

	return nil
}

// GetConnectionInfo 获取连接信息
func (s *SSHClient) GetConnectionInfo() *SSHConnectionInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return &SSHConnectionInfo{
		Host:         s.Host,
		Port:         s.Port,
		Username:     s.Username,
		Connected:    s.connected,
		LastActivity: s.lastActivity,
		SessionCount: 0, // TODO: 实现会话计数
	}
}

// parsePrivateKey 解析私钥
func (s *SSHClient) parsePrivateKey(privateKeyBytes []byte) (ssh.Signer, error) {
	block, _ := pem.Decode(privateKeyBytes)
	if block == nil {
		return nil, WrapError(ErrCodeAuthError, "无效的私钥格式", nil)
	}

	var signer ssh.Signer
	var err error

	switch block.Type {
	case "RSA PRIVATE KEY":
		signer, err = ssh.ParsePrivateKey(privateKeyBytes)
	case "OPENSSH PRIVATE KEY":
		signer, err = ssh.ParsePrivateKey(privateKeyBytes)
	default:
		return nil, WrapError(ErrCodeAuthError, fmt.Sprintf("不支持的私钥类型: %s", block.Type), nil)
	}

	if err != nil {
		return nil, WrapError(ErrCodeAuthError, "解析私钥失败", err)
	}

	return signer, nil
}

// loadPrivateKeyFromFile 从文件加载私钥
func (s *SSHClient) loadPrivateKeyFromFile(keyPath string) (ssh.Signer, error) {
	privateKeyBytes, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, WrapError(ErrCodeFileOperation, "读取私钥文件失败", err)
	}

	return s.parsePrivateKey(privateKeyBytes)
}

// GenerateKeyPair 生成SSH密钥对
func GenerateKeyPair(bits int) (string, string, error) {
	if bits == 0 {
		bits = 2048
	}

	privateKey, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		return "", "", WrapError(ErrCodeAuthError, "生成私钥失败", err)
	}

	// 生成私钥PEM格式
	privateKeyPEM := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	}

	privateKeyBytes := pem.EncodeToMemory(privateKeyPEM)

	// 生成公钥
	publicKey, err := ssh.NewPublicKey(&privateKey.PublicKey)
	if err != nil {
		return "", "", WrapError(ErrCodeAuthError, "生成公钥失败", err)
	}

	publicKeyBytes := ssh.MarshalAuthorizedKey(publicKey)

	return string(privateKeyBytes), string(publicKeyBytes), nil
}

// GetSSHManager 获取全局SSH管理器
func GetSSHManager() *SSHManager {
	return globalSSHManager
}

// AddConnection 添加SSH连接
func (sm *SSHManager) AddConnection(id string, client *SSHClient) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.connections[id] = client
}

// GetConnection 获取SSH连接
func (sm *SSHManager) GetConnection(id string) (*SSHClient, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	client, exists := sm.connections[id]
	return client, exists
}

// RemoveConnection 移除SSH连接
func (sm *SSHManager) RemoveConnection(id string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if client, exists := sm.connections[id]; exists {
		client.Disconnect()
		delete(sm.connections, id)
	}
}

// ListConnections 列出所有连接
func (sm *SSHManager) ListConnections() map[string]*SSHConnectionInfo {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	result := make(map[string]*SSHConnectionInfo)
	for id, client := range sm.connections {
		result[id] = client.GetConnectionInfo()
	}
	return result
}

// CleanupInactiveConnections 清理非活跃连接
func (sm *SSHManager) CleanupInactiveConnections(timeout time.Duration) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	now := time.Now()
	for id, client := range sm.connections {
		if now.Sub(client.lastActivity) > timeout {
			client.Disconnect()
			delete(sm.connections, id)
			LogInfo("清理非活跃SSH连接: %s", id)
		}
	}
}
