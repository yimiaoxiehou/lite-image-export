package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"slices"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/partial"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"golang.org/x/sync/errgroup"
)

func main() {
	image := "docker.utpf.cn/docker.io/library/redis"
	cacheDir := "./cache"
	if err := CacheImage(image, cacheDir, ImagePlatformAmd64, &authn.Basic{
		Username: "admin",
		Password: "Unitech@1998",
	}); err != nil {
		log.Fatal(err)
	}

	if err := CacheImage(image+":7", cacheDir, ImagePlatformAmd64, authn.Anonymous); err != nil {
		log.Fatal(err)
	}

	out, err := os.Create("redis.tar.gz")
	if err != nil {
		log.Fatal(err)
	}
	defer out.Close()

	err = ExportImage(ImagePlatformAmd64, out, cacheDir, image, image+":7")
	if err != nil {
		log.Fatal(err)
	}
}

func ExportImage(platform ImagePlatform, w io.Writer, cacheDir string, images ...string) error {
	writer, gzWriter := createTarGzWriter(w)
	defer gzWriter.Close()
	defer writer.Close()

	allManifest := make([]map[string]interface{}, 0)
	allRepositories := make(map[string]map[string]string)

	layers := make([]string, 0)
	for _, image := range images {
		if len(strings.Split(image, ":")) != 2 {
			image = image + ":latest"
		}

		manifestPath := filepath.Join(cacheDir, "manifest", platform.String(), url.QueryEscape(image)+".json")
		manifestData, err := os.ReadFile(manifestPath)
		if err != nil {
			return fmt.Errorf("读取manifest失败: %w", err)
		}
		var manifest map[string]interface{}
		if err = json.Unmarshal(manifestData, &manifest); err != nil {
			return fmt.Errorf("反序列化manifest失败: %w", err)
		}
		allManifest = append(allManifest, manifest)
		configPath := filepath.Join(cacheDir, "config", platform.String(), url.QueryEscape(image)+".json")
		configData, err := os.ReadFile(configPath)
		if err != nil {
			return err
		}

		configFileName := manifest["Config"].(string)
		if err = addFileToTar(writer, configFileName, configData); err != nil {
			return err
		}

		repositoriesPath := filepath.Join(cacheDir, "repositories", platform.String(), url.QueryEscape(image)+".json")
		repositoriesData, err := os.ReadFile(repositoriesPath)
		if err != nil {
			return err
		}

		var repositories map[string]map[string]string
		if err := json.Unmarshal(repositoriesData, &repositories); err != nil {
			return err
		}

		repoName, tag, _ := strings.Cut(image, ":")
		if tag == "" {
			tag = "latest"
		}
		allRepositories[repoName] = map[string]string{tag: image}

		for _, layer := range manifest["Layers"].([]interface{}) {
			layers = append(layers, layer.(string))
		}
	}

	if allManifestData, err := json.Marshal(allManifest); err != nil {
		return err
	} else {
		if err := addFileToTar(writer, "manifest.json", allManifestData); err != nil {
			return err
		}
	}

	if repositoriesData, err := json.Marshal(allRepositories); err != nil {
		return err
	} else {
		if err := addFileToTar(writer, "repositories", repositoriesData); err != nil {
			return err
		}
	}

	for _, layer := range layers {
		if layerData, err := os.ReadFile(filepath.Join(cacheDir, "layers", layer)); err != nil {
			return err
		} else {
			if err := addFileToTar(writer, layer, layerData); err != nil {
				return err
			}
		}
	}
	return nil
}

func addFileToTar(writer *tar.Writer, filename string, data []byte) error {
	header := &tar.Header{
		Name: filename,
		Size: int64(len(data)),
		Mode: int64(os.ModePerm),
	}
	if err := writer.WriteHeader(header); err != nil {
		return err
	}
	_, err := writer.Write(data)
	return err
}

func createTarGzWriter(w io.Writer) (*tar.Writer, *gzip.Writer) {
	gzWriter := gzip.NewWriter(w)
	return tar.NewWriter(gzWriter), gzWriter
}

func CacheImage(image, cacheDir string, platform ImagePlatform, auth authn.Authenticator) error {
	if len(strings.Split(image, ":")) != 2 {
		image = image + ":latest"
	}

	imageRef, err := name.ParseReference(image)
	if err != nil {
		return fmt.Errorf("解析镜像名称失败: %w", err)
	}

	desc, err := remote.Get(imageRef,
		// 认证
		remote.WithAuth(auth),
		// 代理客户端配置 - 适用于大文件传输
		remote.WithTransport(&http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			MaxIdleConns:          1000,
			MaxIdleConnsPerHost:   1000,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			ResponseHeaderTimeout: 300 * time.Second,
		}),
	)
	if err != nil {
		return fmt.Errorf("获取镜像描述失败: %w", err)
	}

	options := &StreamOptions{
		Compression:         true,
		Platform:            platform,
		UseCompressedLayers: true,
	}
	img, err := getImage(desc, options)
	if err != nil {
		return err
	}
	return streamImageLayers(img, cacheDir, options, image, 4)
}

func getImage(desc *remote.Descriptor, options *StreamOptions) (v1.Image, error) {
	switch desc.MediaType {
	case types.OCIImageIndex, types.DockerManifestList:
		return selectPlatformImage(desc, options)
	default:
		return desc.Image()
	}
}

func streamImageLayers(img v1.Image, cacheDir string, options *StreamOptions, imageRef string, concurrency int) error {
	if err := createCacheDirs(cacheDir, options.Platform.String()); err != nil {
		return err
	}

	layers, err := img.Layers()
	if err != nil {
		return fmt.Errorf("获取镜像层失败: %w", err)
	}

	log.Printf("镜像包含 %d 层", len(layers))
	return streamDockerFormatWithReturn(cacheDir, img, layers, imageRef, options, concurrency)
}

// streamDockerFormatWithReturn 生成Docker格式并返回manifest和repositories信息
func createCacheDirs(cacheDir, platform string) error {
	dirs := []string{
		"layers",
		"manifest/" + platform,
		"repositories/" + platform,
		"config/" + platform,
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(filepath.Join(cacheDir, dir), os.ModePerm); err != nil {
			return fmt.Errorf("创建目录 %s 失败: %w", dir, err)
		}
	}
	return nil
}

func streamDockerFormatWithReturn(cacheDir string, img v1.Image, layers []v1.Layer, imageRef string, options *StreamOptions, concurrency int) error {
	config, err := img.ConfigFile()
	if err != nil {
		return fmt.Errorf("获取镜像配置文件失败: %w", err)
	}

	configData, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("序列化镜像配置文件失败: %w", err)
	}

	configPath := filepath.Join(cacheDir, "config", options.Platform.String(), url.QueryEscape(imageRef)+".json")
	if err := os.WriteFile(configPath, configData, os.ModePerm); err != nil {
		return fmt.Errorf("写入镜像配置文件失败: %w", err)
	}

	configDigest, err := img.ConfigName()
	if err != nil {
		return fmt.Errorf("获取镜像配置哈希失败: %w", err)
	}

	layerDigests := make([]string, len(layers))
	for i, layer := range layers {
		digest, err := layer.Digest()
		if err != nil {
			return fmt.Errorf("获取层 %d 的哈希失败: %w", i, err)
		}
		layerDigests[i] = digest.String()
	}

	var g errgroup.Group
	g.SetLimit(concurrency)

	for i, layer := range layers {
		layer := layer
		i := i
		g.Go(func() error {
			if err := saveLayer(layer, cacheDir, options.UseCompressedLayers); err != nil {
				return fmt.Errorf("保存层 %s 失败: %w", layerDigests[i], err)
			}
			log.Printf("已处理层 %d/%d, digest: %s", i+1, len(layers), layerDigests[i])
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return fmt.Errorf("保存层时发生错误: %w", err)
	}

	return writeMetadata(cacheDir, imageRef, configDigest.String(), layerDigests, options.Platform.String())
}

func saveLayer(layer v1.Layer, cacheDir string, useCompressed bool) error {
	digest, err := layer.Digest()
	if err != nil {
		return err
	}
	digestStr := digest.String()

	var layerReader io.ReadCloser
	var layerSize int64

	if useCompressed {
		layerReader, err = layer.Compressed()
		layerSize, err = layer.Size()
	} else {
		layerReader, err = layer.Uncompressed()
		layerSize, err = partial.UncompressedSize(layer)
	}
	if err != nil {
		return err
	}
	defer layerReader.Close()

	layerPath := filepath.Join(cacheDir, "layers", digestStr+".tar")
	if info, err := os.Stat(layerPath); err == nil {
		if info.Size() == layerSize {
			return nil // 文件已存在且大小正确，跳过
		}
	} else if !os.IsNotExist(err) {
		return err // 其他Stat错误
	}

	layerFile, err := os.OpenFile(layerPath, os.O_CREATE|os.O_WRONLY, os.ModePerm)
	if err != nil {
		return err
	}
	defer layerFile.Close()

	_, err = io.Copy(layerFile, layerReader)
	return err
}

func layerPaths(digests []string) []string {
	paths := make([]string, len(digests))
	for i, digest := range digests {
		paths[i] = digest + ".tar"
	}
	return paths
}

func writeMetadata(cacheDir, imageRef, configDigest string, layerDigests []string, platform string) error {
	manifest := map[string]interface{}{
		"Config":   configDigest + ".json",
		"RepoTags": []string{imageRef},
		"Layers":   layerPaths(layerDigests),
	}

	manifestData, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("序列化manifest失败: %w", err)
	}

	manifestPath := filepath.Join(cacheDir, "manifest", platform, url.QueryEscape(imageRef)+".json")
	if err := os.WriteFile(manifestPath, manifestData, os.ModePerm); err != nil {
		return err
	}

	repositories := make(map[string]map[string]string)
	repoName, tag, _ := strings.Cut(imageRef, ":")
	if tag == "" {
		tag = "latest"
	}
	repositories[repoName] = map[string]string{tag: configDigest}

	repositoriesData, err := json.Marshal(repositories)
	if err != nil {
		return fmt.Errorf("序列化repositories失败: %w", err)
	}

	repositoriesPath := filepath.Join(cacheDir, "repositories", platform, url.QueryEscape(imageRef)+".json")
	return os.WriteFile(repositoriesPath, repositoriesData, os.ModePerm)
}

func selectPlatformImage(desc *remote.Descriptor, options *StreamOptions) (v1.Image, error) {
	index, err := desc.ImageIndex()
	if err != nil {
		return nil, fmt.Errorf("获取镜像索引失败: %w", err)
	}

	manifest, err := index.IndexManifest()
	if err != nil {
		return nil, fmt.Errorf("获取索引清单失败: %w", err)
	}

	var selectedDesc *v1.Descriptor
	idx := slices.IndexFunc(manifest.Manifests, func(m v1.Descriptor) bool {
		return m.Platform != nil &&
			m.Platform.OS == options.Platform.OS &&
			m.Platform.Architecture == options.Platform.Arch &&
			m.Platform.Variant == options.Platform.Variant
	})

	if idx != -1 {
		selectedDesc = &manifest.Manifests[idx]
	} else {
		selectedDesc = nil
	}

	if selectedDesc == nil {
		return nil, fmt.Errorf("未找到与 '%s' 匹配的平台镜像。可用平台: %s", options.Platform.String(), getAvailablePlatforms(manifest))
	}

	img, err := index.Image(selectedDesc.Digest)
	if err != nil {
		return nil, fmt.Errorf("获取选中镜像失败: %w", err)
	}

	return img, nil
}

func getAvailablePlatforms(manifest *v1.IndexManifest) string {
	var availablePlatforms []string
	for _, m := range manifest.Manifests {
		if m.Platform != nil {
			platformStr := fmt.Sprintf("%s/%s", m.Platform.OS, m.Platform.Architecture)
			if m.Platform.Variant != "" {
				platformStr += "/" + m.Platform.Variant
			}
			availablePlatforms = append(availablePlatforms, platformStr)
		}
	}
	return strings.Join(availablePlatforms, ", ")
}

type ImagePlatform struct {
	OS      string
	Arch    string
	Variant string
}

func (p ImagePlatform) String() string {
	if p.Variant != "" {
		return p.OS + "/" + p.Arch + "/" + p.Variant
	}
	return p.OS + "/" + p.Arch
}

var (
	ImagePlatformAmd64    ImagePlatform = ImagePlatform{OS: "linux", Arch: "amd64"}
	ImagePlatformArmV5    ImagePlatform = ImagePlatform{OS: "linux", Arch: "arm", Variant: "v5"}
	ImagePlatformArmV7    ImagePlatform = ImagePlatform{OS: "linux", Arch: "arm", Variant: "v7"}
	ImagePlatformArm64V8  ImagePlatform = ImagePlatform{OS: "linux", Arch: "arm64", Variant: "v8"}
	ImagePlatformI386     ImagePlatform = ImagePlatform{OS: "linux", Arch: "386"}
	ImagePlatformMips64le ImagePlatform = ImagePlatform{OS: "linux", Arch: "mips64le"}
	ImagePlatformPpc64le  ImagePlatform = ImagePlatform{OS: "linux", Arch: "ppc64le"}
	ImagePlatformS390x    ImagePlatform = ImagePlatform{OS: "linux", Arch: "s390x"}
)

// StreamOptions 下载选项
type StreamOptions struct {
	Platform            ImagePlatform
	Compression         bool // 是否压缩，默认压缩
	UseCompressedLayers bool // 是否保存原始压缩层，默认开启
}
