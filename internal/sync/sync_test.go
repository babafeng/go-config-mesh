package sync

import (
	"archive/tar"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"config-mesh/internal/model"
	"golang.org/x/crypto/ssh"
)

func TestApplyMarkedBlock(t *testing.T) {
	// 测试初次追加
	original := "export FOO=BAR\nalias ll='ls -la'\n"
	syncContent := "export PATH=$HOME/bin:$PATH\nalias cm=config-mesh"

	result := applyMarkedBlock(original, syncContent)
	if !strings.Contains(result, BlockStart) || !strings.Contains(result, BlockEnd) {
		t.Fatalf("输出缺少标记块")
	}
	if !strings.Contains(result, "alias cm=config-mesh") {
		t.Fatalf("输出未包含同步内容")
	}
	if !strings.Contains(result, "export FOO=BAR") {
		t.Fatalf("输出丢失了原有内容")
	}

	// 测试二次更新标记块（不产生重复块）
	newSyncContent := "export PATH=$HOME/bin:$PATH\nalias cm=config-mesh-v2"
	updatedResult := applyMarkedBlock(result, newSyncContent)

	if strings.Count(updatedResult, BlockStart) != 1 {
		t.Fatalf("更新后标记块出现多次重复: %d 次", strings.Count(updatedResult, BlockStart))
	}
	if !strings.Contains(updatedResult, "config-mesh-v2") {
		t.Fatalf("未能成功替换标记块中的新内容")
	}
}

func TestDeepMergeJSON(t *testing.T) {
	local := []byte(`{"editor.fontSize": 14, "workbench.colorTheme": "Default Dark"}`)
	remote := []byte(`{"editor.fontSize": 16, "editor.tabSize": 2}`)

	merged, err := deepMergeJSON(local, remote)
	if err != nil {
		t.Fatalf("deepMergeJSON 失败: %v", err)
	}

	mergedStr := string(merged)
	if !strings.Contains(mergedStr, `"editor.fontSize": 16`) {
		t.Errorf("未能合并 remote 字段: %s", mergedStr)
	}
	if !strings.Contains(mergedStr, `"workbench.colorTheme": "Default Dark"`) {
		t.Errorf("丢失了 local 字段: %s", mergedStr)
	}
	if !strings.Contains(mergedStr, `"editor.tabSize": 2`) {
		t.Errorf("未能添加 remote 新字段: %s", mergedStr)
	}
}

func TestDeepMergeJSONArray(t *testing.T) {
	local := []byte(`[
		{"key": "ctrl+c", "command": "copy"},
		{"key": "ctrl+v", "command": "paste"}
	]`)
	remote := []byte(`[
		{"key": "ctrl+v", "command": "paste"},
		{"key": "ctrl+x", "command": "cut"}
	]`)

	merged, err := deepMergeJSON(local, remote)
	if err != nil {
		t.Fatalf("合并 JSON Array 失败: %v", err)
	}

	mergedStr := string(merged)
	if !strings.Contains(mergedStr, "ctrl+c") || !strings.Contains(mergedStr, "ctrl+x") {
		t.Fatalf("合并后的 Array 缺少预期项: %s", mergedStr)
	}
	if strings.Count(mergedStr, "ctrl+v") != 1 {
		t.Fatalf("重复项未被去重: %s", mergedStr)
	}
}

func TestDeepMergeJSONC(t *testing.T) {
	local := []byte(`{
		// 基础配置
		"fontSize": 14, /* 行内注释 */
		"theme": "dark",
	}`)
	remote := []byte(`[
		// 注释
		1, 2, 3,
	]`)

	// 1. 测试带注释与逗号的对象合并
	remoteObj := []byte(`{
		/* 远端设置 */
		"tabSize": 4,
	}`)
	merged, err := deepMergeJSON(local, remoteObj)
	if err != nil {
		t.Fatalf("合并带注释的 JSONC 失败: %v", err)
	}
	if !strings.Contains(string(merged), `"fontSize": 14`) || !strings.Contains(string(merged), `"tabSize": 4`) {
		t.Fatalf("JSONC 合并结果异常: %s", string(merged))
	}

	// 2. 类型冲突测试
	if _, err := deepMergeJSON(local, remote); err == nil {
		t.Fatalf("Map 与 Array 类型冲突时必须报错")
	}
}

func TestApplyConfigItemSSHParentPerm(t *testing.T) {
	tmpDir := t.TempDir()
	sshDir := filepath.Join(tmpDir, ".ssh")
	keyPath := filepath.Join(sshDir, "id_ed25519")

	item := model.ConfigItem{
		ID:          "ssh_key",
		LocalPath:   keyPath,
		RelHomePath: ".ssh/id_ed25519",
		Category:    model.CategorySSH,
		SecretKind:  model.SecretKindSSHPrivateKey,
		FileMode:    0600,
	}

	err := ApplyConfigItem(item, []byte("fake-private-key"), model.StrategyOverwrite)
	if err != nil {
		t.Fatalf("应用 SSH 配置失败: %v", err)
	}

	info, err := os.Stat(sshDir)
	if err != nil {
		t.Fatalf("获取 .ssh 目录状态失败: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0700 {
		t.Fatalf("SSH 父目录权限必须为 0700，实际: %o", perm)
	}
}

func TestBackupAndRollback(t *testing.T) {
	tmpDir := t.TempDir()
	baseBackupDir := filepath.Join(tmpDir, "backups")
	bm := &BackupManager{BackupBaseDir: baseBackupDir, RestoreRoot: tmpDir}

	testFilePath := filepath.Join(tmpDir, "my_config.txt")
	originalContent := "hello original config"
	if err := os.WriteFile(testFilePath, []byte(originalContent), 0644); err != nil {
		t.Fatalf("写入测试文件失败: %v", err)
	}

	item := model.ConfigItem{
		ID:          "test_item",
		Name:        "my_config.txt",
		LocalPath:   testFilePath,
		RelHomePath: "my_config.txt",
		Exists:      true,
		Selected:    true,
	}

	// 1. 执行备份
	manifest, err := bm.BackupSelectedFiles([]model.ConfigItem{item})
	if err != nil {
		t.Fatalf("BackupSelectedFiles 失败: %v", err)
	}
	if len(manifest.Items) != 1 {
		t.Fatalf("备份项数量不符合预期")
	}

	// 2. 模拟配置被修改/覆盖
	if err := os.WriteFile(testFilePath, []byte("corrupted or new config"), 0644); err != nil {
		t.Fatalf("修改文件失败: %v", err)
	}

	// 3. 执行回滚
	if err := bm.Rollback(manifest.BackupID); err != nil {
		t.Fatalf("Rollback 失败: %v", err)
	}

	// 4. 验证回滚后内容是否恢复
	restoredContent, err := os.ReadFile(testFilePath)
	if err != nil {
		t.Fatalf("读取还原后文件失败: %v", err)
	}

	if string(restoredContent) != originalContent {
		t.Fatalf("还原内容不匹配: 期望 %s, 实际 %s", originalContent, string(restoredContent))
	}
}

func TestBackupAndRollbackRestoresLocalState(t *testing.T) {
	tmpDir := t.TempDir()
	baseBackupDir := filepath.Join(tmpDir, "backups")
	stateFile := filepath.Join(tmpDir, "test-state.json")

	initialState := model.LocalState{
		Version:          "1.0.0",
		VaultID:          "test/vault",
		RemoteSnapshotID: "snap-original",
		TrackedItems: map[string]model.TrackedItemState{
			"item1": {ID: "item1", LastLocalHash: "hash-initial"},
		},
	}
	initialBytes, _ := json.MarshalIndent(initialState, "", "  ")
	if err := os.WriteFile(stateFile, initialBytes, 0600); err != nil {
		t.Fatal(err)
	}

	testFilePath := filepath.Join(tmpDir, "file.txt")
	if err := os.WriteFile(testFilePath, []byte("initial-content"), 0644); err != nil {
		t.Fatal(err)
	}

	item := model.ConfigItem{
		ID:          "item1",
		LocalPath:   testFilePath,
		RelHomePath: "file.txt",
		Exists:      true,
		ContentHash: "hash-initial",
	}

	bm := &BackupManager{
		BackupBaseDir: baseBackupDir,
		RestoreRoot:   tmpDir,
		StateFilePath: stateFile,
		VaultID:       "test/vault",
	}

	manifest, err := bm.BackupSelectedFiles([]model.ConfigItem{item})
	if err != nil {
		t.Fatalf("备份失败: %v", err)
	}

	// 模拟同步覆盖：文件被篡改，状态库被写入新的远端快照和新哈希
	if err := os.WriteFile(testFilePath, []byte("new-corrupted-content"), 0644); err != nil {
		t.Fatal(err)
	}
	corruptedState := model.LocalState{
		Version:          "1.0.0",
		VaultID:          "test/vault",
		RemoteSnapshotID: "snap-corrupted",
		TrackedItems: map[string]model.TrackedItemState{
			"item1": {ID: "item1", LastLocalHash: "hash-corrupted"},
		},
	}
	corruptedBytes, _ := json.MarshalIndent(corruptedState, "", "  ")
	if err := os.WriteFile(stateFile, corruptedBytes, 0600); err != nil {
		t.Fatal(err)
	}

	// 执行回滚
	if err := bm.Rollback(manifest.BackupID); err != nil {
		t.Fatalf("回滚失败: %v", err)
	}

	// 验证文件内容已恢复
	fileContent, _ := os.ReadFile(testFilePath)
	if string(fileContent) != "initial-content" {
		t.Fatalf("文件未恢复: %s", string(fileContent))
	}

	// 验证状态库已同步还原为 initialState
	restoredStateBytes, _ := os.ReadFile(stateFile)
	var restoredState model.LocalState
	if err := json.Unmarshal(restoredStateBytes, &restoredState); err != nil {
		t.Fatal(err)
	}
	if restoredState.RemoteSnapshotID != "snap-original" {
		t.Fatalf("状态库 RemoteSnapshotID 未恢复: %s", restoredState.RemoteSnapshotID)
	}
	if restoredState.TrackedItems["item1"].LastLocalHash != "hash-initial" {
		t.Fatalf("状态库 LastLocalHash 未恢复: %s", restoredState.TrackedItems["item1"].LastLocalHash)
	}
}

func TestTarArchiveWithSymlinksAndSockets(t *testing.T) {
	tmpDir := t.TempDir()
	srcDir := filepath.Join(tmpDir, "src")
	dstDir := filepath.Join(tmpDir, "dst")

	_ = os.MkdirAll(filepath.Join(srcDir, "sub"), 0755)

	// 1. 普通文件
	regularFile := filepath.Join(srcDir, "sub", "config.yaml")
	_ = os.WriteFile(regularFile, []byte("key: value"), 0644)

	// 2. 软链接目录 (模拟 skills 软链接)
	realSkillDir := filepath.Join(srcDir, "external_skill")
	_ = os.MkdirAll(realSkillDir, 0755)
	_ = os.WriteFile(filepath.Join(realSkillDir, "SKILL.md"), []byte("# Skill Content"), 0644)

	symlinkPath := filepath.Join(srcDir, "skill_link")
	_ = os.Symlink("external_skill", symlinkPath)

	// 3. Unix Domain Socket (模拟 SSH agent / controlpath socket)
	sockPath := filepath.Join(srcDir, "agent.sock")
	l, err := net.Listen("unix", sockPath)
	if err == nil {
		defer l.Close()
	}

	// 打包测试
	tarBytes, err := CreateTarArchive(srcDir)
	if err != nil {
		t.Fatalf("CreateTarArchive 失败: %v", err)
	}

	if len(tarBytes) == 0 {
		t.Fatalf("生成的 tar 归档为空")
	}

	// 解包测试
	err = applyTarArchive(dstDir, tarBytes, model.StrategyOverwrite)
	if err != nil {
		t.Fatalf("applyTarArchive 失败: %v", err)
	}

	// 验证常规文件
	if _, err := os.Stat(filepath.Join(dstDir, "sub", "config.yaml")); err != nil {
		t.Errorf("解压后常规文件丢失: %v", err)
	}

	// 验证软链接已还原
	if linkInfo, err := os.Lstat(filepath.Join(dstDir, "skill_link")); err != nil || linkInfo.Mode()&os.ModeSymlink == 0 {
		t.Errorf("解压后软链接未正确创建")
	}

	// 验证 socket 文件被安全忽略，没有被解压出来
	if _, err := os.Stat(filepath.Join(dstDir, "agent.sock")); err == nil {
		t.Errorf("Socket 文件本应被忽略")
	}
}

func TestPreviewTarArchiveMatchesPackedEntriesAndExplainsSkips(t *testing.T) {
	srcDir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		path := filepath.Join(srcDir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("settings.json", `{"theme":"dark"}`)
	write("skills/demo/SKILL.md", "# Demo")
	write("cache/generated.json", `{"cached":true}`)
	write("auth.json", `{"account":"local"}`)
	write("mcp_config.json", `{"token":"literal-production-secret"}`)

	preview, err := PreviewTarArchive(srcDir)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := CreateTarArchive(srcDir)
	if err != nil {
		t.Fatal(err)
	}

	packed := make(map[string]bool)
	tr := tar.NewReader(bytes.NewReader(archive))
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		packed[header.Name] = true
	}

	wantSkipped := map[string]string{
		"auth.json":       "敏感路径",
		"cache":           "忽略规则",
		"mcp_config.json": "内容检测到凭据",
	}
	for _, entry := range preview.Entries {
		if entry.Included != packed[entry.Path] {
			t.Errorf("预览与 tar 不一致: %+v", entry)
		}
		if reason, ok := wantSkipped[entry.Path]; ok {
			if entry.Included || entry.Reason != reason {
				t.Errorf("跳过原因不正确: %+v", entry)
			}
			delete(wantSkipped, entry.Path)
		}
	}
	if len(wantSkipped) != 0 {
		t.Fatalf("预览缺少跳过条目: %v", wantSkipped)
	}
	if preview.IncludedCount != len(packed) || preview.SkippedCount != 3 {
		t.Fatalf("预览汇总不正确: %+v", preview)
	}
	if packed["cache/generated.json"] || packed["auth.json"] || packed["mcp_config.json"] {
		t.Fatalf("tar 包含应跳过的文件: %v", packed)
	}
}

func TestValidateTarArchiveRejectsTraversal(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	payload := []byte("owned")
	if err := tw.WriteHeader(&tar.Header{Name: "../../escape", Mode: 0644, Size: int64(len(payload)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ValidateTarArchive(buf.Bytes()); err == nil {
		t.Fatal("应拒绝路径穿越 tar")
	}
}

func TestValidateTarArchiveRejectsSensitiveEntries(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
	}{
		{name: ".env", content: "DEBUG=true\n"},
		{name: "settings.json", content: `{"token":"literal-production-secret"}`},
	} {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		data := []byte(tc.content)
		if err := tw.WriteHeader(&tar.Header{Name: tc.name, Mode: 0600, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := ValidateTarArchive(buf.Bytes()); err == nil {
			t.Errorf("应拒绝敏感 tar 条目 %s", tc.name)
		}
	}
}

func TestValidateConfigItemRejectsEscapesAndSecrets(t *testing.T) {
	home := t.TempDir()
	cases := []model.ConfigItem{
		{ID: "escape", RelHomePath: "../../outside", VaultFile: "escape.age"},
		{ID: "vault_escape", RelHomePath: ".zshrc", VaultFile: "../escape.age"},
		{ID: "private_key", RelHomePath: ".ssh/id_ed25519", VaultFile: "key.age"},
	}
	for _, item := range cases {
		if _, err := ValidateAndResolveConfigItem(home, item); err == nil {
			t.Errorf("应拒绝非法配置项: %+v", item)
		}
	}
}

func TestValidateConfigItemAllowsOnlyWhitelistedCredentials(t *testing.T) {
	home := t.TempDir()
	allowed := []model.ConfigItem{
		{ID: "aws", RelHomePath: ".aws/credentials", VaultFile: "aws.age", SecretKind: model.SecretKindAWSCredentials},
		{ID: "aliyun", RelHomePath: ".aliyun/config.json", VaultFile: "aliyun.age", SecretKind: model.SecretKindAliyunConfig},
		{ID: "ssh", RelHomePath: ".ssh/work-prod", VaultFile: "ssh.age", SecretKind: model.SecretKindSSHPrivateKey},
		{ID: "zshrc", RelHomePath: ".zshrc", VaultFile: "zshrc.age", SecretKind: model.SecretKindDetectedConfig},
	}
	for _, item := range allowed {
		resolved, err := ValidateAndResolveConfigItem(home, item)
		if err != nil {
			t.Errorf("应允许受控凭据 %+v: %v", item, err)
			continue
		}
		if resolved.FileMode != 0600 {
			t.Errorf("凭据权限必须强制为 0600: %+v", resolved)
		}
	}
	for _, item := range []model.ConfigItem{
		{ID: "evil", RelHomePath: ".ssh/authorized_keys", VaultFile: "evil.age", SecretKind: model.SecretKindSSHPrivateKey},
		{ID: "wrong", RelHomePath: ".aws/credentials", VaultFile: "wrong.age", SecretKind: model.SecretKindAliyunConfig},
		{ID: "untyped", RelHomePath: ".aws/credentials", VaultFile: "untyped.age"},
	} {
		if _, err := ValidateAndResolveConfigItem(home, item); err == nil {
			t.Errorf("应拒绝未授权的凭据路径映射: %+v", item)
		}
	}
}

func TestValidateSensitivePayload(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(privateKey, "")
	if err != nil {
		t.Fatal(err)
	}
	privateData := pem.EncodeToMemory(block)
	tests := []struct {
		item model.ConfigItem
		data []byte
		ok   bool
	}{
		{item: model.ConfigItem{SecretKind: model.SecretKindSSHPrivateKey}, data: privateData, ok: true},
		{item: model.ConfigItem{SecretKind: model.SecretKindSSHPrivateKey}, data: []byte("not a key")},
		{item: model.ConfigItem{SecretKind: model.SecretKindAWSCredentials}, data: []byte("[default]\naws_access_key_id=AKIAIOSFODNN7EXAMPLE\naws_secret_access_key=secret\n"), ok: true},
		{item: model.ConfigItem{SecretKind: model.SecretKindAWSCredentials}, data: []byte("[default]\nregion=us-east-1\n")},
		{item: model.ConfigItem{SecretKind: model.SecretKindAliyunConfig}, data: []byte(`{"profiles":[]}`), ok: true},
		{item: model.ConfigItem{SecretKind: model.SecretKindAliyunConfig}, data: []byte("not json")},
		{item: model.ConfigItem{SecretKind: model.SecretKindDetectedConfig}, data: []byte(`export API_TOKEN="literal-production-secret"`), ok: true},
		{item: model.ConfigItem{SecretKind: model.SecretKindDetectedConfig}, data: []byte("export EDITOR=vim\n")},
		{item: model.ConfigItem{}, data: []byte(`token="literal-production-secret"`)},
		{item: model.ConfigItem{}, data: []byte("theme=dark\n"), ok: true},
	}
	for i, tc := range tests {
		err := ValidateSensitivePayload(tc.item, tc.data)
		if (err == nil) != tc.ok {
			t.Errorf("case %d: ok=%v err=%v", i, tc.ok, err)
		}
	}
	if err := ValidateApplyStrategy(model.ConfigItem{RelHomePath: ".aws/credentials", SecretKind: model.SecretKindAWSCredentials}, model.StrategyAppend); err == nil {
		t.Fatal("凭据不得使用追加合并策略")
	}
}

func TestApplyCredentialForcesPrivatePermissions(t *testing.T) {
	home := t.TempDir()
	item, err := ValidateAndResolveConfigItem(home, model.ConfigItem{
		ID: "aws", RelHomePath: ".aws/credentials", VaultFile: "aws.age",
		FileMode: 0777, SecretKind: model.SecretKindAWSCredentials,
	})
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("[default]\naws_access_key_id=AKIAIOSFODNN7EXAMPLE\naws_secret_access_key=secret\n")
	if err := ValidateSensitivePayload(item, data); err != nil {
		t.Fatal(err)
	}
	if err := ApplyConfigItem(item, data, model.StrategyOverwrite); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(home, ".aws", "credentials"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("凭据落盘权限必须为 0600，实际 %o", info.Mode().Perm())
	}
}

func TestValidateConfigItemRejectsSymlinkParent(t *testing.T) {
	home := t.TempDir()
	external := t.TempDir()
	if err := os.Symlink(external, filepath.Join(home, ".config")); err != nil {
		t.Skipf("当前文件系统不支持软链接: %v", err)
	}
	item := model.ConfigItem{
		ID: "nvim", RelHomePath: ".config/nvim", VaultFile: "nvim.tar.age", IsDir: true,
	}
	if _, err := ValidateAndResolveConfigItem(home, item); err == nil {
		t.Fatal("应拒绝通过软链接父目录写到家目录之外")
	}
}

func TestRollbackRemovesNewlyCreatedItem(t *testing.T) {
	tmpDir := t.TempDir()
	bm := &BackupManager{
		BackupBaseDir: filepath.Join(tmpDir, "backups"),
		RestoreRoot:   tmpDir,
	}
	newPath := filepath.Join(tmpDir, "new-config")
	item := model.ConfigItem{
		ID: "new_config", LocalPath: newPath, RelHomePath: "new-config", Selected: true,
	}
	manifest, err := bm.BackupSelectedFiles([]model.ConfigItem{item})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Items) != 1 || manifest.Items[0].Exists {
		t.Fatalf("备份应记录“原本不存在”: %+v", manifest.Items)
	}
	if err := os.WriteFile(newPath, []byte("created by sync"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := bm.Rollback(manifest.BackupID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(newPath); !os.IsNotExist(err) {
		t.Fatalf("回滚应删除本次新建文件: %v", err)
	}
}

func TestApplyDeletionTombstone(t *testing.T) {
	target := filepath.Join(t.TempDir(), "deleted-config")
	if err := os.WriteFile(target, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	item := model.ConfigItem{ID: "deleted", LocalPath: target, RelHomePath: "deleted-config", Deleted: true}
	if err := ApplyConfigItem(item, nil, model.StrategyOverwrite); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("tombstone 应删除目标: %v", err)
	}
}

func TestTarArchiveExcludesSensitiveContent(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "settings.json"), []byte(`{"theme":"dark"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "mcp.json"), []byte(`{"api_key":"literal-production-secret"}`), 0600); err != nil {
		t.Fatal(err)
	}
	archive, err := CreateTarArchive(src)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "restored")
	if err := applyTarArchive(dst, archive, model.StrategyOverwrite); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "settings.json")); err != nil {
		t.Fatalf("普通配置应保留: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "mcp.json")); !os.IsNotExist(err) {
		t.Fatalf("含明文凭据的文件应从归档排除: %v", err)
	}
}

func TestBackupFailureRemovesIncompleteSnapshot(t *testing.T) {
	root := t.TempDir()
	backupDir := filepath.Join(root, "backups")
	socketPath := filepath.Join(root, "config.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Skipf("当前文件系统不支持 Unix socket: %v", err)
	}
	defer listener.Close()
	bm := &BackupManager{BackupBaseDir: backupDir, RestoreRoot: root}
	item := model.ConfigItem{ID: "socket", LocalPath: socketPath, RelHomePath: "config.sock", Selected: true}
	if _, err := bm.BackupSelectedFiles([]model.ConfigItem{item}); err == nil {
		t.Fatal("特殊文件备份应失败")
	}
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("失败备份不应残留不完整快照: %+v", entries)
	}
}

func TestRollbackPrevalidatesAllBackupContent(t *testing.T) {
	root := t.TempDir()
	bm := &BackupManager{BackupBaseDir: filepath.Join(root, "backups"), RestoreRoot: root}
	firstPath := filepath.Join(root, "first")
	secondPath := filepath.Join(root, "second")
	for _, path := range []string{firstPath, secondPath} {
		if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	items := []model.ConfigItem{
		{ID: "first", LocalPath: firstPath, RelHomePath: "first", Selected: true},
		{ID: "second", LocalPath: secondPath, RelHomePath: "second", Selected: true},
	}
	manifest, err := bm.BackupSelectedFiles(items)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{firstPath, secondPath} {
		if err := os.WriteFile(path, []byte("modified"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(filepath.Join(manifest.BackupDir, "second")); err != nil {
		t.Fatal(err)
	}
	if err := bm.Rollback(manifest.BackupID); err == nil {
		t.Fatal("缺失备份内容时回滚应失败")
	}
	firstData, err := os.ReadFile(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstData) != "modified" {
		t.Fatalf("预检失败前不得部分还原第一个文件: %q", firstData)
	}
}
