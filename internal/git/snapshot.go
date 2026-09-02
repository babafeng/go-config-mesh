package git

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"config-mesh/internal/model"
)

var deviceIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
var snapshotIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,200}$`)

// sanitizeHostname 清理主机名中可能存在的非法字符并转换为小写
func sanitizeHostname(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	if h == "" {
		h = "macbook"
	}
	// 移除 .local 或其他后缀
	h = strings.TrimSuffix(h, ".local")
	// 替换非字母数字和减号的字符
	reg := regexp.MustCompile(`[^a-z0-9_\-]+`)
	cleaned := strings.Trim(reg.ReplaceAllString(h, "-"), "-")
	if cleaned == "" {
		return "macbook"
	}
	return cleaned
}

// GenerateStableSnapshotDirName 为一台设备生成长期复用的仓库目录名。
// hostname 仅用于可读前缀，真正避免碰撞的是持久化随机 deviceID。
func GenerateStableSnapshotDirName(hostname, deviceID string) (string, error) {
	if !deviceIDPattern.MatchString(deviceID) {
		return "", fmt.Errorf("设备 ID 格式无效")
	}
	prefix := sanitizeHostname(hostname)
	if len(prefix) > 180 {
		prefix = prefix[:180]
	}
	name := fmt.Sprintf("%s-%s", prefix, deviceID[:12])
	if !snapshotIDPattern.MatchString(name) {
		return "", fmt.Errorf("生成的稳定目录名无效: %q", name)
	}
	return name, nil
}

// CreateSnapshotStagingDir 在 hosts 下创建同文件系统的临时目录，供完整构建后原子切换。
func CreateSnapshotStagingDir(repoRoot, snapshotID string) (string, error) {
	if !snapshotIDPattern.MatchString(snapshotID) {
		return "", fmt.Errorf("快照目录标识无效: %q", snapshotID)
	}
	hostsDir := filepath.Join(repoRoot, "hosts")
	if err := os.MkdirAll(hostsDir, 0700); err != nil {
		return "", fmt.Errorf("创建 hosts 目录失败: %w", err)
	}
	dir, err := os.MkdirTemp(hostsDir, "."+snapshotID+".staging-")
	if err != nil {
		return "", fmt.Errorf("创建快照暂存目录失败: %w", err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

// InstallStagedSnapshot 用构建完整的暂存目录替换稳定目录；替换失败时恢复旧目录。
func InstallStagedSnapshot(repoRoot, snapshotID, stagingDir string) error {
	if !snapshotIDPattern.MatchString(snapshotID) {
		return fmt.Errorf("快照目录标识无效: %q", snapshotID)
	}
	hostsDir := filepath.Join(repoRoot, "hosts")
	stagingParent, err := filepath.Abs(filepath.Dir(stagingDir))
	if err != nil {
		return err
	}
	hostsAbs, err := filepath.Abs(hostsDir)
	if err != nil {
		return err
	}
	if stagingParent != hostsAbs || !strings.HasPrefix(filepath.Base(stagingDir), "."+snapshotID+".staging-") {
		return fmt.Errorf("快照暂存目录不属于目标 hosts: %s", stagingDir)
	}
	stagingInfo, err := os.Lstat(stagingDir)
	if err != nil || !stagingInfo.IsDir() || stagingInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("快照暂存目录无效: %s", stagingDir)
	}

	target := filepath.Join(hostsDir, snapshotID)
	targetInfo, targetErr := os.Lstat(target)
	if os.IsNotExist(targetErr) {
		if err := os.Rename(stagingDir, target); err != nil {
			return fmt.Errorf("安装新快照目录失败: %w", err)
		}
		return nil
	}
	if targetErr != nil || !targetInfo.IsDir() || targetInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("现有快照目标不是安全目录: %s", target)
	}

	previous, err := os.MkdirTemp(hostsDir, "."+snapshotID+".previous-")
	if err != nil {
		return fmt.Errorf("创建旧快照暂存名失败: %w", err)
	}
	if err := os.Remove(previous); err != nil {
		return err
	}
	if err := os.Rename(target, previous); err != nil {
		return fmt.Errorf("暂存旧快照失败: %w", err)
	}
	if err := os.Rename(stagingDir, target); err != nil {
		rollbackErr := os.Rename(previous, target)
		if rollbackErr != nil {
			return fmt.Errorf("安装新快照失败: %v；恢复旧快照也失败: %w", err, rollbackErr)
		}
		return fmt.Errorf("安装新快照失败，已恢复旧快照: %w", err)
	}
	if err := os.RemoveAll(previous); err != nil {
		return fmt.Errorf("清理旧快照暂存目录失败: %w", err)
	}
	return nil
}

// HostSnapshotInfo 快照信息
type HostSnapshotInfo struct {
	SnapshotID string
	Hostname   string
	DeviceID   string
	Date       string
	Seq        int
	FullPath   string
	CreatedAt  time.Time
}

// WriteSnapshotMetadata 写入不含配置路径或内容的公开排序元数据。
func WriteSnapshotMetadata(snapshotDir string, metadata model.SnapshotMetadata) error {
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(snapshotDir, "snapshot-meta.json"), data, 0644)
}

// ListHostSnapshots 列出仓库中已存在的所有主机快照
func ListHostSnapshots(repoRoot string) ([]HostSnapshotInfo, error) {
	hostsDir := filepath.Join(repoRoot, "hosts")
	if _, err := os.Stat(hostsDir); os.IsNotExist(err) {
		return nil, nil
	}

	entries, err := os.ReadDir(hostsDir)
	if err != nil {
		return nil, err
	}

	var snapshots []HostSnapshotInfo
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		name := entry.Name()
		fullPath := filepath.Join(hostsDir, name)

		// 只接受普通的加密 Manifest；明文或软链接清单不属于有效快照。
		manifestPathAge := filepath.Join(fullPath, "manifest.json.age")
		manifestInfo, manifestErr := os.Lstat(manifestPathAge)
		if manifestErr != nil || !manifestInfo.Mode().IsRegular() || manifestInfo.Size() > 4<<20 {
			continue
		}

		info := HostSnapshotInfo{
			SnapshotID: name,
			FullPath:   fullPath,
		}
		metadataPath := filepath.Join(fullPath, "snapshot-meta.json")
		metadataInfo, metadataErr := os.Lstat(metadataPath)
		if metadataErr == nil && metadataInfo.Mode().IsRegular() && metadataInfo.Size() <= 1<<20 {
			data, readErr := os.ReadFile(metadataPath)
			var metadata model.SnapshotMetadata
			if readErr == nil && json.Unmarshal(data, &metadata) == nil && metadata.SnapshotID == name {
				info.Hostname = metadata.Hostname
				info.DeviceID = metadata.DeviceID
				info.CreatedAt = metadata.CreatedAt
				info.Date = metadata.CreatedAt.Format("20060102")
			}
		}
		if info.CreatedAt.IsZero() {
			populateLegacySnapshotInfo(&info)
		}
		snapshots = append(snapshots, info)
	}

	// 按真实时间排序；旧版快照则按日期与数字序号排序，不再使用纯字符串顺序。
	sort.Slice(snapshots, func(i, j int) bool {
		if !snapshots[i].CreatedAt.Equal(snapshots[j].CreatedAt) {
			return snapshots[i].CreatedAt.After(snapshots[j].CreatedAt)
		}
		if snapshots[i].Seq != snapshots[j].Seq {
			return snapshots[i].Seq > snapshots[j].Seq
		}
		return snapshots[i].SnapshotID > snapshots[j].SnapshotID
	})

	return snapshots, nil
}

func populateLegacySnapshotInfo(info *HostSnapshotInfo) {
	name := info.SnapshotID
	lastDash := strings.LastIndex(name, "-")
	if lastDash < 0 || len(name)-lastDash-1 != 8 {
		return
	}
	dateText := name[lastDash+1:]
	date, err := time.Parse("20060102", dateText)
	if err != nil {
		return
	}
	info.Date = dateText
	info.CreatedAt = date
	info.Seq = 1
	prefix := name[:lastDash]
	if seqDash := strings.LastIndex(prefix, "-"); seqDash >= 0 {
		if seq, err := strconv.Atoi(prefix[seqDash+1:]); err == nil && seq >= 2 {
			info.Seq = seq
			info.Hostname = prefix[:seqDash]
			return
		}
	}
	info.Hostname = prefix
}
