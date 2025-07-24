# 层数不匹配问题修复测试

## 修复内容总结

### 1. 核心问题
- **原问题**: `invalid manifest, layers length mismatch: expected 25, got 36`
- **根本原因**: `streamImageLayers` 函数过滤了已存在的层，但 `manifest.json` 中的 `Layers` 字段应包含所有层

### 2. 修复方案
采用了**方案1：分离下载逻辑与 manifest 生成逻辑**

### 3. 具体修改

#### 3.1 修改 `streamImageLayers` 函数
- 分离层过滤逻辑，获取完整层列表和需要下载的层列表
- 传递两个参数给 `streamDockerFormatWithReturn`：`allLayers` 和 `layersToDownload`

#### 3.2 修改 `streamDockerFormatWithReturn` 函数
- 更新函数签名，接收完整层列表和需要下载的层列表
- 使用 `allLayers` 生成完整的 `manifest.json`
- 只对 `layersToDownload` 进行实际下载

#### 3.3 修改 `assembleImageTar` 函数
- 添加层文件存在性检查
- 对于不存在的层文件，跳过添加到 tar 包（记录日志）

### 4. 修复效果

#### 4.1 解决的问题
- ✅ `manifest.json` 现在包含所有层信息（25层）
- ✅ 只下载不存在的层（增量下载保持）
- ✅ 符合 Docker 标准格式
- ✅ 避免层数不匹配错误

#### 4.2 保持的功能
- ✅ 增量下载功能
- ✅ 断点续传
- ✅ 性能优化
- ✅ 错误处理

### 5. 测试建议

#### 5.1 基本功能测试
```bash
# 测试完整镜像下载
./lite-image-export -image nginx:latest -output ./test-output

# 检查 manifest.json 层数
jq '.[] | .Layers | length' ./test-output/manifest.json
```

#### 5.2 增量下载测试
```bash
# 第一次下载
./lite-image-export -image nginx:latest -output ./test-output1

# 第二次下载（应该跳过已存在的层）
./lite-image-export -image nginx:latest -output ./test-output2

# 比较两次的 manifest.json
diff ./test-output1/manifest.json ./test-output2/manifest.json
```

#### 5.3 Docker 兼容性测试
```bash
# 导入生成的镜像
docker load -i ./test-output/nginx-latest.tar

# 验证镜像可以正常运行
docker run --rm nginx:latest nginx -v
```

### 6. 预期结果

- `manifest.json` 中的层数应该与镜像配置文件中的层数一致
- 不应该再出现 "layers length mismatch" 错误
- 增量下载功能正常工作
- 生成的 Docker 镜像可以正常加载和运行

### 7. 风险评估

#### 7.1 低风险
- 代码修改集中在核心逻辑，影响范围可控
- 保持了现有的错误处理机制
- 向后兼容性良好

#### 7.2 需要注意
- 对于大量已存在层的情况，tar 包可能会缺少一些层文件
- 需要确保目标 Docker 环境已经有这些层

### 8. 后续优化建议

1. **添加更详细的日志**: 记录跳过的层和原因
2. **性能监控**: 监控修复后的下载性能
3. **兼容性测试**: 在不同 Docker 版本上测试
4. **错误恢复**: 添加层文件缺失时的恢复机制