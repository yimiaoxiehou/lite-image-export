package main

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/partial"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/gookit/goutil/mathutil"
	"github.com/gookit/goutil/x/fmtutil"
)

// 全局配置变量
var globalConfig *Config

// Validate 验证配置
func (c *Config) Validate() error {
	return validateConfig(c)
}

// parseCommandLineArgs 解析命令行参数
func parseCommandLineArgs() (string, string) {
	image := flag.String("image", "docker.utpf.cn/platform/edgeai-videostreamserver:V1.00.02.03_Omnisky", "镜像名称:TAG")
	configPath := flag.String("config", "config.json", "配置文件路径")
	flag.Parse()
	return *image, *configPath
}

// initializeApplication 初始化应用程序（配置加载、验证、系统初始化）
func initializeApplication(configPath string) error {
	// 加载配置
	var err error
	globalConfig, err = LoadConfig(configPath)
	if err != nil {
		// 临时使用标准log，因为logger还未初始化
		LogWarn("加载配置文件失败，使用默认配置: %v", err)
		globalConfig = DefaultConfig()
	}

	// 初始化日志系统
	logLevel := ParseLogLevel(globalConfig.Logging.Level)
	InitLogger(logLevel)
	LogInfo("日志系统初始化完成，级别: %s", globalConfig.Logging.Level)

	// 验证配置
	if err := globalConfig.Validate(); err != nil {
		return WrapError(ErrCodeConfigValidate, "配置验证失败", err)
	}
	LogInfo("配置验证通过")

	// 初始化HTTP客户端
	initHTTPClients()
	LogInfo("HTTP客户端初始化完成")

	// 初始化镜像流式下载器
	initImageStreamer()
	LogInfo("镜像流式下载器初始化完成")

	return nil
}

// prepareImageProcessing 准备镜像处理（解析镜像引用、获取描述符、创建输出目录）
func prepareImageProcessing(imageName string) (*remote.Descriptor, string, error) {
	LogInfo("开始准备镜像处理: %s", imageName)

	imageRef, err := name.ParseReference(imageName)
	if err != nil {
		return nil, "", WrapError(ErrCodeImageParse, "解析镜像名称失败", err)
	}
	LogDebug("镜像引用解析成功: %s", imageRef.String())

	desc, err := remote.Get(imageRef, globalImageStreamer.remoteOptions...)
	if err != nil {
		return nil, "", WrapError(ErrCodeNetworkError, "获取镜像 manifest 失败", err)
	}
	LogDebug("获取镜像 manifest 成功，媒体类型: %s", desc.MediaType)

	// 创建输出目录
	outputDir := "output"
	if err = os.MkdirAll(outputDir, 0755); err != nil {
		return nil, "", WrapError(ErrCodeFileOperation, "创建输出目录失败", err)
	}
	LogInfo("输出目录创建成功: %s", outputDir)

	return desc, outputDir, nil
}

// processImage 处理镜像（根据类型选择处理方式）
func processImage(desc *remote.Descriptor, existLayers []string, outputDir string, options *StreamOptions, imageRef string) error {
	LogInfo("开始处理镜像，媒体类型: %s", desc.MediaType)

	switch desc.MediaType {
	case types.OCIImageIndex, types.DockerManifestList:
		LogInfo("处理多平台镜像，选择平台: %s", options.Platform)
		img, err := selectPlatformImage(desc, options)
		if err != nil {
			return WrapError(ErrCodeImageParse, "选择平台镜像失败", err)
		}
		return streamImageLayers(img, existLayers, outputDir, options, imageRef)
	default:
		LogInfo("处理单平台镜像")
		img, err := desc.Image()
		if err != nil {
			return WrapError(ErrCodeImageParse, "获取镜像失败", err)
		}
		return streamImageLayers(img, existLayers, outputDir, options, imageRef)
	}
}

// finalizeImageExport 完成镜像导出（组装tar文件）
func finalizeImageExport(outputDir string) error {
	if err := assembleImageTar(outputDir, "output.tar"); err != nil {
		return WrapError(ErrCodeTarAssembly, "组装镜像 tar 文件失败", err)
	}

	LogInfo("镜像 tar 文件已成功创建: output.tar")
	return nil
}

func main() {
	imageName, configPath := parseCommandLineArgs()

	if err := initializeApplication(configPath); err != nil {
		LogFatal("应用程序初始化失败: %v", err)
	}
	existLayers, err := loadImageLayers()
	if err != nil {
		LogFatal("加载镜像层失败: %v", err)
	}

	desc, outputDir, err := prepareImageProcessing(imageName)
	if err != nil {
		LogFatal("准备镜像处理失败: %v", err)
	}

	options := &StreamOptions{
		Compression:         true,
		Platform:            "linux/amd64",
		UseCompressedLayers: true,
	}

	if err := processImage(desc, existLayers, outputDir, options, imageName); err != nil {
		LogFatal("处理镜像失败: %v", err)
	}

	if err := finalizeImageExport(outputDir); err != nil {
		LogFatal("完成镜像导出失败: %v", err)
	}
}

const maxRetries = 5

// parseAuthHeader parses the Www-Authenticate header.
func parseAuthHeader(header string) map[string]string {
	authHeader := make(map[string]string)
	parts := strings.SplitN(header, " ", 2)
	if len(parts) == 2 && parts[0] == "Bearer" {
		params := strings.Split(parts[1], ",")
		for _, param := range params {
			kv := strings.SplitN(param, "=", 2)
			if len(kv) == 2 {
				authHeader[kv[0]] = strings.Trim(kv[1], "\"")
			}
		}
	}
	return authHeader
}

// getFileSize 获取文件大小，如果文件不存在返回0
func getFileSize(filePath string) int64 {
	if stat, err := os.Stat(filePath); err == nil {
		return stat.Size()
	}
	return 0
}

// validateFileIntegrity 验证文件完整性
func validateFileIntegrity(filePath string, expectedDigest string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return WrapError(ErrCodeFileOperation, "无法打开文件进行校验", err)
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return WrapError(ErrCodeFileOperation, "校验文件时计算哈希失败", err)
	}

	actualDigest := "sha256:" + hex.EncodeToString(hasher.Sum(nil))
	if actualDigest != expectedDigest {
		return WrapError(ErrCodeChecksumError, fmt.Sprintf("校验失败: 预期摘要 %s, 实际摘要 %s", expectedDigest, actualDigest), nil)
	}

	return nil
}

// downloadLayerWithRetry 带重试机制的层下载函数
func downloadLayerWithRetry(imageRef name.Reference, layerURL string, destPath string, size int64, expectedDigest string) error {
	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	// 检查文件是否已存在且完整
	localSize := getFileSize(destPath)
	if localSize > 0 && size > 0 && localSize == size {
		// 验证文件完整性
		if expectedDigest != "" {
			if err := validateFileIntegrity(destPath, expectedDigest); err != nil {
				LogWarn("文件存在但校验失败，重新下载: %s, 错误: %v", destPath, err)
				if err := os.Remove(destPath); err != nil {
					LogWarn("删除校验失败的文件时出错: %v", err)
				}
			} else {
				LogInfo("文件已存在且校验通过: %s", destPath)
				return nil
			}
		} else {
			LogInfo("文件已存在且大小匹配: %s", destPath)
			return nil
		}
	}

	// Get auth token once with better error handling.
	token, err := func() (string, error) {
		authURL := fmt.Sprintf("https://%s/v2/", imageRef.Context().RegistryStr())
		authClient := &http.Client{Timeout: 10 * time.Second}
		resp, err := authClient.Get(authURL)
		if err != nil {
			if resp != nil {
				resp.Body.Close()
			}
			return "", WrapError(ErrCodeNetworkError, "认证预请求失败", err)
		}
		defer func() {
			if resp != nil && resp.Body != nil {
				resp.Body.Close()
			}
		}()

		if resp.StatusCode == http.StatusUnauthorized {
			authHeader := parseAuthHeader(resp.Header.Get("Www-Authenticate"))
			realm := authHeader["realm"]
			service := authHeader["service"]
			scope := authHeader["scope"]
			if scope == "" {
				scope = fmt.Sprintf("repository:%s:pull", imageRef.Context().RepositoryStr())
			}

			tokenURL := fmt.Sprintf("%s?service=%s&scope=%s", realm, service, scope)
			tokenResp, err := authClient.Get(tokenURL)
			if err != nil {
				return "", WrapError(ErrCodeNetworkError, "获取 token 请求失败", err)
			}
			defer tokenResp.Body.Close()

			if tokenResp.StatusCode != http.StatusOK {
				bodyBytes, _ := io.ReadAll(tokenResp.Body)
				return "", WrapError(ErrCodeAuthError, fmt.Sprintf("获取 token 失败，状态码: %d, 响应: %s", tokenResp.StatusCode, string(bodyBytes)), nil)
			}

			var tokenData struct {
				Token string `json:"token"`
			}
			if err := json.NewDecoder(tokenResp.Body).Decode(&tokenData); err != nil {
				return "", WrapError(ErrCodeAuthError, "解析 token 失败", err)
			}
			return tokenData.Token, nil
		} else if resp.StatusCode != http.StatusOK {
			return "", WrapError(ErrCodeAuthError, fmt.Sprintf("认证请求失败，状态码: %d", resp.StatusCode), nil)
		}
		return "", nil // No auth needed
	}()

	if err != nil {
		return WrapError(ErrCodeAuthError, "认证失败", err)
	}

	for {
		currentSize := getFileSize(destPath)

		// 检查文件是否已完成下载并验证完整性
		if size > 0 && currentSize == size {
			LogInfo("层 %s 已下载完成，开始校验...", destPath)
			if err := validateFileIntegrity(destPath, expectedDigest); err != nil {
				LogError("校验失败: %v", err)
				// 校验失败，删除文件并重新下载
				if err := os.Remove(destPath); err != nil {
					LogWarn("删除校验失败的文件 %s 时出错: %v", destPath, err)
				}
				continue // 继续外层循环重新下载
			}
			LogInfo("层 %s 校验成功", destPath)
			return nil
		}

		// 检查文件大小是否超过预期
		if size > 0 && currentSize > size {
			LogWarn("文件 %s 大小 (%d) 超过预期 (%d)，截断并重新开始", destPath, currentSize, size)
			if err := os.Truncate(destPath, 0); err != nil {
				return WrapError(ErrCodeFileOperation, "无法截断文件", err)
			}
			currentSize = 0
		}

		// 重试下载逻辑
		var lastErr error
		var chunkDownloaded bool
		for retries := 0; retries < maxRetries; {
			// 重新获取当前文件大小
			currentSize = getFileSize(destPath)

			req, err := http.NewRequest("GET", layerURL, nil)
			if err != nil {
				return WrapError(ErrCodeNetworkError, "创建请求失败", err)
			}

			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			if currentSize > 0 {
				req.Header.Set("Range", fmt.Sprintf("bytes=%d-", currentSize))
				LogDebug("断点续传 layer(%s) 进度: %.2f%%", layerURL, mathutil.Percent(int(currentSize), int(size)))
			} else {
				retries++
			}

			resp, err := client.Do(req)
			if err != nil {
				lastErr = WrapError(ErrCodeNetworkError, "请求失败", err)
				LogWarn("下载层 %s 时出错 (尝试 %d/%d): %v", destPath, retries+1, maxRetries, lastErr)
				time.Sleep(time.Duration(retries+1) * 2 * time.Second) // 递增延迟
				continue
			}

			// 使用匿名函数确保响应体正确关闭，避免defer在循环中的问题
			func() {
				defer func() {
					if resp != nil && resp.Body != nil {
						resp.Body.Close()
					}
				}()

				var file *os.File
				var openErr error

				switch resp.StatusCode {
				case http.StatusPartialContent:
					file, openErr = os.OpenFile(destPath, os.O_WRONLY|os.O_APPEND, 0644)
				case http.StatusOK:
					if currentSize > 0 {
						LogWarn("服务器不支持 Range 请求，将从头下载")
						currentSize = 0 // Reset progress
					}
					file, openErr = os.OpenFile(destPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
				default:
					lastErr = WrapError(ErrCodeNetworkError, fmt.Sprintf("下载失败，状态码: %d", resp.StatusCode), nil)
					LogWarn("下载层 %s 时出错 (尝试 %d/%d): %v", destPath, retries+1, maxRetries, lastErr)
					time.Sleep(time.Duration(retries+1) * 2 * time.Second)
					return // 从匿名函数返回，继续外层循环
				}

				if openErr != nil {
					lastErr = WrapError(ErrCodeFileOperation, fmt.Sprintf("打开目标文件 %s 失败", destPath), openErr)
					LogWarn("下载层 %s 时出错 (尝试 %d/%d): %v", destPath, retries+1, maxRetries, lastErr)
					time.Sleep(time.Duration(retries+1) * 2 * time.Second)
					return // 从匿名函数返回，继续外层循环
				}

				// 确保文件在使用后关闭
				n, copyErr := io.Copy(file, resp.Body)
				file.Close()

				if copyErr != nil {
					lastErr = WrapError(ErrCodeFileOperation, "复制层数据时出错", copyErr)
					LogWarn("下载层 %s 时出错 (尝试 %d/%d): %v，本次写入 %d 字节", destPath, retries+1, maxRetries, lastErr, n)
					time.Sleep(time.Duration(retries+1) * 2 * time.Second)
					return // 从匿名函数返回，继续外层循环
				}

				LogDebug("成功写入 %d 字节到 %s", n, destPath)
				chunkDownloaded = true
			}() // 调用匿名函数

			if chunkDownloaded {
				break // Exit retry loop on success
			}
		}

		if !chunkDownloaded {
			return WrapError(ErrCodeImageDownload, fmt.Sprintf("经过 %d 次尝试后，层块下载失败", maxRetries), lastErr)
		}
	}
}

func loadImageLayers() ([]string, error) {
	LogInfo("开始加载现有镜像层")
	layers := []string{}

	// 使用全局配置中的SSH连接信息
	sshClient := NewSSHClient(
		globalConfig.SSH.Host,
		globalConfig.SSH.Port,
		globalConfig.SSH.Username,
		globalConfig.SSH.Password,
		globalConfig.SSH.KeyFile,
		"", // passphrase留空，如需要可在config中添加
	)
	LogDebug("尝试连接SSH: %s:%d", globalConfig.SSH.Host, globalConfig.SSH.Port)
	if err := sshClient.Connect(); err != nil {
		return nil, WrapError(ErrCodeNetworkError, "SSH连接失败", err)
	}
	LogInfo("SSH连接成功")

	defer sshClient.Disconnect()
	command := "cd `docker info | grep 'Docker Root Dir' | awk '{print $4}'` && ls ./image/overlay2/distribution/diffid-by-digest/sha256"
	LogDebug("执行SSH命令: %s", command)
	if result, err := sshClient.ExecuteCommand(command); err != nil {
		return nil, WrapError(ErrCodeNetworkError, "执行SSH命令失败", err)
	} else {
		layers = strings.Split(result.Stdout, "\n")
		LogInfo("成功获取现有镜像层，共 %d 个", len(layers))
	}

	return layers, nil
}

func streamImageLayers(img v1.Image, existLayers []string, outputDir string, options *StreamOptions, imageRef string) error {
	LogInfo("开始流式处理镜像层")

	configFile, err := img.ConfigFile()
	if err != nil {
		return WrapError(ErrCodeImageParse, "获取镜像配置失败", err)
	}
	LogDebug("成功获取镜像配置")

	layers, err := img.Layers()
	if err != nil {
		return WrapError(ErrCodeImageParse, "获取镜像层失败", err)
	}

	LogInfo("镜像包含 %d 层，将下载所有层以确保 Docker 兼容性", len(layers))
	
	// 为了确保生成的 tar 包完整且符合 Docker 标准，下载所有层
	// 这解决了 manifest.json 引用的层文件在 tar 包中缺失的问题
	return streamDockerFormatWithReturn(img, layers, configFile, imageRef, nil, nil, options, outputDir)
}

// streamDockerFormatWithReturn 生成Docker格式并返回manifest和repositories信息
func streamDockerFormatWithReturn(img v1.Image, layers []v1.Layer, configFile *v1.ConfigFile, imageRef string, manifestOut *map[string]interface{}, repositoriesOut *map[string]map[string]string, options *StreamOptions, outputDir string) error {
	LogInfo("开始生成Docker格式")

	configDigest, err := img.ConfigName()
	if err != nil {
		return WrapError(ErrCodeImageParse, "获取配置摘要失败", err)
	}

	configData, err := json.Marshal(configFile)
	if err != nil {
		return WrapError(ErrCodeImageParse, "序列化配置文件失败", err)
	}

	configPath := fmt.Sprintf("%s/%s.json", outputDir, configDigest.String())
	if err := os.WriteFile(configPath, configData, 0644); err != nil {
		return WrapError(ErrCodeFileOperation, "写入配置文件失败", err)
	}
	LogDebug("配置文件已写入: %s", configPath)

	// 记录所有层的摘要用于manifest
	layerDigests := make([]string, len(layers))
	for i, layer := range layers {
		digest, err := layer.Digest()
		if err != nil {
			return WrapError(ErrCodeImageParse, "获取层摘要失败", err)
		}
		layerDigests[i] = digest.String()
	}
	
	// 下载所有层
	for i, layer := range layers {
		if err = func() error {
			digest, err := layer.Digest()
			if err != nil {
				return WrapError(ErrCodeImageParse, "获取层摘要失败", err)
			}
			// digest 已在上面获取，这里直接使用

			var layerSize int64

			// 根据配置选择使用压缩层或未压缩层
			if options != nil && options.UseCompressedLayers {
				layerSize, err = layer.Size()
			} else {
				layerSize, err = partial.UncompressedSize(layer)
			}
			LogInfo("下载 layer %s 大小: %s", digest.String(), fmtutil.HumanSize(uint64(layerSize)))

			if err != nil {
				return WrapError(ErrCodeImageParse, "获取层大小失败", err)
			}

			layerPath := fmt.Sprintf("%s/%s.tar", outputDir, digest.String())

			// 构建层下载的 URL
			imageRefInfo, err := name.ParseReference(imageRef)
			if err != nil {
				return WrapError(ErrCodeImageParse, "解析镜像引用失败", err)
			}
			registryStr := imageRefInfo.Context().RegistryStr()
			repositoryStr := imageRefInfo.Context().RepositoryStr()
			layerURL := fmt.Sprintf("https://%s/v2/%s/blobs/%s", registryStr, repositoryStr, digest.String())
			LogDebug("构建层下载URL: %s", layerURL)

			// 调用优化后的下载函数，它会处理断点续传和校验
			if err := downloadLayerWithRetry(imageRefInfo, layerURL, layerPath, layerSize, digest.String()); err != nil {
				return WrapError(ErrCodeImageDownload, fmt.Sprintf("通过 REST 下载层 %s 失败", digest.String()), err)
			}

			return nil
		}(); err != nil {
			return err
		}

		LogInfo("已处理层 %d/%d", i+1, len(layers))
	}

	// 构建单个镜像的manifest信息
	singleManifest := map[string]interface{}{
		"Config":   configDigest.String() + ".json",
		"RepoTags": []string{imageRef},
		"Layers": func() []string {
			var layerFiles []string
			for _, digest := range layerDigests {
				layerFiles = append(layerFiles, digest+".tar")
			}
			return layerFiles
		}(),
	}

	// 构建repositories信息
	repositories := make(map[string]map[string]string)
	i := strings.LastIndex(imageRef, ":")
	if i > -1 {
		repoName := imageRef[:i]
		tag := imageRef[i+1:]
		repositories[repoName] = map[string]string{tag: configDigest.String()}
	}

	// 如果是批量下载，返回信息而不写入文件
	if manifestOut != nil && repositoriesOut != nil {
		*manifestOut = singleManifest
		*repositoriesOut = repositories
		return nil
	}

	// 单镜像下载，直接写入manifest.json
	manifest := []map[string]interface{}{singleManifest}

	manifestData, err := json.Marshal(manifest)
	if err != nil {
		return WrapError(ErrCodeFileOperation, "序列化manifest失败", err)
	}

	manifestPath := fmt.Sprintf("%s/manifest.json", outputDir)
	if err = os.WriteFile(manifestPath, manifestData, 0644); err != nil {
		return WrapError(ErrCodeFileOperation, "写入manifest.json失败", err)
	}
	LogInfo("manifest.json已写入: %s", manifestPath)

	// 写入repositories文件
	repositoriesData, err := json.Marshal(repositories)
	if err != nil {
		return WrapError(ErrCodeFileOperation, "序列化repositories失败", err)
	}

	repositoriesPath := fmt.Sprintf("%s/repositories", outputDir)
	if err := os.WriteFile(repositoriesPath, repositoriesData, 0644); err != nil {
		return WrapError(ErrCodeFileOperation, "写入repositories文件失败", err)
	}
	LogInfo("repositories文件已写入: %s", repositoriesPath)

	return nil
}

func assembleImageTar(outputDir, tarPath string) error {
	LogInfo("开始组装镜像tar文件: %s", tarPath)
	// 打开 manifest.json 文件
	manifestPath := filepath.Join(outputDir, "manifest.json")
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return WrapError(ErrCodeFileOperation, "读取 manifest.json 失败", err)
	}

	var manifest []map[string]interface{}
	if err = json.Unmarshal(manifestData, &manifest); err != nil {
		return WrapError(ErrCodeImageParse, "解析 manifest.json 失败", err)
	}

	if len(manifest) == 0 {
		return WrapError(ErrCodeImageParse, "manifest.json 为空或格式不正确", nil)
	}

	// 创建 tar 文件
	tarFile, err := os.Create(tarPath)
	if err != nil {
		return WrapError(ErrCodeFileOperation, "创建 tar 文件失败", err)
	}
	defer tarFile.Close()

	tw := tar.NewWriter(tarFile)
	defer tw.Close()

	// 添加 manifest.json 到 tar
	LogDebug("添加 manifest.json 到 tar")
	if err := addFileToTar(tw, manifestPath, "manifest.json"); err != nil {
		return WrapError(ErrCodeTarAssembly, "添加 manifest.json 到 tar 失败", err)
	}

	// 添加 repositories 文件到 tar
	repositoriesPath := filepath.Join(outputDir, "repositories")
	if _, err := os.Stat(repositoriesPath); err == nil {
		LogDebug("添加 repositories 到 tar")
		if err = addFileToTar(tw, repositoriesPath, "repositories"); err != nil {
			return WrapError(ErrCodeTarAssembly, "添加 repositories 到 tar 失败", err)
		}
	} else if !os.IsNotExist(err) {
		return WrapError(ErrCodeFileOperation, "检查 repositories 文件失败", err)
	}

	// 遍历 manifest 中的每个镜像配置
	for _, m := range manifest {
		// 添加配置文件
		if config, ok := m["Config"].(string); ok {
			configPath := filepath.Join(outputDir, config)
			LogDebug("添加配置文件 %s 到 tar", config)
			if err := addFileToTar(tw, configPath, config); err != nil {
				return WrapError(ErrCodeTarAssembly, fmt.Sprintf("添加配置文件 %s 到 tar 失败", config), err)
			}
		}

		// 添加层文件
		if layers, ok := m["Layers"].([]interface{}); ok {
			for _, layer := range layers {
				if layerPath, ok := layer.(string); ok {
					fullLayerPath := filepath.Join(outputDir, layerPath)
					
					LogDebug("添加层文件 %s 到 tar", layerPath)
					if err := addFileToTar(tw, fullLayerPath, layerPath); err != nil {
						return WrapError(ErrCodeTarAssembly, fmt.Sprintf("添加层文件 %s 到 tar 失败", layerPath), err)
					}
				}
			}
		}
	}

	LogInfo("tar文件组装完成: %s", tarPath)

	return nil
}

func addFileToTar(tw *tar.Writer, filePath, nameInTar string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return WrapError(ErrCodeFileOperation, fmt.Sprintf("打开文件 %s 失败", filePath), err)
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return WrapError(ErrCodeFileOperation, fmt.Sprintf("获取文件 %s 信息失败", filePath), err)
	}

	hdr := &tar.Header{
		Name: nameInTar,
		Mode: 0644,
		Size: stat.Size(),
	}

	if err := tw.WriteHeader(hdr); err != nil {
		return WrapError(ErrCodeTarAssembly, fmt.Sprintf("写入tar头信息失败: %s", nameInTar), err)
	}

	if _, err := io.Copy(tw, file); err != nil {
		return WrapError(ErrCodeTarAssembly, fmt.Sprintf("复制文件内容到tar失败: %s", nameInTar), err)
	}

	return nil
}

func selectPlatformImage(desc *remote.Descriptor, options *StreamOptions) (v1.Image, error) {
	LogInfo("开始选择平台镜像")
	index, err := desc.ImageIndex()
	if err != nil {
		return nil, WrapError(ErrCodeImageParse, "获取镜像索引失败", err)
	}

	manifest, err := index.IndexManifest()
	if err != nil {
		return nil, WrapError(ErrCodeImageParse, "获取索引清单失败", err)
	}
	LogDebug("镜像索引包含 %d 个清单", len(manifest.Manifests))

	// 选择合适的平台
	var selectedDesc *v1.Descriptor
	for _, m := range manifest.Manifests {
		if m.Platform == nil {
			continue
		}

		if options.Platform != "" {
			platformParts := strings.Split(options.Platform, "/")
			if len(platformParts) >= 2 {
				targetOS := platformParts[0]
				targetArch := platformParts[1]
				targetVariant := ""
				if len(platformParts) >= 3 {
					targetVariant = platformParts[2]
				}

				if m.Platform.OS == targetOS &&
					m.Platform.Architecture == targetArch &&
					m.Platform.Variant == targetVariant {
					selectedDesc = &m
					break
				}
			}
		} else if m.Platform.OS == "linux" && m.Platform.Architecture == "amd64" {
			selectedDesc = &m
			break
		}
	}

	if selectedDesc == nil && len(manifest.Manifests) > 0 {
		selectedDesc = &manifest.Manifests[0]
	}

	if selectedDesc == nil {
		return nil, WrapError(ErrCodeImageParse, "未找到合适的平台镜像", nil)
	}

	LogInfo("选择平台: %s/%s", selectedDesc.Platform.OS, selectedDesc.Platform.Architecture)
	img, err := index.Image(selectedDesc.Digest)
	if err != nil {
		return nil, WrapError(ErrCodeImageParse, "获取选中镜像失败", err)
	}

	LogInfo("平台镜像选择完成")
	return img, nil
}
