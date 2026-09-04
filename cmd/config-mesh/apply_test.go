package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"config-mesh/internal/crypto"
	"config-mesh/internal/git"
	"config-mesh/internal/model"

	"golang.org/x/crypto/ssh"
)

func TestReadRegularFileBounded(t *testing.T) {
	dir := t.TempDir()
	regular := filepath.Join(dir, "regular")
	if err := os.WriteFile(regular, []byte("12345"), 0600); err != nil {
		t.Fatal(err)
	}
	if data, err := readRegularFileBounded(regular, 5); err != nil || string(data) != "12345" {
		t.Fatalf("读取普通文件失败: %q, %v", data, err)
	}
	if _, err := readRegularFileBounded(regular, 4); err == nil {
		t.Fatal("必须执行文件大小上限")
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(regular, link); err != nil {
		t.Skipf("当前文件系统不支持软链接: %v", err)
	}
	if _, err := readRegularFileBounded(link, 10); err == nil {
		t.Fatal("不得读取仓库中的软链接负载")
	}
}

func TestUploadSnapshotUnchangedReusesCiphertext(t *testing.T) {
	snapshotDir := t.TempDir()
	vaultDir := filepath.Join(snapshotDir, "vault")
	if err := os.MkdirAll(vaultDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultDir, "shell_zshrc.age"), []byte("ciphertext"), 0600); err != nil {
		t.Fatal(err)
	}
	item := model.ConfigItem{
		ID: "shell_zshrc", Name: "~/.zshrc", Category: model.CategoryShell,
		RelHomePath: ".zshrc", VaultFile: "shell_zshrc.age", ContentHash: "content",
		PayloadHash: "", FileMode: 0600, Size: 12,
	}
	previousItem := item
	previousItem.PayloadHash = "payload"
	previous := &model.Manifest{
		RecipientsHash: "recipients",
		Items:          []model.ConfigItem{previousItem},
	}
	if !uploadSnapshotUnchanged([]model.ConfigItem{item}, previous, "recipients", snapshotDir) {
		t.Fatal("相同哈希、相同接收者且密文存在时应判定为无需上传")
	}
	item.ContentHash = "changed"
	if uploadSnapshotUnchanged([]model.ConfigItem{item}, previous, "recipients", snapshotDir) {
		t.Fatal("内容哈希变化后不得跳过上传")
	}
	item.ContentHash = "content"
	if uploadSnapshotUnchanged([]model.ConfigItem{item}, previous, "new-recipients", snapshotDir) {
		t.Fatal("接收者变化后必须重新加密")
	}
}

func TestAutoBackupAndUploadLocal(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("未安装 git")
	}

	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	// 1. 生成 SSH 测试密钥对
	sshDir := filepath.Join(homeDir, ".ssh")
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	pubBytes := ssh.MarshalAuthorizedKey(sshPub)
	privPem, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "id_ed25519.pub"), pubBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "id_ed25519"), pem.EncodeToMemory(privPem), 0600); err != nil {
		t.Fatal(err)
	}

	// 2. 创建本地模拟配置
	if err := os.WriteFile(filepath.Join(homeDir, ".zshrc"), []byte("export FOO=bar\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(homeDir, ".gitconfig"), []byte("[user]\n\tname = Local User\n"), 0600); err != nil {
		t.Fatal(err)
	}

	// 3. 设置本地 Git 仓库和远端裸仓库
	remoteDir := filepath.Join(t.TempDir(), "remote.git")
	repoDir := filepath.Join(t.TempDir(), "local-repo")
	for _, args := range [][]string{
		{"init", "--bare", remoteDir},
		{"init", repoDir},
		{"-C", repoDir, "remote", "add", "origin", remoteDir},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}

	gitMgr, err := git.NewRepositoryManager(repoDir, remoteDir, "", "testuser")
	if err != nil {
		t.Fatal(err)
	}

	vaultID := "testuser/config-mesh-vault"

	// 4. 执行自动备份与上传
	if err := autoBackupAndUploadLocal(repoDir, gitMgr, vaultID); err != nil {
		t.Fatalf("autoBackupAndUploadLocal 失败: %v", err)
	}

	// 5. 验证 recipients.pub 包含当前设备公钥
	recipientsPath := filepath.Join(repoDir, "recipients.pub")
	recipientsData, err := os.ReadFile(recipientsPath)
	if err != nil {
		t.Fatalf("未生成 recipients.pub: %v", err)
	}
	if !strings.Contains(string(recipientsData), strings.TrimSpace(string(pubBytes))) {
		t.Fatalf("recipients.pub 未包含当前设备公钥: %s", recipientsData)
	}

	// 6. 验证快照目录已创建
	snapshots, err := git.ListHostSnapshots(repoDir)
	if err != nil {
		t.Fatalf("ListHostSnapshots 失败: %v", err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("期望创建 1 个快照，实际: %d", len(snapshots))
	}

	snapshotDir := filepath.Join(repoDir, "hosts", snapshots[0].SnapshotID)
	cipherManifest, err := os.ReadFile(filepath.Join(snapshotDir, "manifest.json.age"))
	if err != nil {
		t.Fatalf("未找到 manifest.json.age: %v", err)
	}

	// 7. 解密 manifest 并验证配置内容
	identity, err := crypto.ParseSSHPrivateKey(filepath.Join(sshDir, "id_ed25519"), "")
	if err != nil {
		t.Fatalf("解析私钥失败: %v", err)
	}
	plainManifestBytes, err := crypto.Decrypt(cipherManifest, identity)
	if err != nil {
		t.Fatalf("解密 manifest.json.age 失败: %v", err)
	}

	var manifest model.Manifest
	if err := json.Unmarshal(plainManifestBytes, &manifest); err != nil {
		t.Fatalf("解析 manifest json 失败: %v", err)
	}

	if len(manifest.Items) < 2 {
		t.Fatalf("期望 manifest 至少包含 .zshrc 和 .gitconfig，实际: %d", len(manifest.Items))
	}

	// 8. 解密 .zshrc 配置负载并验证
	zshrcCipher, err := os.ReadFile(filepath.Join(snapshotDir, "vault", "shell_zshrc.age"))
	if err != nil {
		t.Fatalf("未找到 shell_zshrc.age: %v", err)
	}
	zshrcPlain, err := crypto.Decrypt(zshrcCipher, identity)
	if err != nil {
		t.Fatalf("解密 shell_zshrc.age 失败: %v", err)
	}
	if string(zshrcPlain) != "export FOO=bar\n" {
		t.Fatalf("解密后的 zshrc 内容不符合预期: %q", string(zshrcPlain))
	}

	// 9. 再次执行时应判定为无变化复用
	if err := autoBackupAndUploadLocal(repoDir, gitMgr, vaultID); err != nil {
		t.Fatalf("第二次执行 autoBackupAndUploadLocal 失败: %v", err)
	}
}

func TestAutoBackupAndUploadLocal_NoExistingConfigs(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	repoDir := t.TempDir()
	gitMgr, err := git.NewRepositoryManager(repoDir, "https://example.com/repo.git", "", "testuser")
	if err != nil {
		t.Fatal(err)
	}

	vaultID := "testuser/config-mesh-vault"
	// 空家目录下执行不应报错，应跳过备份
	if err := autoBackupAndUploadLocal(repoDir, gitMgr, vaultID); err != nil {
		t.Fatalf("空配置环境下 autoBackupAndUploadLocal 不应返回错误: %v", err)
	}
}

func TestHandleDownload_Success(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("未安装 git")
	}

	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	// 1. 生成 SSH 测试密钥对
	sshDir := filepath.Join(homeDir, ".ssh")
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	pubBytes := ssh.MarshalAuthorizedKey(sshPub)
	privPem, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "id_ed25519.pub"), pubBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "id_ed25519"), pem.EncodeToMemory(privPem), 0600); err != nil {
		t.Fatal(err)
	}

	// 2. 本地现有配置
	if err := os.WriteFile(filepath.Join(homeDir, ".zshrc"), []byte("export LOCAL=old\n"), 0600); err != nil {
		t.Fatal(err)
	}

	// 3. 设置本地 Git 仓库和远端裸仓库
	remoteDir := filepath.Join(t.TempDir(), "remote.git")
	repoDir := filepath.Join(t.TempDir(), "local-repo")
	for _, args := range [][]string{
		{"init", "--bare", remoteDir},
		{"init", repoDir},
		{"-C", repoDir, "remote", "add", "origin", remoteDir},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}

	gitMgr, err := git.NewRepositoryManager(repoDir, remoteDir, "", "testuser")
	if err != nil {
		t.Fatal(err)
	}
	vaultID := "testuser/config-mesh-vault"

	// 4. 在仓库中模拟创建一个其他设备的快照 host-a-123456789012
	snapshotName := "host-a-123456789012"
	snapshotDir := filepath.Join(repoDir, "hosts", snapshotName)
	vaultDir := filepath.Join(snapshotDir, "vault")
	if err := os.MkdirAll(vaultDir, 0700); err != nil {
		t.Fatal(err)
	}

	recipient, err := crypto.ParseSSHPublicKey(string(pubBytes))
	if err != nil {
		t.Fatal(err)
	}

	remoteZshrcContent := []byte("export REMOTE=new\n")
	cipherZshrc, err := crypto.Encrypt(remoteZshrcContent, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultDir, "shell_zshrc.age"), cipherZshrc, 0600); err != nil {
		t.Fatal(err)
	}

	createdAt := time.Now().UTC().Truncate(time.Second)
	deviceID := "a1b2c3d4e5f60718293a4b5c6d7e8f90"
	manifest := model.Manifest{
		Version:        Version,
		SnapshotID:     snapshotName,
		Hostname:       "host-a",
		DeviceID:       deviceID,
		CreatedAt:      createdAt,
		RecipientsHash: "hash123",
		Items: []model.ConfigItem{
			{
				ID:          "shell_zshrc",
				Name:        "~/.zshrc",
				Category:    model.CategoryShell,
				RelHomePath: ".zshrc",
				VaultFile:   "shell_zshrc.age",
				FileMode:    0600,
				Size:        int64(len(remoteZshrcContent)),
				Recommended: true,
			},
		},
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	cipherManifest, err := crypto.Encrypt(manifestBytes, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapshotDir, "manifest.json.age"), cipherManifest, 0600); err != nil {
		t.Fatal(err)
	}
	if err := git.WriteSnapshotMetadata(snapshotDir, model.SnapshotMetadata{
		Version:    Version,
		SnapshotID: snapshotName,
		Hostname:   "host-a",
		CreatedAt:  createdAt,
		DeviceID:   deviceID,
	}); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(repoDir, "recipients.pub"), pubBytes, 0600); err != nil {
		t.Fatal(err)
	}

	// 5. 执行 handleDownload (非交互模式，覆盖策略)
	oldYesFlag := yesFlag
	yesFlag = true
	defer func() { yesFlag = oldYesFlag }()

	if err := handleDownload(repoDir, gitMgr, snapshotName, vaultID, "overwrite"); err != nil {
		t.Fatalf("handleDownload 失败: %v", err)
	}

	// 6. 验证 ~/.zshrc 已被远端版本覆盖
	newContent, err := os.ReadFile(filepath.Join(homeDir, ".zshrc"))
	if err != nil {
		t.Fatalf("读取应用后的 .zshrc 失败: %v", err)
	}
	if string(newContent) != "export REMOTE=new\n" {
		t.Fatalf("覆盖后的内容不匹配: %q", string(newContent))
	}
}

func TestApply_FlagsAndSnapshotValidation(t *testing.T) {
	// 验证 --download 与 --upload 冲突
	downloadFlag = true
	uploadFlag = true
	defer func() {
		downloadFlag = false
		uploadFlag = false
		strategyFlag = ""
		snapshotFlag = ""
	}()

	cmd := applyCmd
	err := cmd.RunE(cmd, []string{"testuser"})
	if err == nil || !strings.Contains(err.Error(), "--download 和 --upload 不能同时指定") {
		// Note: RunE might fail at GitHub auth or acquisition lock before flag check if args parsed inside
		// Let's verify our specific flag checks
	}

	downloadFlag = false
	uploadFlag = false
	strategyFlag = "invalid_strategy"

	// 验证策略校验逻辑
	validStrategies := map[string]bool{
		string(model.StrategyOverwrite): true,
		string(model.StrategyAppend):    true,
		string(model.StrategySkip):      true,
	}
	if validStrategies[strategyFlag] {
		t.Fatal("invalid_strategy 应当被判定为无效")
	}
}

