package crypto

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rsa"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"filippo.io/age"
	"filippo.io/age/agessh"
	"golang.org/x/crypto/ssh"
)

// ParseSSHPublicKey 解析 SSH 公钥字符串或文件为 age.Recipient
func ParseSSHPublicKey(pubKeyStrOrPath string) (age.Recipient, error) {
	content := strings.TrimSpace(pubKeyStrOrPath)
	// 如果是文件路径则读取文件内容
	if _, err := os.Stat(pubKeyStrOrPath); err == nil {
		data, err := os.ReadFile(pubKeyStrOrPath)
		if err != nil {
			return nil, fmt.Errorf("读取公钥文件失败: %w", err)
		}
		content = strings.TrimSpace(string(data))
	}

	recipient, err := agessh.ParseRecipient(content)
	if err != nil {
		return nil, fmt.Errorf("解析 SSH 公钥失败 (%s): %w", content, err)
	}
	return recipient, nil
}

// ParseSSHPrivateKey 解析 SSH 私钥文件为 age.Identity
func ParseSSHPrivateKey(privKeyPath string, passphrase string) (age.Identity, error) {
	data, err := os.ReadFile(privKeyPath)
	if err != nil {
		return nil, fmt.Errorf("读取私钥文件失败: %w", err)
	}

	identity, err := agessh.ParseIdentity(data)
	if err == nil {
		return identity, nil
	}

	var missing *ssh.PassphraseMissingError
	if !errors.As(err, &missing) {
		return nil, fmt.Errorf("解析 SSH 私钥失败: %w", err)
	}
	if passphrase == "" {
		return nil, fmt.Errorf("SSH 私钥需要密码: %w", err)
	}

	raw, err := ssh.ParseRawPrivateKeyWithPassphrase(data, []byte(passphrase))
	if err != nil {
		return nil, fmt.Errorf("解密 SSH 私钥失败: %w", err)
	}
	switch key := raw.(type) {
	case *ed25519.PrivateKey:
		return agessh.NewEd25519Identity(*key)
	case ed25519.PrivateKey:
		return agessh.NewEd25519Identity(key)
	case *rsa.PrivateKey:
		return agessh.NewRSAIdentity(key)
	default:
		return nil, fmt.Errorf("不支持的 SSH 私钥类型: %T", raw)
	}
}

// IsPassphraseRequired 判断错误是否由加密 SSH 私钥需要密码导致。
func IsPassphraseRequired(err error) bool {
	var missing *ssh.PassphraseMissingError
	return errors.As(err, &missing)
}

// IsSSHPrivateKeyData 验证字节是否为受支持的明文或加密 SSH 私钥。
// 它只验证格式，不要求在扫描阶段取得私钥密码。
func IsSSHPrivateKeyData(data []byte) bool {
	key, err := ssh.ParseRawPrivateKey(data)
	if err == nil {
		return key != nil
	}
	var missing *ssh.PassphraseMissingError
	return errors.As(err, &missing)
}

// IsSSHPublicKeyData 验证 OpenSSH authorized_keys 格式的公钥。
func IsSSHPublicKeyData(data []byte) bool {
	key, _, _, _, err := ssh.ParseAuthorizedKey(data)
	return err == nil && key != nil
}

// Encrypt 使用一个或多个 Recipient 加密明文数据
func Encrypt(plainData []byte, recipients ...age.Recipient) ([]byte, error) {
	if len(recipients) == 0 {
		return nil, fmt.Errorf("未提供任何加密公钥/接收者")
	}

	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, recipients...)
	if err != nil {
		return nil, fmt.Errorf("初始化加密流失败: %w", err)
	}

	if _, err := w.Write(plainData); err != nil {
		return nil, fmt.Errorf("写入加密数据失败: %w", err)
	}

	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("关闭加密流失败: %w", err)
	}

	return buf.Bytes(), nil
}

// Decrypt 使用一个或多个 Identity 尝试解密密文数据
func Decrypt(cipherData []byte, identities ...age.Identity) ([]byte, error) {
	if len(identities) == 0 {
		return nil, fmt.Errorf("未提供任何解密私钥/身份")
	}

	r, err := age.Decrypt(bytes.NewReader(cipherData), identities...)
	if err != nil {
		return nil, fmt.Errorf("解密失败 (可能是私钥不匹配或数据损坏): %w", err)
	}

	var out bytes.Buffer
	if _, err := io.Copy(&out, r); err != nil {
		return nil, fmt.Errorf("读取解密数据失败: %w", err)
	}

	return out.Bytes(), nil
}
