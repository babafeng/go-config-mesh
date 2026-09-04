package sync

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"config-mesh/internal/model"
)

// BackupManager 负责本地配置的精准快照备份与还原
type BackupManager struct {
	BackupBaseDir string
	// RestoreRoot 限制 rollback 只能写入该目录。空值时使用当前用户家目录。
	RestoreRoot   string
	StateFilePath string
	VaultID       string
}

// NewBackupManager 创建备份管理器实例
func NewBackupManager() (*BackupManager, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("无法获取家目录: %w", err)
	}

	baseDir := filepath.Join(homeDir, ".config-mesh", "backups")
	if err := os.MkdirAll(baseDir, 0700); err != nil {
		return nil, fmt.Errorf("创建备份根目录失败: %w", err)
	}
	if err := os.Chmod(baseDir, 0700); err != nil {
		return nil, fmt.Errorf("收紧备份根目录权限失败: %w", err)
	}

	return &BackupManager{BackupBaseDir: baseDir}, nil
}

// BackupSelectedFiles 备份选中项，并显式记录同步前不存在的目标。
func (bm *BackupManager) BackupSelectedFiles(items []model.ConfigItem) (*model.BackupManifest, error) {
	hostname, _ := os.Hostname()
	now := time.Now()
	cleanHost := regexp.MustCompile(`[^A-Za-z0-9._-]+`).ReplaceAllString(hostname, "-")
	cleanHost = strings.Trim(cleanHost, "-")
	if cleanHost == "" {
		cleanHost = "host"
	}
	backupID := fmt.Sprintf("backup-%s-%s", cleanHost, now.UTC().Format("20060102T150405.000000000Z"))
	backupDir := filepath.Join(bm.BackupBaseDir, backupID)

	if err := os.MkdirAll(bm.BackupBaseDir, 0700); err != nil {
		return nil, fmt.Errorf("创建备份根目录失败: %w", err)
	}
	if err := os.Mkdir(backupDir, 0700); err != nil {
		return nil, fmt.Errorf("创建快照备份目录失败: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(backupDir)
		}
	}()

	var backedUpItems []model.ConfigItem

	for _, item := range items {
		// 不存在的目标也进入 Manifest，回滚时据此删除同步新建项。
		info, err := os.Lstat(item.LocalPath)
		if os.IsNotExist(err) {
			item.Exists = false
			backedUpItems = append(backedUpItems, item)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("检查备份源失败 (%s): %w", item.LocalPath, err)
		}
		item.Exists = true
		item.FileMode = uint32(info.Mode().Perm())
		if !info.Mode().IsRegular() && !info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			return nil, fmt.Errorf("拒绝备份特殊文件类型: %s", item.LocalPath)
		}

		destPath, err := safeJoin(backupDir, item.RelHomePath)
		if err != nil {
			return nil, fmt.Errorf("备份目标路径无效: %w", err)
		}
		if item.IsDir || info.IsDir() {
			if err := copyDir(item.LocalPath, destPath); err != nil {
				return nil, fmt.Errorf("备份目录失败 (%s): %w", item.LocalPath, err)
			}
		} else {
			if err := copyFile(item.LocalPath, destPath); err != nil {
				return nil, fmt.Errorf("备份文件失败 (%s): %w", item.LocalPath, err)
			}
		}

		backedUpItems = append(backedUpItems, item)
	}

	manifest := &model.BackupManifest{
		BackupID:  backupID,
		CreatedAt: now,
		Hostname:  hostname,
		BackupDir: backupDir,
		VaultID:   bm.VaultID,
		Items:     backedUpItems,
	}

	// 如果指定了当前状态文件（或者默认发现状态文件），一并为 LocalState 生成安全快照
	stateFile := bm.StateFilePath
	if stateFile == "" {
		homeDir, _ := os.UserHomeDir()
		if homeDir != "" {
			activeVaultPath := filepath.Join(homeDir, ".config-mesh", "active-vault")
			if activeVaultBytes, err := os.ReadFile(activeVaultPath); err == nil {
				vID := strings.TrimSpace(string(activeVaultBytes))
				if vID != "" {
					if manifest.VaultID == "" {
						manifest.VaultID = vID
					}
					stateFile = filepath.Join(homeDir, ".config-mesh", "states", fmt.Sprintf("%x.json", sha256.Sum256([]byte(vID))))
				}
			}
		}
	}
	if stateFile != "" {
		if stateData, err := os.ReadFile(stateFile); err == nil {
			stateSnapshotName := "state-snapshot.json"
			stateSnapshotPath := filepath.Join(backupDir, stateSnapshotName)
			if err := os.WriteFile(stateSnapshotPath, stateData, 0600); err == nil {
				manifest.StateFile = stateFile
				manifest.StateSnapshot = stateSnapshotName
			}
		}
	}

	// 写入 backup-manifest.json
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("序列化备份清单失败: %w", err)
	}

	manifestPath := filepath.Join(backupDir, "backup-manifest.json")
	if err := atomicWriteFile(manifestPath, manifestData, 0600); err != nil {
		return nil, fmt.Errorf("保存备份清单失败: %w", err)
	}
	complete = true

	return manifest, nil
}

// copyFile 复制单个文件 (妥善处理软链接与跳过特殊文件)
func copyFile(src, dst string) error {
	lInfo, err := os.Lstat(src)
	if err != nil {
		return err
	}

	mode := lInfo.Mode()
	// 跳过 Socket 等不支持类型
	if mode&(os.ModeSocket|os.ModeNamedPipe|os.ModeDevice) != 0 {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}

	// 软链接复制
	if mode&os.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		if err := os.RemoveAll(dst); err != nil {
			return err
		}
		return os.Symlink(target, dst)
	}

	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_EXCL, lInfo.Mode().Perm())
	if err != nil {
		return err
	}
	defer dstFile.Close()

	if _, err = io.Copy(dstFile, srcFile); err != nil {
		return err
	}
	return dstFile.Sync()
}

// copyDir 递归复制目录
func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}

		destPath := filepath.Join(dst, relPath)
		if info.IsDir() {
			if err := os.MkdirAll(destPath, info.Mode().Perm()); err != nil {
				return err
			}
			return os.Chmod(destPath, info.Mode().Perm())
		}

		return copyFile(path, destPath)
	})
}
