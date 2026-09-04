package sync

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"config-mesh/internal/model"
)

// ListBackups 列出本地所有历史快照备份
func (bm *BackupManager) ListBackups() ([]model.BackupManifest, error) {
	entries, err := os.ReadDir(bm.BackupBaseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var manifests []model.BackupManifest
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		manifestFile := filepath.Join(bm.BackupBaseDir, entry.Name(), "backup-manifest.json")
		info, err := os.Lstat(manifestFile)
		if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > 4<<20 {
			continue
		}
		data, err := os.ReadFile(manifestFile)
		if err != nil {
			continue
		}

		var m model.BackupManifest
		if err := json.Unmarshal(data, &m); err == nil && len(m.Items) <= 10_000 {
			if m.BackupID != entry.Name() {
				continue
			}
			// 不信任 Manifest 中可被篡改的 BackupDir。
			m.BackupDir = filepath.Join(bm.BackupBaseDir, entry.Name())
			manifests = append(manifests, m)
		}
	}

	// 按时间倒序排序（最新的在最前）
	sort.Slice(manifests, func(i, j int) bool {
		return manifests[i].CreatedAt.After(manifests[j].CreatedAt)
	})

	return manifests, nil
}

// Rollback 根据指定的备份标识或最新备份恢复配置
func (bm *BackupManager) Rollback(backupID string) error {
	backups, err := bm.ListBackups()
	if err != nil {
		return fmt.Errorf("读取备份列表失败: %w", err)
	}

	if len(backups) == 0 {
		return fmt.Errorf("未找到任何历史备份")
	}

	var target *model.BackupManifest
	if backupID == "" || backupID == "latest" {
		target = &backups[0]
	} else {
		for _, b := range backups {
			if b.BackupID == backupID {
				target = &b
				break
			}
		}
	}

	if target == nil {
		return fmt.Errorf("未找到指定的备份快照: %s", backupID)
	}

	// 逐个还原文件
	restoreRoot := bm.RestoreRoot
	if restoreRoot == "" {
		restoreRoot, err = os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("获取回滚根目录失败: %w", err)
		}
	}
	// 先验证全部路径和备份内容，再开始任何破坏性还原，避免因坏清单造成半回滚。
	for _, item := range target.Items {
		targetPath, pathErr := safeJoin(restoreRoot, item.RelHomePath)
		if pathErr != nil {
			return fmt.Errorf("回滚目标越界: %s", item.RelHomePath)
		}
		if bm.RestoreRoot == "" && filepath.Clean(targetPath) != filepath.Clean(item.LocalPath) {
			return fmt.Errorf("回滚目标与备份记录不一致: %s", item.RelHomePath)
		}
		if err := ensureNoSymlinkParents(restoreRoot, targetPath); err != nil {
			return fmt.Errorf("回滚目标父路径不安全: %w", err)
		}
		if !item.Exists {
			continue
		}
		backupFilePath, pathErr := safeJoin(target.BackupDir, item.RelHomePath)
		if pathErr != nil {
			return fmt.Errorf("备份文件路径越界: %w", pathErr)
		}
		if err := ensureNoSymlinkParents(target.BackupDir, backupFilePath); err != nil {
			return fmt.Errorf("备份文件父路径不安全: %w", err)
		}
		if _, err := os.Lstat(backupFilePath); err != nil {
			return fmt.Errorf("备份内容缺失 (%s): %w", backupFilePath, err)
		}
	}
	for _, item := range target.Items {
		targetPath, err := safeJoin(restoreRoot, item.RelHomePath)
		if err != nil {
			return fmt.Errorf("回滚目标越界: %s", item.RelHomePath)
		}
		if bm.RestoreRoot == "" && filepath.Clean(targetPath) != filepath.Clean(item.LocalPath) {
			return fmt.Errorf("回滚目标与备份记录不一致: %s", item.RelHomePath)
		}
		if !item.Exists {
			if err := os.RemoveAll(targetPath); err != nil {
				return fmt.Errorf("删除同步前不存在的配置失败 (%s): %w", targetPath, err)
			}
			continue
		}

		backupFilePath, _ := safeJoin(target.BackupDir, item.RelHomePath)

		if item.IsDir {
			if err := os.RemoveAll(targetPath); err != nil {
				return err
			}
			if err := copyDir(backupFilePath, targetPath); err != nil {
				return fmt.Errorf("回滚目录失败 (%s): %w", targetPath, err)
			}
		} else {
			if err := copyFile(backupFilePath, targetPath); err != nil {
				return fmt.Errorf("回滚文件失败 (%s): %w", targetPath, err)
			}
		}
	}

	// 同步还原本地状态基线 (LocalState)，确保 rollback 后基线与实际文件一致
	bm.restoreLocalState(target)

	return nil
}

// restoreLocalState 还原本地状态库，使文件回滚后 LocalState 与本地实际文件保持一致，消除虚假修改
func (bm *BackupManager) restoreLocalState(target *model.BackupManifest) {
	// 1. 若备份清单中包含状态快照文件 (state-snapshot.json)，直接覆盖还原状态文件
	if target.StateSnapshot != "" {
		snapshotPath := filepath.Join(target.BackupDir, target.StateSnapshot)
		if data, err := os.ReadFile(snapshotPath); err == nil {
			destFile := target.StateFile
			if destFile == "" && bm.StateFilePath != "" {
				destFile = bm.StateFilePath
			}
			if destFile != "" {
				_ = os.MkdirAll(filepath.Dir(destFile), 0700)
				_ = atomicWriteFile(destFile, data, 0600)
				return
			}
		}
	}

	// 2. 向后兼容：若备份中未打包状态快照文件，则定位本地状态库，
	// 根据 target.Items 记录的原始 ContentHash 刷新本地 TrackedItems 基线哈希
	homeDir, _ := os.UserHomeDir()
	if homeDir == "" {
		return
	}
	stateFile := bm.StateFilePath
	if stateFile == "" {
		if target.StateFile != "" {
			stateFile = target.StateFile
		} else {
			activeVaultFile := filepath.Join(homeDir, ".config-mesh", "active-vault")
			if vBytes, err := os.ReadFile(activeVaultFile); err == nil {
				vID := strings.TrimSpace(string(vBytes))
				if vID != "" {
					stateFile = filepath.Join(homeDir, ".config-mesh", "states", fmt.Sprintf("%x.json", sha256.Sum256([]byte(vID))))
				}
			}
		}
	}
	if stateFile == "" {
		return
	}

	stateBytes, err := os.ReadFile(stateFile)
	if err != nil {
		return
	}
	var localState model.LocalState
	if err := json.Unmarshal(stateBytes, &localState); err != nil {
		return
	}

	for _, item := range target.Items {
		if tracked, ok := localState.TrackedItems[item.ID]; ok {
			if item.ContentHash != "" {
				tracked.LastLocalHash = item.ContentHash
				localState.TrackedItems[item.ID] = tracked
			}
		}
	}

	if updatedBytes, err := json.MarshalIndent(localState, "", "  "); err == nil {
		_ = atomicWriteFile(stateFile, updatedBytes, 0600)
	}
}
