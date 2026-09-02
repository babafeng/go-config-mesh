package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"config-mesh/internal/model"
	"config-mesh/internal/state"
	"config-mesh/internal/sync"
)

func loadPreviousUploadManifest(snapshotDir, snapshotID, deviceID string) (*model.Manifest, error) {
	manifestPath := filepath.Join(snapshotDir, "manifest.json.age")
	cipherManifest, err := readRegularFileBounded(manifestPath, 4<<20)
	if err != nil {
		return nil, fmt.Errorf("读取本机已有上传清单失败: %w", err)
	}
	_, plainManifest, err := decryptManifestWithLocalKeys(cipherManifest)
	if err != nil {
		return nil, fmt.Errorf("无法验证本机已有上传目录 %s: %w", snapshotID, err)
	}
	var manifest model.Manifest
	if err := json.Unmarshal(plainManifest, &manifest); err != nil {
		return nil, fmt.Errorf("解析本机已有上传清单失败: %w", err)
	}
	if manifest.Version != Version || manifest.SnapshotID != snapshotID {
		return nil, fmt.Errorf("本机已有上传清单与目录不匹配或版本不兼容")
	}
	if manifest.DeviceID != deviceID {
		return nil, fmt.Errorf("稳定上传目录属于另一台设备，拒绝覆盖: %s", snapshotID)
	}
	if len(manifest.Items) > 10_000 {
		return nil, fmt.Errorf("本机已有上传清单配置项过多: %d", len(manifest.Items))
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	seenIDs := make(map[string]bool, len(manifest.Items))
	seenVaultFiles := make(map[string]bool, len(manifest.Items))
	for i := range manifest.Items {
		item, err := sync.ValidateAndResolveConfigItem(homeDir, manifest.Items[i])
		if err != nil {
			return nil, fmt.Errorf("已有上传清单第 %d 项无效: %w", i+1, err)
		}
		if seenIDs[item.ID] || (!item.Deleted && seenVaultFiles[item.VaultFile]) {
			return nil, fmt.Errorf("已有上传清单包含重复 ID 或 vault 文件: %s", item.ID)
		}
		seenIDs[item.ID] = true
		if !item.Deleted {
			seenVaultFiles[item.VaultFile] = true
		}
		manifest.Items[i] = item
	}
	return &manifest, nil
}

func verifyItemsUnchangedSinceScan(items []model.ConfigItem) error {
	for _, item := range items {
		currentHash, err := state.CalculateItemHash(item)
		if err != nil {
			return fmt.Errorf("重新计算配置哈希失败 (%s): %w", item.Name, err)
		}
		expectedHash := item.ContentHash
		if item.Deleted {
			expectedHash = ""
		}
		if currentHash != expectedHash {
			return fmt.Errorf("配置在扫描选择期间发生变化，请重试: %s", item.Name)
		}
	}
	return nil
}

func canonicalUploadItem(item model.ConfigItem) model.ConfigItem {
	if item.Deleted {
		item.ContentHash = ""
		item.RemoteHash = ""
		item.PayloadHash = ""
		item.VaultFile = ""
		item.Size = 0
		item.FileMode = 0
	}
	return item
}

func manifestItemsByID(manifest *model.Manifest) map[string]model.ConfigItem {
	items := make(map[string]model.ConfigItem)
	if manifest == nil {
		return items
	}
	for _, item := range manifest.Items {
		items[item.ID] = item
	}
	return items
}

func canReuseEncryptedPayload(current, previous model.ConfigItem, recipientsUnchanged bool, previousSnapshotDir string) bool {
	if !recipientsUnchanged || current.Deleted || previous.Deleted || current.ContentHash == "" ||
		current.ContentHash != previous.ContentHash || current.IsDir != previous.IsDir ||
		current.VaultFile != previous.VaultFile || current.SecretKind != previous.SecretKind ||
		previous.PayloadHash == "" {
		return false
	}
	cipherPath := filepath.Join(previousSnapshotDir, "vault", previous.VaultFile)
	info, err := os.Lstat(cipherPath)
	return err == nil && info.Mode().IsRegular() && info.Size() >= 0 && info.Size() <= sync.MaxArchiveBytes+(1<<20)
}

func uploadManifestItemEquivalent(current, previous model.ConfigItem) bool {
	current = canonicalUploadItem(current)
	return current.ID == previous.ID &&
		current.Name == previous.Name &&
		current.Category == previous.Category &&
		current.RelHomePath == previous.RelHomePath &&
		current.VaultFile == previous.VaultFile &&
		current.IsDir == previous.IsDir &&
		current.FileMode == previous.FileMode &&
		current.ContentHash == previous.ContentHash &&
		current.Size == previous.Size &&
		current.Deleted == previous.Deleted &&
		current.SecretKind == previous.SecretKind
}

func uploadSnapshotUnchanged(
	selected []model.ConfigItem,
	previous *model.Manifest,
	recipientsHash string,
	previousSnapshotDir string,
) bool {
	if previous == nil || previous.RecipientsHash == "" || previous.RecipientsHash != recipientsHash ||
		len(selected) != len(previous.Items) {
		return false
	}
	previousByID := manifestItemsByID(previous)
	for _, item := range selected {
		item = canonicalUploadItem(item)
		old, ok := previousByID[item.ID]
		if !ok || !uploadManifestItemEquivalent(item, old) {
			return false
		}
		if !item.Deleted && !canReuseEncryptedPayload(item, old, true, previousSnapshotDir) {
			return false
		}
	}
	return true
}

func copyEncryptedPayload(sourcePath, destinationPath string) error {
	data, err := readRegularFileBounded(sourcePath, sync.MaxArchiveBytes+(1<<20))
	if err != nil {
		return err
	}
	if err := os.WriteFile(destinationPath, data, 0644); err != nil {
		return err
	}
	return nil
}
