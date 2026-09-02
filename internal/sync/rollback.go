package sync

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

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
		if pathErr != nil || filepath.Clean(targetPath) != filepath.Clean(item.LocalPath) {
			return fmt.Errorf("回滚目标越界或与备份记录不一致: %s", item.RelHomePath)
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
		if err != nil || filepath.Clean(targetPath) != filepath.Clean(item.LocalPath) {
			return fmt.Errorf("回滚目标越界或与备份记录不一致: %s", item.RelHomePath)
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

	return nil
}
