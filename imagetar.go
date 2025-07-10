package main

import (
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// ImageStreamer 镜像流式下载器
type ImageStreamer struct {
	concurrency   int
	remoteOptions []remote.Option
}

// StreamOptions 下载选项
type StreamOptions struct {
	Platform            string
	Compression         bool
	UseCompressedLayers bool // 是否保存原始压缩层，默认开启
}

var globalImageStreamer *ImageStreamer

// initImageStreamer 初始化镜像下载器
func initImageStreamer() {
	globalImageStreamer = &ImageStreamer{
		concurrency: 1,
		remoteOptions: []remote.Option{
			remote.WithAuth(authn.Anonymous),
			remote.WithTransport(GetGlobalHTTPClient().Transport),
		},
	}
}

// formatPlatformText 格式化平台文本
func formatPlatformText(platform string) string {
	if platform == "" {
		return "自动选择"
	}
	return platform
}
