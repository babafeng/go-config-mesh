package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"config-mesh/internal/model"
)

func TestStateSaveAndLoad(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	sm := &StateManager{StateFilePath: stateFile}

	// 1. 首次加载空状态
	state, err := sm.LoadState()
	if err != nil {
		t.Fatalf("LoadState 失败: %v", err)
	}
	if state.Version != StateVersion {
		t.Errorf("Version 不匹配: %s", state.Version)
	}

	// 2. 模拟写入已同步项
	item := model.ConfigItem{
		ID:          "shell_zshrc",
		RelHomePath: ".zshrc",
		Selected:    true,
		Strategy:    model.StrategyAppend,
	}

	err = sm.RecordSyncSuccess([]model.ConfigItem{item}, "snapshot-20260901")
	if err != nil {
		t.Fatalf("RecordSyncSuccess 失败: %v", err)
	}

	// 3. 再次加载验证
	loadedState, err := sm.LoadState()
	if err != nil {
		t.Fatalf("再次 LoadState 失败: %v", err)
	}
	if loadedState.RemoteSnapshotID != "snapshot-20260901" {
		t.Errorf("RemoteSnapshotID 不匹配: %s", loadedState.RemoteSnapshotID)
	}
	tracked, ok := loadedState.TrackedItems["shell_zshrc"]
	if !ok || !tracked.Selected {
		t.Errorf("TrackedItems 未成功记录 shell_zshrc 勾选状态")
	}
}

func TestDeviceIDAndUploadStateArePersistent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sm, err := NewStateManagerForVault("alice/config-mesh-vault")
	if err != nil {
		t.Fatal(err)
	}
	localState, err := sm.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	firstID, err := sm.EnsureDeviceID(localState)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := sm.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := sm.EnsureDeviceID(reloaded)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstID) != 32 || firstID != secondID {
		t.Fatalf("设备 ID 没有稳定持久化: first=%q second=%q", firstID, secondID)
	}

	configPath := filepath.Join(t.TempDir(), ".zshrc")
	if err := os.WriteFile(configPath, []byte("export FOO=1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	zshrcHash, err := CalculateItemHash(model.ConfigItem{LocalPath: configPath})
	if err != nil {
		t.Fatal(err)
	}
	allItems := []model.ConfigItem{
		{ID: "shell_zshrc", Name: "~/.zshrc", LocalPath: configPath, RelHomePath: ".zshrc", VaultFile: "shell_zshrc.age", Selected: true, ContentHash: zshrcHash, PayloadHash: "payload"},
		{ID: "shell_bashrc", Name: "~/.bashrc", RelHomePath: ".bashrc", Selected: false},
	}
	if err := sm.RecordUploadSuccess(allItems, allItems[:1], "mac-0123456789ab", "recipients-hash"); err != nil {
		t.Fatal(err)
	}
	recorded, err := sm.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if recorded.UploadSnapshotID != "mac-0123456789ab" || recorded.LastUploadedAt.IsZero() ||
		recorded.RecipientsHash != "recipients-hash" {
		t.Fatalf("上传状态没有完整记录: %+v", recorded)
	}
	if tracked := recorded.TrackedItems["shell_zshrc"]; tracked.LastLocalHash == "" ||
		tracked.PayloadHash != "payload" || tracked.LastSyncedAt.IsZero() {
		t.Fatalf("文件哈希表没有完整记录: %+v", tracked)
	}
	if recorded.TrackedItems["shell_bashrc"].Selected {
		t.Fatal("未选择项的偏好没有记录")
	}
}

func TestAnalyzeDiff(t *testing.T) {
	tmpDir := t.TempDir()
	zshrcPath := filepath.Join(tmpDir, ".zshrc")
	_ = os.WriteFile(zshrcPath, []byte("export FOO=1\n"), 0644)

	localItem := model.ConfigItem{
		ID:          "shell_zshrc",
		Name:        "~/.zshrc",
		LocalPath:   zshrcPath,
		RelHomePath: ".zshrc",
		Exists:      true,
		Recommended: true,
	}

	state := &model.LocalState{
		TrackedItems: map[string]model.TrackedItemState{
			"shell_zshrc": {
				ID:             "shell_zshrc",
				LastLocalHash:  "old_hash",
				LastRemoteHash: "old_hash",
				Selected:       true,
				UpdatedAt:      time.Now(),
			},
		},
	}

	// 模拟当前远端哈希仍为 old_hash，本地被修改
	remoteManifest := &model.Manifest{
		Items: []model.ConfigItem{
			{ID: "shell_zshrc", ContentHash: "old_hash"},
		},
	}

	diffs, err := AnalyzeDiff([]model.ConfigItem{localItem}, remoteManifest, state)
	if err != nil {
		t.Fatal(err)
	}
	if len(diffs) != 1 {
		t.Fatalf("返回差异数量不符合预期")
	}

	if diffs[0].DiffStatus != model.DiffStatusLocalModified {
		t.Errorf("期望状态为 local_modified，实际为: %s", diffs[0].DiffStatus)
	}
}

func TestIncludeTrackedMissingItemsCreatesTombstone(t *testing.T) {
	home := t.TempDir()
	localState := &model.LocalState{TrackedItems: map[string]model.TrackedItemState{
		"shell_zshrc": {
			ID: "shell_zshrc", Name: "~/.zshrc", RelHomePath: ".zshrc",
			VaultFile: "shell_zshrc.age", Category: model.CategoryShell,
			Selected: true, LastLocalHash: "old",
		},
	}}
	items := IncludeTrackedMissingItems(nil, localState, home)
	if len(items) != 1 || !items[0].Deleted || items[0].Exists {
		t.Fatalf("本地删除项应转换为 tombstone: %+v", items)
	}
}

func TestIncludeTrackedMissingCredentialCreatesTypedTombstone(t *testing.T) {
	home := t.TempDir()
	localState := &model.LocalState{TrackedItems: map[string]model.TrackedItemState{
		"cloud_aws_credentials": {
			ID: "cloud_aws_credentials", Name: "AWS Credentials", RelHomePath: ".aws/credentials",
			VaultFile: "aws_credentials.age", Category: model.CategoryCloud,
			Selected: true, SecretKind: model.SecretKindAWSCredentials,
		},
	}}
	items := IncludeTrackedMissingItems(nil, localState, home)
	if len(items) != 1 || !items[0].Deleted || items[0].SecretKind != model.SecretKindAWSCredentials {
		t.Fatalf("凭据删除应保留受控类型并生成 tombstone: %+v", items)
	}
}

func TestRemoteTombstoneIsRemoteUpdate(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, ".zshrc")
	if err := os.WriteFile(filePath, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	local := model.ConfigItem{ID: "shell_zshrc", LocalPath: filePath, RelHomePath: ".zshrc", Exists: true}
	remote := &model.Manifest{Items: []model.ConfigItem{{ID: "shell_zshrc", Deleted: true}}}
	diffs, err := AnalyzeDiff([]model.ConfigItem{local}, remote, &model.LocalState{TrackedItems: map[string]model.TrackedItemState{}})
	if err != nil {
		t.Fatal(err)
	}
	if diffs[0].DiffStatus != model.DiffStatusRemoteModified {
		t.Fatalf("远端 tombstone 应识别为远端更新: %s", diffs[0].DiffStatus)
	}
}

func TestVaultStatesAreIsolated(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	alice, err := NewStateManagerForVault("alice/config-mesh-vault")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := NewStateManagerForVault("bob/config-mesh-vault")
	if err != nil {
		t.Fatal(err)
	}
	if alice.StateFilePath == bob.StateFilePath {
		t.Fatal("不同 vault 不得共用状态文件")
	}

	if err := alice.RecordSyncSuccess([]model.ConfigItem{{ID: "alice-item", Selected: true}}, "alice-snapshot"); err != nil {
		t.Fatal(err)
	}
	if err := bob.RecordSyncSuccess([]model.ConfigItem{{ID: "bob-item", Selected: true}}, "bob-snapshot"); err != nil {
		t.Fatal(err)
	}

	aliceState, err := alice.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if aliceState.VaultID != "alice/config-mesh-vault" || aliceState.RemoteSnapshotID != "alice-snapshot" {
		t.Fatalf("Alice 状态被污染: %+v", aliceState)
	}
	if _, exists := aliceState.TrackedItems["bob-item"]; exists {
		t.Fatal("Alice 状态包含 Bob 的配置项")
	}

	active, err := NewStateManager()
	if err != nil {
		t.Fatal(err)
	}
	if active.VaultID != "bob/config-mesh-vault" {
		t.Fatalf("活跃 vault 应指向最近保存的 Bob 状态: %q", active.VaultID)
	}
}

func TestDirectoryHashExcludesSensitiveContent(t *testing.T) {
	dir := t.TempDir()
	ordinary := filepath.Join(dir, "settings.json")
	secret := filepath.Join(dir, "token.txt")
	if err := os.WriteFile(ordinary, []byte(`{"theme":"dark"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte(`api_key="literal-production-secret"`), 0600); err != nil {
		t.Fatal(err)
	}
	item := model.ConfigItem{ID: "dir", LocalPath: dir, IsDir: true}
	first, err := CalculateItemHash(item)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte(`api_key="another-production-secret"`), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := CalculateItemHash(item)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("被内容扫描拒绝的凭据不应影响可同步目录哈希")
	}
}
