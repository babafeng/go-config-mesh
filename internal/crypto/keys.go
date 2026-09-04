package crypto

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"config-mesh/internal/model"
	"filippo.io/age"
	"golang.org/x/crypto/ssh"
)

// DiscoverLocalSSHKeys 扫描 ~/.ssh 目录寻找可用的 SSH 公钥和私钥对
func DiscoverLocalSSHKeys() ([]model.KeyPairInfo, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("无法获取用户家目录: %w", err)
	}

	sshDir := filepath.Join(homeDir, ".ssh")
	if _, err := os.Stat(sshDir); os.IsNotExist(err) {
		return nil, nil
	}

	entries, err := os.ReadDir(sshDir)
	if err != nil {
		return nil, fmt.Errorf("读取 ~/.ssh 目录失败: %w", err)
	}

	var keyPairs []model.KeyPairInfo
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".pub") {
			continue
		}

		pubPath := filepath.Join(sshDir, entry.Name())
		privName := strings.TrimSuffix(entry.Name(), ".pub")
		privPath := filepath.Join(sshDir, privName)

		// 验证私钥是否存在
		hasPriv := false
		if _, err := os.Stat(privPath); err == nil {
			hasPriv = true
		}

		// 读取并解析公钥以获取类型与指纹
		pubData, err := os.ReadFile(pubPath)
		if err != nil {
			continue
		}

		pubKey, _, _, _, err := ssh.ParseAuthorizedKey(pubData)
		if err != nil {
			continue
		}

		keyType := pubKey.Type()
		// 仅支持 ed25519 和 rsa
		if keyType != ssh.KeyAlgoED25519 && keyType != ssh.KeyAlgoRSA {
			continue
		}

		fingerprint := ssh.FingerprintSHA256(pubKey)

		kp := model.KeyPairInfo{
			Name:        entry.Name(),
			PubKeyPath:  pubPath,
			KeyType:     keyType,
			Fingerprint: fingerprint,
		}
		if hasPriv {
			kp.PrivKeyPath = privPath
		}

		keyPairs = append(keyPairs, kp)
	}

	// 严格按照 OpenSSH 默认密钥规范排序：优先 id_ed25519，其次 id_rsa，其余按字母序排列
	sort.SliceStable(keyPairs, func(i, j int) bool {
		pI := sshDefaultKeyPriority(keyPairs[i].Name)
		pJ := sshDefaultKeyPriority(keyPairs[j].Name)
		if pI != pJ {
			return pI < pJ
		}
		return keyPairs[i].Name < keyPairs[j].Name
	})

	return keyPairs, nil
}

func sshDefaultKeyPriority(filename string) int {
	base := strings.TrimSuffix(filename, ".pub")
	switch base {
	case "id_ed25519":
		return 0
	case "id_rsa":
		return 1
	case "id_ecdsa":
		return 2
	default:
		return 100
	}
}

// CalculateSHA256 计算数据的 SHA256 哈希值
func CalculateSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// CalculateFileSHA256 计算文件的 SHA256 哈希值
func CalculateFileSHA256(filePath string) (string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	return CalculateSHA256(data), nil
}

// LoadRecipients 读取 recipients.pub 中的 SSH 公钥并转换为 age recipients。
func LoadRecipients(filePath string) ([]age.Recipient, error) {
	data, err := readRegularFileLimit(filePath, 1<<20)
	if err != nil {
		return nil, err
	}

	var recipients []age.Recipient
	seen := make(map[string]bool)
	for lineNo, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
		if err != nil {
			return nil, fmt.Errorf("recipients.pub 第 %d 行公钥无效: %w", lineNo+1, err)
		}
		if pub.Type() != ssh.KeyAlgoED25519 && pub.Type() != ssh.KeyAlgoRSA {
			return nil, fmt.Errorf("recipients.pub 第 %d 行密钥类型不支持: %s", lineNo+1, pub.Type())
		}
		fingerprint := ssh.FingerprintSHA256(pub)
		if seen[fingerprint] {
			continue
		}
		recipient, err := ParseSSHPublicKey(line)
		if err != nil {
			return nil, err
		}
		seen[fingerprint] = true
		recipients = append(recipients, recipient)
	}
	if len(recipients) == 0 {
		return nil, fmt.Errorf("recipients.pub 中没有可用的 SSH 公钥")
	}
	return recipients, nil
}

// AddRecipient 以原子方式将一把 SSH 公钥加入 recipients.pub，并按指纹去重。
func AddRecipient(filePath, publicKeyOrPath string) (string, bool, error) {
	content := strings.TrimSpace(publicKeyOrPath)
	if info, err := os.Stat(publicKeyOrPath); err == nil && !info.IsDir() {
		data, err := os.ReadFile(publicKeyOrPath)
		if err != nil {
			return "", false, err
		}
		content = strings.TrimSpace(string(data))
	}

	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(content))
	if err != nil {
		return "", false, fmt.Errorf("解析 SSH 公钥失败: %w", err)
	}
	if pub.Type() != ssh.KeyAlgoED25519 && pub.Type() != ssh.KeyAlgoRSA {
		return "", false, fmt.Errorf("不支持的 SSH 公钥类型: %s", pub.Type())
	}
	fingerprint := ssh.FingerprintSHA256(pub)
	canonical := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))

	var existing []string
	if data, err := readRegularFileLimit(filePath, 1<<20); err == nil {
		existing = strings.Split(string(data), "\n")
	} else if !os.IsNotExist(err) {
		return "", false, err
	}
	for _, line := range existing {
		old, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(line)))
		if err == nil && ssh.FingerprintSHA256(old) == fingerprint {
			return fingerprint, false, nil
		}
	}

	if err := os.MkdirAll(filepath.Dir(filePath), 0700); err != nil {
		return "", false, err
	}
	cleanLines := make([]string, 0, len(existing)+1)
	for _, line := range existing {
		line = strings.TrimSpace(line)
		if line != "" {
			cleanLines = append(cleanLines, line)
		}
	}
	cleanLines = append(cleanLines, canonical)
	data := []byte(strings.Join(cleanLines, "\n") + "\n")
	tmp, err := os.CreateTemp(filepath.Dir(filePath), ".recipients-*")
	if err != nil {
		return "", false, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0644); err != nil {
		tmp.Close()
		return "", false, err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", false, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", false, err
	}
	if err := tmp.Close(); err != nil {
		return "", false, err
	}
	if err := os.Rename(tmpName, filePath); err != nil {
		return "", false, err
	}
	return fingerprint, true, nil
}

func readRegularFileLimit(filePath string, maxBytes int64) ([]byte, error) {
	info, err := os.Lstat(filePath)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("拒绝读取非普通文件: %s", filePath)
	}
	if info.Size() < 0 || info.Size() > maxBytes {
		return nil, fmt.Errorf("文件超过 %d 字节上限: %s", maxBytes, filePath)
	}
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("文件读取期间增长并超过 %d 字节上限: %s", maxBytes, filePath)
	}
	return data, nil
}
