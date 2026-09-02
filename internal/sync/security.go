package sync

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	meshcrypto "config-mesh/internal/crypto"
	"config-mesh/internal/model"
	"config-mesh/internal/scanner"
)

const (
	MaxArchiveBytes int64 = 64 << 20 // 64 MiB，避免内存打包与 GitHub 大文件失败
	MaxArchiveFiles       = 100_000
)

var safeIdentifier = regexp.MustCompile(`^[A-Za-z0-9._-]{1,200}$`)

var (
	awsAccessKeyPattern = regexp.MustCompile(`(?mi)^\s*aws_access_key_id\s*=\s*\S+\s*$`)
	awsSecretKeyPattern = regexp.MustCompile(`(?mi)^\s*aws_secret_access_key\s*=\s*\S+\s*$`)
)

// ValidateAndResolveConfigItem 验证远端 Manifest 中的路径和文件名，
// 并用经过边界检查的本机路径覆盖不可信的 LocalPath。
func ValidateAndResolveConfigItem(homeDir string, item model.ConfigItem) (model.ConfigItem, error) {
	if !safeIdentifier.MatchString(item.ID) {
		return item, fmt.Errorf("非法配置项 ID: %q", item.ID)
	}
	if item.Deleted {
		if item.VaultFile != "" || item.PayloadHash != "" || item.ContentHash != "" {
			return item, fmt.Errorf("删除 tombstone 不得包含 vault 文件或内容哈希")
		}
	} else if !safeIdentifier.MatchString(item.VaultFile) || filepath.Base(item.VaultFile) != item.VaultFile ||
		!strings.HasSuffix(item.VaultFile, ".age") {
		return item, fmt.Errorf("非法 vault 文件名: %q", item.VaultFile)
	}
	pathIsSensitive := scanner.IsSensitiveConfigPath(item.RelHomePath, item.IsDir)
	if item.SecretKind != "" {
		if !scanner.IsAllowedSecretConfigPath(item.RelHomePath, item.IsDir, item.SecretKind) {
			return item, fmt.Errorf("凭据类型与路径不匹配: %s -> %s", item.SecretKind, item.RelHomePath)
		}
		item.FileMode = 0600
	} else if pathIsSensitive {
		return item, fmt.Errorf("敏感路径缺少受支持的 secret_kind: %s", item.RelHomePath)
	}
	target, err := safeJoin(homeDir, item.RelHomePath)
	if err != nil {
		return item, fmt.Errorf("非法本地目标路径 %q: %w", item.RelHomePath, err)
	}
	if filepath.Clean(target) == filepath.Clean(homeDir) {
		return item, fmt.Errorf("拒绝使用用户家目录根作为配置目标")
	}
	if err := ensureNoSymlinkParents(homeDir, target); err != nil {
		return item, fmt.Errorf("本地目标路径不安全: %w", err)
	}
	item.LocalPath = target
	return item, nil
}

// ValidateSensitivePayload 对明确选择的凭据执行类型验证。
// 已知配置文件若检测到凭据内容，可作为 detected_config_secret 受控同步。
func ValidateSensitivePayload(item model.ConfigItem, data []byte) error {
	if item.IsDir {
		if item.SecretKind != "" {
			return fmt.Errorf("凭据类型不支持目录负载: %s", item.SecretKind)
		}
		return nil
	}
	switch item.SecretKind {
	case "":
		if scanner.ContainsSensitiveContent(data) {
			return fmt.Errorf("普通配置包含明文凭据，必须使用受支持的凭据类型")
		}
	case model.SecretKindSSHPrivateKey:
		if !meshcrypto.IsSSHPrivateKeyData(data) {
			return fmt.Errorf("SSH 私钥负载格式无效")
		}
	case model.SecretKindAWSCredentials:
		if !awsAccessKeyPattern.Match(data) || !awsSecretKeyPattern.Match(data) {
			return fmt.Errorf("AWS credentials 缺少 aws_access_key_id 或 aws_secret_access_key")
		}
	case model.SecretKindAliyunConfig:
		var document map[string]any
		if err := json.Unmarshal(data, &document); err != nil || len(document) == 0 {
			return fmt.Errorf("阿里云 config.json 不是有效的非空 JSON 对象")
		}
	case model.SecretKindDetectedConfig:
		if !scanner.ContainsSensitiveContent(data) {
			return fmt.Errorf("配置未检测到凭据内容，secret_kind 与负载不匹配")
		}
	default:
		return fmt.Errorf("不支持的凭据类型: %s", item.SecretKind)
	}
	return nil
}

func safeJoin(root, relative string) (string, error) {
	if root == "" || relative == "" || filepath.IsAbs(relative) {
		return "", fmt.Errorf("路径必须是非空相对路径")
	}
	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	target := filepath.Join(cleanRoot, filepath.Clean(relative))
	rel, err := filepath.Rel(cleanRoot, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("路径逃逸允许的根目录")
	}
	return target, nil
}

// ValidateTarArchive 对解密后的 tar 进行完整预检，不写入文件系统。
func ValidateTarArchive(tarData []byte) error {
	tr := tar.NewReader(bytes.NewReader(tarData))
	seen := make(map[string]bool)
	var totalSize int64
	entries := 0
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("读取 tar 失败: %w", err)
		}
		entries++
		if entries > MaxArchiveFiles {
			return fmt.Errorf("tar 条目数超过上限 %d", MaxArchiveFiles)
		}
		name, err := validateTarName(header.Name)
		if err != nil {
			return err
		}
		if seen[name] {
			return fmt.Errorf("tar 包含重复条目: %s", name)
		}
		if scanner.IsSensitiveFile(name) {
			return fmt.Errorf("tar 包含敏感路径: %s", name)
		}
		seen[name] = true

		switch header.Typeflag {
		case tar.TypeDir:
		case tar.TypeReg:
			if header.Size < 0 || header.Size > MaxArchiveBytes-totalSize {
				return fmt.Errorf("tar 解包大小超过上限 %d 字节", MaxArchiveBytes)
			}
			totalSize += header.Size
			entryData, readErr := io.ReadAll(io.LimitReader(tr, header.Size+1))
			if readErr != nil || int64(len(entryData)) != header.Size {
				return fmt.Errorf("读取 tar 条目失败: %s", name)
			}
			if scanner.ContainsSensitiveContent(entryData) {
				return fmt.Errorf("tar 条目包含明文凭据: %s", name)
			}
		case tar.TypeSymlink:
			if err := validateTarLink(name, header.Linkname); err != nil {
				return err
			}
		default:
			return fmt.Errorf("tar 包含不支持的条目类型 %d: %s", header.Typeflag, name)
		}
	}
	return nil
}

func validateTarName(name string) (string, error) {
	if name == "" || strings.ContainsRune(name, '\x00') || path.IsAbs(name) || strings.Contains(name, `\`) {
		return "", fmt.Errorf("非法 tar 条目路径: %q", name)
	}
	clean := path.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("tar 条目路径越界: %q", name)
	}
	return clean, nil
}

func validateTarLink(name, link string) error {
	if link == "" || path.IsAbs(link) || strings.ContainsRune(link, '\x00') || strings.Contains(link, `\`) {
		return fmt.Errorf("非法 tar 软链接 %s -> %q", name, link)
	}
	resolved := path.Clean(path.Join(path.Dir(name), link))
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return fmt.Errorf("tar 软链接越界: %s -> %s", name, link)
	}
	return nil
}

func ensureNoSymlinkParents(root, target string) error {
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("目标路径越界: %s", target)
	}
	current := filepath.Clean(root)
	parts := strings.Split(filepath.Dir(rel), string(filepath.Separator))
	for _, part := range parts {
		if part == "." || part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("目标父路径包含软链接: %s", current)
		}
	}
	return nil
}
