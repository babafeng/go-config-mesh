package sync

import (
	"os"
	"path/filepath"
	"testing"

	"filippo.io/age"

	"config-mesh/internal/crypto"
)

func TestTarAndEncryptDirectoryFixture(t *testing.T) {
	srcDir := filepath.Join(t.TempDir(), "config")
	if err := os.MkdirAll(filepath.Join(srcDir, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "settings.json"), []byte(`{"theme":"dark"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "nested", "aliases"), []byte("alias ll='ls -la'\n"), 0600); err != nil {
		t.Fatal(err)
	}

	tarBytes, err := CreateTarArchive(srcDir)
	if err != nil {
		t.Fatalf("打包目录夹具失败: %v", err)
	}
	if len(tarBytes) == 0 {
		t.Fatal("tar 归档不应为空")
	}

	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	cipherBytes, err := crypto.Encrypt(tarBytes, identity.Recipient())
	if err != nil {
		t.Fatalf("Age 加密失败: %v", err)
	}
	plainBytes, err := crypto.Decrypt(cipherBytes, identity)
	if err != nil {
		t.Fatalf("Age 解密失败: %v", err)
	}
	if string(plainBytes) != string(tarBytes) {
		t.Fatal("加密往返后的 tar 数据不一致")
	}
}
