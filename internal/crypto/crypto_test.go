package crypto

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestAgeEncryptionWithSSHKey(t *testing.T) {
	// 生成临时的 ed25519 SSH 密钥对
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成测试密钥失败: %v", err)
	}

	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("生成 SSH 公钥失败: %v", err)
	}
	pubBytes := ssh.MarshalAuthorizedKey(sshPub)

	privPem, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("生成私钥 PEM 失败: %v", err)
	}
	privPemBytes := pem.EncodeToMemory(privPem)

	tmpDir := t.TempDir()
	pubPath := filepath.Join(tmpDir, "id_ed25519.pub")
	privPath := filepath.Join(tmpDir, "id_ed25519")

	if err := os.WriteFile(pubPath, pubBytes, 0600); err != nil {
		t.Fatalf("写入公钥失败: %v", err)
	}
	if err := os.WriteFile(privPath, privPemBytes, 0600); err != nil {
		t.Fatalf("写入私钥失败: %v", err)
	}

	// 1. 解析公钥并加密
	recipient, err := ParseSSHPublicKey(pubPath)
	if err != nil {
		t.Fatalf("ParseSSHPublicKey 失败: %v", err)
	}

	plainText := []byte("hello config-mesh, this is secret dotfiles!")
	cipherText, err := Encrypt(plainText, recipient)
	if err != nil {
		t.Fatalf("Encrypt 失败: %v", err)
	}

	if len(cipherText) == 0 || string(cipherText) == string(plainText) {
		t.Fatalf("加密输出异常")
	}

	// 2. 解析私钥并解密
	identity, err := ParseSSHPrivateKey(privPath, "")
	if err != nil {
		t.Fatalf("ParseSSHPrivateKey 失败: %v", err)
	}

	decryptedText, err := Decrypt(cipherText, identity)
	if err != nil {
		t.Fatalf("Decrypt 失败: %v", err)
	}

	if string(decryptedText) != string(plainText) {
		t.Fatalf("解密结果不匹配: 期望 %s, 实际 %s", string(plainText), string(decryptedText))
	}
}

func TestSHA256(t *testing.T) {
	data := []byte("test-data")
	hash := CalculateSHA256(data)
	if hash == "" {
		t.Errorf("哈希值为空")
	}
}

func TestEncryptedSSHPrivateKey(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "test", []byte("correct horse"))
	if err != nil {
		t.Fatal(err)
	}
	tmpDir := t.TempDir()
	privPath := filepath.Join(tmpDir, "id_ed25519")
	if err := os.WriteFile(privPath, pem.EncodeToMemory(block), 0600); err != nil {
		t.Fatal(err)
	}
	if !IsSSHPrivateKeyData(pem.EncodeToMemory(block)) {
		t.Fatal("加密 SSH 私钥应在不获取密码时通过格式识别")
	}
	if _, err := ParseSSHPrivateKey(privPath, ""); err == nil || !IsPassphraseRequired(err) {
		t.Fatalf("未提供密码时应报告需要 passphrase: %v", err)
	}
	identity, err := ParseSSHPrivateKey(privPath, "correct horse")
	if err != nil {
		t.Fatalf("加密 SSH 私钥应可解锁: %v", err)
	}
	recipient, err := ParseSSHPublicKey(string(ssh.MarshalAuthorizedKey(sshPub)))
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := Encrypt([]byte("secret"), recipient)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := Decrypt(ciphertext, identity)
	if err != nil || string(plaintext) != "secret" {
		t.Fatalf("加密 SSH 私钥解密失败: %q, %v", plaintext, err)
	}
}

func TestRecipientsRegistryRejectsSymlink(t *testing.T) {
	_, pub, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub.Public())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "real-recipients.pub")
	if err := os.WriteFile(target, ssh.MarshalAuthorizedKey(sshPub), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "recipients.pub")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("当前文件系统不支持软链接: %v", err)
	}
	if _, err := LoadRecipients(link); err == nil {
		t.Fatal("接收者注册表不得是软链接")
	}
}

func TestSSHDefaultKeyPriority(t *testing.T) {
	if p := sshDefaultKeyPriority("id_ed25519.pub"); p != 0 {
		t.Fatalf("id_ed25519 优先级必须为最高 0，实际: %d", p)
	}
	if p := sshDefaultKeyPriority("id_rsa.pub"); p != 1 {
		t.Fatalf("id_rsa 优先级必须为 1，实际: %d", p)
	}
	if p := sshDefaultKeyPriority("aws_key.pub"); p <= 1 {
		t.Fatalf("自定义 key 优先级必须靠后，实际: %d", p)
	}
}
