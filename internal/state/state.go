package state

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"config-mesh/internal/model"
	"config-mesh/internal/scanner"
)

const StateVersion = "1.0.0"

var vaultIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}/[A-Za-z0-9_.-]{1,100}$`)
var deviceIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
var snapshotIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,200}$`)

// StateManager 本地状态与差异对比管理器
type StateManager struct {
	StateFilePath string
	VaultID       string
	activeFile    string
}

// NewStateManager 返回最近一次成功同步的 vault 状态；尚无活跃 vault 时兼容旧版 state.json。
func NewStateManager() (*StateManager, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("获取家目录失败: %w", err)
	}

	configDir := filepath.Join(homeDir, ".config-mesh")
	if err := ensurePrivateDir(configDir); err != nil {
		return nil, fmt.Errorf("创建状态配置目录失败: %w", err)
	}
	activeFile := filepath.Join(configDir, "active-vault")
	if data, readErr := os.ReadFile(activeFile); readErr == nil {
		vaultID := strings.TrimSpace(string(data))
		if !vaultIDPattern.MatchString(vaultID) {
			return nil, fmt.Errorf("活跃 vault 标识无效: %q", vaultID)
		}
		return newStateManager(configDir, vaultID)
	} else if !os.IsNotExist(readErr) {
		return nil, fmt.Errorf("读取活跃 vault 失败: %w", readErr)
	}
	return &StateManager{StateFilePath: filepath.Join(configDir, "state.json")}, nil
}

// NewStateManagerForVault 为指定 owner/repository 创建隔离的状态基线。
func NewStateManagerForVault(vaultID string) (*StateManager, error) {
	if !vaultIDPattern.MatchString(vaultID) {
		return nil, fmt.Errorf("vault 标识必须为 owner/repository: %q", vaultID)
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("获取家目录失败: %w", err)
	}
	configDir := filepath.Join(homeDir, ".config-mesh")
	if err := ensurePrivateDir(configDir); err != nil {
		return nil, fmt.Errorf("创建状态配置目录失败: %w", err)
	}
	return newStateManager(configDir, vaultID)
}

func newStateManager(configDir, vaultID string) (*StateManager, error) {
	statesDir := filepath.Join(configDir, "states")
	if err := ensurePrivateDir(statesDir); err != nil {
		return nil, fmt.Errorf("创建 vault 状态目录失败: %w", err)
	}
	digest := sha256.Sum256([]byte(vaultID))
	return &StateManager{
		StateFilePath: filepath.Join(statesDir, hex.EncodeToString(digest[:])+".json"),
		VaultID:       vaultID,
		activeFile:    filepath.Join(configDir, "active-vault"),
	}, nil
}

func ensurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return os.Chmod(dir, 0700)
}

// LoadState 加载本地状态配置
func (sm *StateManager) LoadState() (*model.LocalState, error) {
	data, err := os.ReadFile(sm.StateFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return &model.LocalState{
				Version:      StateVersion,
				VaultID:      sm.VaultID,
				TrackedItems: make(map[string]model.TrackedItemState),
			}, nil
		}
		return nil, fmt.Errorf("读取状态文件失败: %w", err)
	}

	var state model.LocalState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("本地状态文件已损坏，已拒绝静默重置: %w", err)
	}

	if state.TrackedItems == nil {
		state.TrackedItems = make(map[string]model.TrackedItemState)
	}
	if state.Version != "" && state.Version != StateVersion {
		return nil, fmt.Errorf("本地状态版本不兼容: %s", state.Version)
	}
	if sm.VaultID != "" && state.VaultID != "" && state.VaultID != sm.VaultID {
		return nil, fmt.Errorf("状态文件属于其他 vault: %s", state.VaultID)
	}
	if sm.VaultID != "" {
		state.VaultID = sm.VaultID
	}
	if state.DeviceID != "" && !deviceIDPattern.MatchString(state.DeviceID) {
		return nil, fmt.Errorf("本机设备标识无效")
	}
	if state.UploadSnapshotID != "" && !snapshotIDPattern.MatchString(state.UploadSnapshotID) {
		return nil, fmt.Errorf("本机上传目录标识无效: %q", state.UploadSnapshotID)
	}

	return &state, nil
}

// SaveState 持久化保存状态配置
func (sm *StateManager) SaveState(state *model.LocalState) error {
	if state.DeviceID != "" && !deviceIDPattern.MatchString(state.DeviceID) {
		return fmt.Errorf("拒绝保存无效的本机设备标识")
	}
	if state.UploadSnapshotID != "" && !snapshotIDPattern.MatchString(state.UploadSnapshotID) {
		return fmt.Errorf("拒绝保存无效的上传目录标识: %q", state.UploadSnapshotID)
	}
	state.Version = StateVersion
	if sm.VaultID != "" {
		if state.VaultID != "" && state.VaultID != sm.VaultID {
			return fmt.Errorf("拒绝把 %s 的状态写入 %s", state.VaultID, sm.VaultID)
		}
		state.VaultID = sm.VaultID
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化状态文件失败: %w", err)
	}

	stateDir := filepath.Dir(sm.StateFilePath)
	if err := ensurePrivateDir(stateDir); err != nil {
		return fmt.Errorf("创建状态目录失败: %w", err)
	}
	if sm.VaultID != "" && sm.activeFile != "" {
		if err := atomicWritePrivateFile(sm.activeFile, []byte(sm.VaultID+"\n")); err != nil {
			return fmt.Errorf("保存活跃 vault 失败: %w", err)
		}
	}
	if err := atomicWritePrivateFile(sm.StateFilePath, data); err != nil {
		return fmt.Errorf("保存状态文件失败: %w", err)
	}
	return nil
}

// EnsureDeviceID 返回持久化的随机设备 ID。它不使用 hostname，因此设备改名后仍复用同一上传目录。
func (sm *StateManager) EnsureDeviceID(state *model.LocalState) (string, error) {
	if state.DeviceID != "" {
		if !deviceIDPattern.MatchString(state.DeviceID) {
			return "", fmt.Errorf("本机设备标识无效")
		}
		return state.DeviceID, nil
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("生成本机设备标识失败: %w", err)
	}
	state.DeviceID = hex.EncodeToString(random)
	if err := sm.SaveState(state); err != nil {
		return "", err
	}
	return state.DeviceID, nil
}

func atomicWritePrivateFile(filePath string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(filePath), ".state-*")
	if err != nil {
		return err
	}
	tmpFile := tmp.Name()
	defer os.Remove(tmpFile)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpFile, filePath)
}

// CalculateItemHash 计算配置项的当前内容哈希 (针对单文件或排除 ignore 后的目录)
func CalculateItemHash(item model.ConfigItem) (string, error) {
	rootInfo, err := os.Lstat(item.LocalPath)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}

	if !item.IsDir {
		return hashPath(item.LocalPath, rootInfo, "")
	}

	// 目录：递归计算所有有效文件的综合哈希
	h := sha256.New()
	var paths []string

	if err := filepath.Walk(item.LocalPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info == nil {
			return fmt.Errorf("无法读取路径元数据: %s", path)
		}

		relPath, err := filepath.Rel(item.LocalPath, path)
		if err != nil || relPath == "." {
			return nil
		}

		if scanner.IsSensitiveFile(path) || scanner.ShouldIgnorePath(relPath) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.Mode().IsRegular() {
			sensitive, inspectErr := scanner.FileContainsSensitiveContent(path)
			if inspectErr != nil {
				return inspectErr
			}
			if sensitive {
				return nil
			}
		}

		paths = append(paths, relPath)
		return nil
	}); err != nil {
		return "", err
	}

	// 排序保证确定性
	sort.Strings(paths)
	for _, rel := range paths {
		abs := filepath.Join(item.LocalPath, rel)
		info, err := os.Lstat(abs)
		if err != nil {
			return "", err
		}
		part, err := hashPath(abs, info, rel)
		if err != nil {
			return "", err
		}
		h.Write([]byte(part))
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

func hashPath(filePath string, info os.FileInfo, rel string) (string, error) {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%o\x00%d\x00", filepath.ToSlash(rel), info.Mode().Perm(), info.Mode()&os.ModeType)
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(filePath)
		if err != nil {
			return "", err
		}
		h.Write([]byte(target))
	} else if info.Mode().IsRegular() {
		file, err := os.Open(filePath)
		if err != nil {
			return "", err
		}
		_, copyErr := io.Copy(h, file)
		closeErr := file.Close()
		if copyErr != nil {
			return "", copyErr
		}
		if closeErr != nil {
			return "", closeErr
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// AnalyzeDiff 对比本地扫描项、云端 Manifest 与本地历史状态，计算每个配置项的差异状态并继承选中偏好
func AnalyzeDiff(localItems []model.ConfigItem, remoteManifest *model.Manifest, state *model.LocalState) ([]model.ConfigItem, error) {
	remoteMap := make(map[string]model.ConfigItem)
	if remoteManifest != nil {
		for _, rItem := range remoteManifest.Items {
			remoteMap[rItem.ID] = rItem
		}
	}

	var results []model.ConfigItem

	for _, item := range localItems {
		// 计算当前本地哈希
		localHash, err := CalculateItemHash(item)
		if err != nil {
			return nil, fmt.Errorf("计算配置项哈希失败 (%s): %w", item.ID, err)
		}
		item.ContentHash = localHash

		// 匹配云端哈希
		var remoteHash string
		remoteItem, remoteExists := remoteMap[item.ID]
		if remoteExists {
			remoteHash = remoteItem.ContentHash
			item.RemoteHash = remoteHash
		}

		// 匹配历史状态基线
		tracked, hasTracked := state.TrackedItems[item.ID]
		if hasTracked {
			item.Selected = tracked.Selected // 继承用户历史勾选记忆
			if tracked.Strategy != "" {
				item.Strategy = tracked.Strategy
			}
		} else {
			item.Selected = item.Recommended
		}

		// 差异状态判定算法
		switch {
		case remoteExists && remoteItem.Deleted && localHash == "":
			item.DiffStatus = model.DiffStatusSynced
		case remoteExists && remoteItem.Deleted && !hasTracked:
			item.DiffStatus = model.DiffStatusRemoteModified

		case remoteHash == "" && !hasTracked:
			// 云端无、本地无历史记录
			item.DiffStatus = model.DiffStatusNew

		case remoteHash == localHash && localHash != "":
			// 两端内容完全相同
			item.DiffStatus = model.DiffStatusSynced

		case hasTracked:
			localChanged := localHash != tracked.LastLocalHash
			remoteChanged := remoteExists && remoteHash != tracked.LastRemoteHash

			if localChanged && remoteChanged {
				item.DiffStatus = model.DiffStatusConflict
			} else if localChanged {
				item.DiffStatus = model.DiffStatusLocalModified
			} else if remoteChanged {
				item.DiffStatus = model.DiffStatusRemoteModified
			} else {
				item.DiffStatus = model.DiffStatusSynced
			}

		case remoteHash != "" && remoteHash != localHash:
			item.DiffStatus = model.DiffStatusRemoteModified

		default:
			item.DiffStatus = model.DiffStatusNew
		}

		results = append(results, item)
	}

	return results, nil
}

// IncludeTrackedMissingItems 将“上次跟踪、现在不存在”的项显式加入列表。
// 这些项在上传时作为 tombstone，不会再被“扫描器跳过不存在文件”吞掉。
func IncludeTrackedMissingItems(items []model.ConfigItem, localState *model.LocalState, homeDir string) []model.ConfigItem {
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		seen[item.ID] = true
	}
	for id, tracked := range localState.TrackedItems {
		pathIsSensitive := scanner.IsSensitiveConfigPath(tracked.RelHomePath, tracked.IsDir)
		allowedSecret := scanner.IsAllowedSecretConfigPath(tracked.RelHomePath, tracked.IsDir, tracked.SecretKind)
		if seen[id] || !tracked.Selected || tracked.RelHomePath == "" ||
			(pathIsSensitive && !allowedSecret) || (tracked.SecretKind != "" && !allowedSecret) ||
			filepath.IsAbs(tracked.RelHomePath) {
			continue
		}
		target := filepath.Join(homeDir, filepath.Clean(tracked.RelHomePath))
		rel, err := filepath.Rel(homeDir, target)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if _, err := os.Lstat(target); err == nil || !os.IsNotExist(err) {
			continue
		}
		name := tracked.Name
		if name == "" {
			name = "~/" + filepath.ToSlash(tracked.RelHomePath)
		}
		items = append(items, model.ConfigItem{
			ID: id, Name: name, Category: tracked.Category,
			LocalPath: target, RelHomePath: tracked.RelHomePath, VaultFile: tracked.VaultFile,
			IsDir: tracked.IsDir, Exists: false, Selected: true, Strategy: tracked.Strategy,
			Deleted: true, SecretKind: tracked.SecretKind,
		})
	}
	return items
}

// RecordSyncSuccess 在同步成功后更新当前 vault 的通用本地状态。
// 新代码应优先使用有方向语义的 RecordUploadSuccess/RecordDownloadSuccess。
func (sm *StateManager) RecordSyncSuccess(syncedItems []model.ConfigItem, snapshotID string) error {
	state, err := sm.LoadState()
	if err != nil {
		return err
	}
	now := time.Now()
	state.LastSyncedAt = now
	state.RemoteSnapshotID = snapshotID
	if err := recordSyncedItems(state, syncedItems, now); err != nil {
		return err
	}
	return sm.SaveState(state)
}

// RecordDownloadSuccess 记录从远端应用到本机的文件、哈希和时间。
func (sm *StateManager) RecordDownloadSuccess(syncedItems []model.ConfigItem, snapshotID string) error {
	state, err := sm.LoadState()
	if err != nil {
		return err
	}
	now := time.Now()
	state.LastSyncedAt = now
	state.LastDownloadedAt = now
	state.RemoteSnapshotID = snapshotID
	if err := recordSyncedItems(state, syncedItems, now); err != nil {
		return err
	}
	return sm.SaveState(state)
}

// RecordUploadSuccess 记录完整选择表与已上传项。
// allItems 保存“哪些文件被选中”，syncedItems 保存这些文件对应的同步哈希与时间。
func (sm *StateManager) RecordUploadSuccess(
	allItems []model.ConfigItem,
	syncedItems []model.ConfigItem,
	snapshotID string,
	recipientsHash string,
) error {
	state, err := sm.LoadState()
	if err != nil {
		return err
	}
	now := time.Now()
	state.LastSyncedAt = now
	state.LastUploadedAt = now
	state.RemoteSnapshotID = snapshotID
	state.UploadSnapshotID = snapshotID
	state.RecipientsHash = recipientsHash

	syncedByID := make(map[string]model.ConfigItem, len(syncedItems))
	for _, item := range syncedItems {
		syncedByID[item.ID] = item
	}
	for _, item := range allItems {
		tracked := state.TrackedItems[item.ID]
		tracked.ID = item.ID
		tracked.Name = item.Name
		tracked.RelHomePath = item.RelHomePath
		tracked.VaultFile = item.VaultFile
		tracked.Category = item.Category
		tracked.IsDir = item.IsDir
		tracked.SecretKind = item.SecretKind
		tracked.Selected = item.Selected
		tracked.Strategy = item.Strategy
		tracked.UpdatedAt = now
		if synced, ok := syncedByID[item.ID]; ok {
			updated, err := trackedStateFromSyncedItem(tracked, synced, now)
			if err != nil {
				return err
			}
			expectedHash := synced.ContentHash
			if synced.Deleted {
				expectedHash = ""
			}
			if updated.LastLocalHash != expectedHash {
				return fmt.Errorf("配置在上传完成前再次发生变化，未更新同步基线: %s", synced.ID)
			}
			tracked = updated
		}
		state.TrackedItems[item.ID] = tracked
	}

	return sm.SaveState(state)
}

func recordSyncedItems(state *model.LocalState, syncedItems []model.ConfigItem, now time.Time) error {

	for _, item := range syncedItems {
		tracked := model.TrackedItemState{
			ID:          item.ID,
			Name:        item.Name,
			RelHomePath: item.RelHomePath,
			VaultFile:   item.VaultFile,
			Category:    item.Category,
			IsDir:       item.IsDir,
			Deleted:     item.Deleted,
			SecretKind:  item.SecretKind,
			Selected:    item.Selected,
			Strategy:    item.Strategy,
			UpdatedAt:   now,
		}
		updated, err := trackedStateFromSyncedItem(tracked, item, now)
		if err != nil {
			return err
		}
		state.TrackedItems[item.ID] = updated
	}
	return nil
}

func trackedStateFromSyncedItem(tracked model.TrackedItemState, item model.ConfigItem, now time.Time) (model.TrackedItemState, error) {
	currentHash, err := CalculateItemHash(item)
	if err != nil {
		return tracked, fmt.Errorf("计算同步后哈希失败 (%s): %w", item.ID, err)
	}
	remoteHash := item.RemoteHash
	if remoteHash == "" {
		remoteHash = item.ContentHash
	}
	tracked.Name = item.Name
	tracked.RelHomePath = item.RelHomePath
	tracked.VaultFile = item.VaultFile
	tracked.Category = item.Category
	tracked.IsDir = item.IsDir
	tracked.Deleted = item.Deleted
	tracked.SecretKind = item.SecretKind
	tracked.PayloadHash = item.PayloadHash
	tracked.FileMode = item.FileMode
	tracked.Size = item.Size
	tracked.LastLocalHash = currentHash
	tracked.LastRemoteHash = remoteHash
	tracked.Selected = item.Selected
	tracked.Strategy = item.Strategy
	tracked.UpdatedAt = now
	tracked.LastSyncedAt = now
	return tracked, nil
}
