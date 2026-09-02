package scanner

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"config-mesh/internal/model"
)

const MaxContentInspectionBytes int64 = 64 << 20

var (
	safeSecretFilename  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,120}$`)
	knownSecretPatterns = []*regexp.Regexp{
		regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
		regexp.MustCompile(`ASIA[0-9A-Z]{16}`),
		regexp.MustCompile(`gh[pousr]_[A-Za-z0-9_]{30,}`),
		regexp.MustCompile(`sk-[A-Za-z0-9_-]{20,}`),
	}
	secretAssignmentPattern = regexp.MustCompile(`(?i)["']?(api[_-]?key|access[_-]?token|auth[_-]?token|client[_-]?secret|password|passwd|secret|token)["']?\s*(?:=|:)\s*["']?([^\s"',;}\]]{6,})`)
)

// BlacklistPatterns 敏感文件黑名单匹配模式（防止系统级临时密钥或误选的敏感文件）
var BlacklistPatterns = []string{
	// GPG 私钥临时导出文件
	"secring.gpg", "private-keys-v1.d",
	// 临时证书或未受管的私钥
	"*.p12", "*.pfx", "*.keystore", "*.pem", "*.key",
	// 密码与 shadow
	"passwd", "shadow", "master.key", "credentials", "credentials.json",
	".credentials.json", "auth.json", "secrets.json", "oauth_creds.json",
	// 环境文件经常包含 Token/密码
	".env", ".env.*",
}

// IsSensitiveFile 检查文件是否命中黑名单拦截
func IsSensitiveFile(filePath string) bool {
	cleanPath := filepath.Clean(filePath)
	baseName := filepath.Base(cleanPath)
	slashPath := "/" + strings.TrimPrefix(filepath.ToSlash(cleanPath), "/")

	// 如果是以 .pub 结尾的 SSH 公钥，绝不属于敏感私钥
	if strings.HasSuffix(baseName, ".pub") {
		return false
	}

	// 标记凭据位置；受控 secret_kind 可在后续精确白名单校验后同步。
	if strings.Contains(slashPath, "/.aws/credentials") ||
		strings.Contains(slashPath, "/.kube/config") ||
		strings.Contains(slashPath, "/.docker/config.json") ||
		strings.Contains(slashPath, "/.config/gcloud/") ||
		strings.Contains(slashPath, "/.gnupg/private-keys-v1.d/") {
		return true
	}

	// 标记常见 SSH 私钥名；动态发现流程会解析格式并添加受控凭据类型。
	if strings.Contains(slashPath, "/.ssh/") {
		lower := strings.ToLower(baseName)
		if strings.HasPrefix(lower, "id_") || strings.HasSuffix(lower, "_rsa") ||
			strings.HasSuffix(lower, "_ed25519") || strings.HasSuffix(lower, "_ecdsa") {
			return true
		}
	}

	for _, pattern := range BlacklistPatterns {
		// 支持 glob 匹配
		if strings.Contains(pattern, "*") {
			if matched, _ := filepath.Match(pattern, baseName); matched {
				return true
			}
		}

		// 检查文件名是否包含敏感词
		if strings.EqualFold(baseName, pattern) {
			return true
		}

		// 检查路径中是否包含特定敏感子路径
		if strings.Contains(cleanPath, pattern) {
			return true
		}
	}

	return false
}

// IsSensitiveConfigPath 检查一个完整配置项是否本身就是凭据集合。
// 对目录项使用更严格的规则，防止“根目录名未命中，子文件却是私钥”。
func IsSensitiveConfigPath(relHomePath string, isDir bool) bool {
	rel := filepath.ToSlash(filepath.Clean(relHomePath))
	if rel == "." || rel == "" {
		return true
	}
	if strings.HasPrefix(rel, "../") || filepath.IsAbs(relHomePath) {
		return true
	}
	if strings.HasPrefix(rel, ".ssh/") {
		base := strings.TrimPrefix(rel, ".ssh/")
		if !strings.Contains(base, "/") && (strings.HasSuffix(base, ".pub") || strings.EqualFold(base, "config")) {
			return false
		}
		// 自定义 SSH 私钥通常不使用 id_* 命名；默认将其余直属文件视为敏感。
		return true
	}

	switch rel {
	case ".ssh", ".aws", ".aliyun", ".tccli", ".config/gcloud", ".gnupg",
		".claude", ".codex", ".ollama", ".config/helm":
		return isDir
	case ".aws/credentials", ".aliyun/config.json", ".kube/config", ".docker/config.json", ".npmrc":
		return true
	}

	return IsSensitiveFile(rel)
}

// IsAllowedSecretConfigPath 将 secret_kind 绑定到严格路径白名单。
// 远端清单不能仅靠声明“这是凭据”就绕过任意路径限制。
func IsAllowedSecretConfigPath(relHomePath string, isDir bool, kind model.SecretKind) bool {
	if isDir {
		return false
	}
	rel := filepath.ToSlash(filepath.Clean(relHomePath))
	switch kind {
	case model.SecretKindAWSCredentials:
		return rel == ".aws/credentials"
	case model.SecretKindAliyunConfig:
		return rel == ".aliyun/config.json"
	case model.SecretKindSSHPrivateKey:
		if !strings.HasPrefix(rel, ".ssh/") || strings.Count(rel, "/") != 1 {
			return false
		}
		base := strings.TrimPrefix(rel, ".ssh/")
		if base == "" || strings.HasSuffix(base, ".pub") {
			return false
		}
		switch strings.ToLower(base) {
		case "config", "authorized_keys", "authorized_keys2", "known_hosts", "known_hosts.old", "environment":
			return false
		}
		return safeSecretFilename.MatchString(base)
	case model.SecretKindDetectedConfig:
		// 只允许扫描器内置的普通单文件预设或受管动态配置升级为“检测到凭据”的配置。
		// 远端不能靠伪造 secret_kind 绕过任意敏感路径限制。
		for _, preset := range DefaultPresets {
			if !preset.IsDir && preset.SecretKind == "" && filepath.ToSlash(filepath.Clean(preset.RelHomePath)) == rel {
				return true
			}
		}
		if IsClaudeSettingsRelPath(rel, isDir) {
			return true
		}
		return false
	default:
		return false
	}
}

// IsClaudeSettingsFilename 判断文件名是否属于 Claude Code 的 settings.json* 或 settings*.json 配置文件
func IsClaudeSettingsFilename(name string) bool {
	if !safeSecretFilename.MatchString(name) {
		return false
	}
	lower := strings.ToLower(name)
	if lower == "settings.json" ||
		strings.HasPrefix(lower, "settings.json.") ||
		(strings.HasPrefix(lower, "settings.") && strings.HasSuffix(lower, ".json")) ||
		(strings.HasPrefix(lower, "settings-") && strings.HasSuffix(lower, ".json")) ||
		(strings.HasPrefix(lower, "settings_") && strings.HasSuffix(lower, ".json")) ||
		(strings.HasPrefix(lower, "setting") && strings.Contains(lower, ".json")) {
		return true
	}
	return false
}

// IsClaudeSettingsRelPath 检查相对路径是否为合法的 Claude settings 文件
func IsClaudeSettingsRelPath(relHomePath string, isDir bool) bool {
	if isDir {
		return false
	}
	rel := filepath.ToSlash(filepath.Clean(relHomePath))
	if !strings.HasPrefix(rel, ".claude/") || strings.Count(rel, "/") != 1 {
		return false
	}
	base := strings.TrimPrefix(rel, ".claude/")
	return IsClaudeSettingsFilename(base)
}

// FileContainsSensitiveContent 对实际要同步的普通文件做保守的凭据内容检测。
// 环境变量引用和常见占位值不会被当成明文凭据。
func FileContainsSensitiveContent(filePath string) (bool, error) {
	info, err := os.Lstat(filePath)
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, nil
	}
	if info.Size() > MaxContentInspectionBytes {
		return false, fmt.Errorf("文件超过内容检查上限 %d 字节: %s", MaxContentInspectionBytes, filePath)
	}
	file, err := os.Open(filePath)
	if err != nil {
		return false, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxContentInspectionBytes+1))
	if err != nil {
		return false, err
	}
	if int64(len(data)) > MaxContentInspectionBytes {
		return false, fmt.Errorf("文件读取期间增长并超过内容检查上限: %s", filePath)
	}
	return ContainsSensitiveContent(data), nil
}

// ContainsSensitiveContent 检查已经读取的配置字节，供打包临界点再次校验。
func ContainsSensitiveContent(data []byte) bool {
	if bytes.Contains(data, []byte("-----BEGIN OPENSSH PRIVATE KEY-----")) ||
		bytes.Contains(data, []byte("-----BEGIN PRIVATE KEY-----")) ||
		bytes.Contains(data, []byte("-----BEGIN RSA PRIVATE KEY-----")) ||
		bytes.Contains(data, []byte("-----BEGIN EC PRIVATE KEY-----")) {
		return true
	}
	for _, pattern := range knownSecretPatterns {
		if pattern.Match(data) {
			return true
		}
	}
	for _, match := range secretAssignmentPattern.FindAllSubmatch(data, -1) {
		if len(match) < 3 {
			continue
		}
		value := strings.ToLower(strings.TrimSpace(string(match[2])))
		if isSecretPlaceholder(value) {
			continue
		}
		return true
	}
	return false
}

func isSecretPlaceholder(value string) bool {
	if value == "" || strings.HasPrefix(value, "$") || strings.HasPrefix(value, "<") ||
		strings.HasPrefix(value, "env:") || strings.HasPrefix(value, "your_") {
		return true
	}
	for _, placeholder := range []string{"example", "placeholder", "changeme", "change-me", "dummy", "redacted", "replace_me", "replace-me"} {
		if value == placeholder || strings.HasPrefix(value, placeholder+"_") || strings.HasPrefix(value, placeholder+"-") {
			return true
		}
	}
	return false
}
