package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/partial"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/gookit/goutil/arrutil"
)

func main() {
	image := flag.String("image", "redis", "镜像名称:TAG")
	flag.Parse()
	// 初始化HTTP客户端
	initHTTPClients()

	// 初始化镜像流式下载器
	initImageStreamer()
	existLayers, err := LoadImageLayers()
	if err != nil {
		log.Fatalf("加载镜像层失败: %v", err)
	}
	fmt.Println(existLayers)

	imageRef, err := name.ParseReference(*image)
	if err != nil {
		fmt.Errorf("", err)
	}

	desc, err := remote.Get(imageRef, globalImageStreamer.remoteOptions...)
	if err != nil {
		fmt.Errorf("", err)
	}
	// 创建输出文件
	writer, err := os.OpenFile("output.tgz", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		log.Fatalf("创建输出文件失败: %v", err)
	}
	defer writer.Close()

	options := &StreamOptions{
		Compression:         true,
		Platform:            "linux/amd64",
		UseCompressedLayers: true,
	}

	switch desc.MediaType {
	case types.OCIImageIndex, types.DockerManifestList:
		img, err := selectPlatformImage(desc, options)
		if err != nil {
			return
		}
		streamImageLayers(img, existLayers, writer, options, *image)
	default:
		img, err := desc.Image()
		if err != nil {
			return
		}
		streamImageLayers(img, existLayers, writer, options, *image)
	}
}

func LoadImageLayers() ([]string, error) {
	layers := []string{}

	sshClient := NewSSHClient("192.168.44.213", 22, "root", "123456", "", "")
	if err := sshClient.Connect(); err != nil {
		return nil, err
	}

	defer sshClient.Disconnect()
	if result, err := sshClient.ExecuteCommand("cd `docker info | grep 'Docker Root Dir' | awk '{print $4}'` && ls ./image/overlay2/distribution/diffid-by-digest/sha256"); err != nil {
		return nil, err
	} else {
		layers = strings.Split(result.Stdout, "\n")
	}

	return layers, nil
}

func streamImageLayers(img v1.Image, existLayers []string, writer io.Writer, options *StreamOptions, imageRef string) error {
	var finalWriter io.Writer = writer

	if options.Compression {
		gzWriter := gzip.NewWriter(writer)
		defer gzWriter.Close()
		finalWriter = gzWriter
	}

	tarWriter := tar.NewWriter(finalWriter)
	defer tarWriter.Close()

	configFile, err := img.ConfigFile()
	if err != nil {
		return fmt.Errorf("获取镜像配置失败: %w", err)
	}

	layers, err := img.Layers()
	if err != nil {
		return fmt.Errorf("获取镜像层失败: %w", err)
	}

	log.Printf("镜像包含 %d 层", len(layers))
	layers = arrutil.Filter(layers, func(l v1.Layer) bool {
		digest, _ := l.Digest()
		return !arrutil.Contains(existLayers, digest.Hex)
	})
	log.Printf("过滤后镜像包含 %d 层", len(layers))
	return streamDockerFormat(tarWriter, img, layers, configFile, imageRef, options)
}

func streamDockerFormat(tarWriter *tar.Writer, img v1.Image, layers []v1.Layer, configFile *v1.ConfigFile, imageRef string, options *StreamOptions) error {
	return streamDockerFormatWithReturn(tarWriter, img, layers, configFile, imageRef, nil, nil, options)
}

// streamDockerFormatWithReturn 生成Docker格式并返回manifest和repositories信息
func streamDockerFormatWithReturn(tarWriter *tar.Writer, img v1.Image, layers []v1.Layer, configFile *v1.ConfigFile, imageRef string, manifestOut *map[string]interface{}, repositoriesOut *map[string]map[string]string, options *StreamOptions) error {
	configDigest, err := img.ConfigName()
	if err != nil {
		return err
	}

	configData, err := json.Marshal(configFile)
	if err != nil {
		return err
	}

	configHeader := &tar.Header{
		Name: configDigest.String() + ".json",
		Size: int64(len(configData)),
		Mode: 0644,
	}

	if err := tarWriter.WriteHeader(configHeader); err != nil {
		return err
	}
	if _, err := tarWriter.Write(configData); err != nil {
		return err
	}

	layerDigests := make([]string, len(layers))
	for i, layer := range layers {

		if err := func() error {
			digest, err := layer.Digest()
			if err != nil {
				return err
			}
			layerDigests[i] = digest.String()

			layerDir := digest.String()
			layerHeader := &tar.Header{
				Name:     layerDir + "/",
				Typeflag: tar.TypeDir,
				Mode:     0755,
			}

			if err := tarWriter.WriteHeader(layerHeader); err != nil {
				return err
			}

			var layerSize int64
			var layerReader io.ReadCloser

			// 根据配置选择使用压缩层或未压缩层
			if options != nil && options.UseCompressedLayers {
				layerSize, err = layer.Size()
				if err != nil {
					return err
				}
				layerReader, err = layer.Compressed()
			} else {
				layerSize, err = partial.UncompressedSize(layer)
				if err != nil {
					return err
				}
				layerReader, err = layer.Uncompressed()
			}

			if err != nil {
				return err
			}
			defer layerReader.Close()

			layerTarHeader := &tar.Header{
				Name: layerDir + "/layer.tar",
				Size: layerSize,
				Mode: 0644,
			}

			if err := tarWriter.WriteHeader(layerTarHeader); err != nil {
				return err
			}

			if _, err := io.Copy(tarWriter, layerReader); err != nil {
				return err
			}

			return nil
		}(); err != nil {
			return err
		}

		log.Printf("已处理层 %d/%d", i+1, len(layers))
	}

	// 构建单个镜像的manifest信息
	singleManifest := map[string]interface{}{
		"Config":   configDigest.String() + ".json",
		"RepoTags": []string{imageRef},
		"Layers": func() []string {
			var layers []string
			for _, digest := range layerDigests {
				layers = append(layers, digest+"/layer.tar")
			}
			return layers
		}(),
	}

	// 构建repositories信息
	repositories := make(map[string]map[string]string)
	parts := strings.Split(imageRef, ":")
	if len(parts) == 2 {
		repoName := parts[0]
		tag := parts[1]
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
		return err
	}

	manifestHeader := &tar.Header{
		Name: "manifest.json",
		Size: int64(len(manifestData)),
		Mode: 0644,
	}

	if err := tarWriter.WriteHeader(manifestHeader); err != nil {
		return err
	}

	if _, err := tarWriter.Write(manifestData); err != nil {
		return err
	}

	// 写入repositories文件
	repositoriesData, err := json.Marshal(repositories)
	if err != nil {
		return err
	}

	repositoriesHeader := &tar.Header{
		Name: "repositories",
		Size: int64(len(repositoriesData)),
		Mode: 0644,
	}

	if err := tarWriter.WriteHeader(repositoriesHeader); err != nil {
		return err
	}

	_, err = tarWriter.Write(repositoriesData)
	return err
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
		return nil, fmt.Errorf("未找到合适的平台镜像")
	}

	img, err := index.Image(selectedDesc.Digest)
	if err != nil {
		return nil, fmt.Errorf("获取选中镜像失败: %w", err)
	}

	return img, nil
}
