package sync

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"config-mesh/internal/model"
	"config-mesh/internal/scanner"
)

const (
	BlockStart = "# >>> config-mesh start >>>"
	BlockEnd   = "# <<< config-mesh end <<<"
)

// ApplyConfigItem 将解密后的远端数据应用到本地文件
func ApplyConfigItem(item model.ConfigItem, remoteData []byte, strategy model.Strategy) error {
	// 如果是跳过策略
	if strategy == model.StrategySkip {
		return nil
	}
	if item.Deleted {
		return os.RemoveAll(item.LocalPath)
	}

	if err := ValidateApplyStrategy(item, strategy); err != nil {
		return err
	}

	// 确保父目录存在
	if err := os.MkdirAll(filepath.Dir(item.LocalPath), 0755); err != nil {
		return fmt.Errorf("创建父目录失败: %w", err)
	}

	// 如果是目录类型 (如 ~/.config/nvim)，remoteData 是 tar 归档
	if item.IsDir {
		return applyTarArchive(item.LocalPath, remoteData, strategy)
	}

	// 单文件处理
	localExists := false
	var localData []byte
	if info, err := os.Stat(item.LocalPath); err == nil && !info.IsDir() {
		localExists = true
		localData, err = os.ReadFile(item.LocalPath)
		if err != nil {
			return fmt.Errorf("读取本地配置失败: %w", err)
		}
	}

	// 如果本地文件不存在，直接写入远端内容
	if !localExists || strategy == model.StrategyOverwrite || strategy == model.StrategyReplace {
		mode := os.FileMode(0644)
		if item.FileMode != 0 {
			mode = os.FileMode(item.FileMode)
		}
		return atomicWriteFile(item.LocalPath, remoteData, mode)
	}

	// 默认追加策略 (StrategyAppend)
	if strings.HasSuffix(item.LocalPath, ".json") {
		// JSON 结构化深度合并
		mergedData, err := deepMergeJSON(localData, remoteData)
		if err == nil {
			return atomicWriteFile(item.LocalPath, mergedData, os.FileMode(infoMode(item.LocalPath, item.FileMode)))
		}
		return fmt.Errorf("JSON 结构化合并失败，已拒绝自动覆盖: %w", err)
	}

	// 纯文本文件（如 Shell 脚本、Git 配置等）：采用标记块保护追加
	mergedText := applyMarkedBlock(string(localData), string(remoteData))
	return atomicWriteFile(item.LocalPath, []byte(mergedText), os.FileMode(infoMode(item.LocalPath, item.FileMode)))
}

// ValidateApplyStrategy 在写入前拒绝会破坏语法或语义的通用追加。
func ValidateApplyStrategy(item model.ConfigItem, strategy model.Strategy) error {
	if item.Deleted {
		return nil
	}
	if item.SecretKind != "" && strategy != model.StrategyOverwrite && strategy != model.StrategyReplace {
		return fmt.Errorf("凭据 %s 只能使用 overwrite 策略", item.RelHomePath)
	}
	if strategy != model.StrategyAppend || item.IsDir || strings.HasSuffix(strings.ToLower(item.LocalPath), ".json") {
		return nil
	}
	base := strings.ToLower(filepath.Base(item.LocalPath))
	supported := map[string]bool{
		".zshrc": true, ".zprofile": true, ".zshenv": true,
		".bashrc": true, ".bash_profile": true, "config.fish": true,
		".tmux.conf": true, ".gitconfig": true, ".gitignore_global": true,
	}
	if !supported[base] {
		return fmt.Errorf("文件 %s 不支持安全追加，请使用 overwrite 策略", item.RelHomePath)
	}
	return nil
}

func infoMode(filePath string, fallback uint32) uint32 {
	if info, err := os.Stat(filePath); err == nil {
		return uint32(info.Mode().Perm())
	}
	if fallback != 0 {
		return fallback
	}
	return 0644
}

func atomicWriteFile(filePath string, data []byte, mode os.FileMode) error {
	parent := filepath.Dir(filePath)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(parent, ".config-mesh-write-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode.Perm()); err != nil {
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
	if err := os.Rename(tmpName, filePath); err != nil {
		return err
	}
	return nil
}

// applyMarkedBlock 在纯文本中插入或替换标记块
func applyMarkedBlock(originalText, syncText string) string {
	syncBlock := fmt.Sprintf("%s\n%s\n%s", BlockStart, strings.TrimSpace(syncText), BlockEnd)

	// 使用正则匹配已存在的标记块
	re := regexp.MustCompile(fmt.Sprintf(`(?s)%s.*?%s`, regexp.QuoteMeta(BlockStart), regexp.QuoteMeta(BlockEnd)))
	if re.MatchString(originalText) {
		return re.ReplaceAllString(originalText, syncBlock)
	}

	// 若不存在标记块，则追加到尾部
	trimmedOriginal := strings.TrimRight(originalText, "\r\n")
	if len(trimmedOriginal) == 0 {
		return syncBlock + "\n"
	}
	return trimmedOriginal + "\n\n" + syncBlock + "\n"
}

// deepMergeJSON 针对两个 JSON 文本做 key-value 递归合并
func deepMergeJSON(localJSON, remoteJSON []byte) ([]byte, error) {
	var localMap, remoteMap map[string]interface{}
	if err := json.Unmarshal(localJSON, &localMap); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(remoteJSON, &remoteMap); err != nil {
		return nil, err
	}

	merged := mergeMaps(localMap, remoteMap)
	return json.MarshalIndent(merged, "", "  ")
}

func mergeMaps(a, b map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(a))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		if vMap, ok := v.(map[string]interface{}); ok {
			if aMap, ok := out[k].(map[string]interface{}); ok {
				out[k] = mergeMaps(aMap, vMap)
				continue
			}
		}
		out[k] = v
	}
	return out
}

// CreateTarArchive 将本地目录打包为 tar 字节切片 (自动跳过大缓存/临时文件、Socket 与特殊文件，妥善处理软链接与目录)
func CreateTarArchive(srcDir string) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	var totalSize int64
	entries := 0

	err := filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(srcDir, path)
		if err != nil || relPath == "." {
			return nil
		}

		if scanner.IsSensitiveFile(path) || scanner.ShouldIgnorePath(relPath) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// 获取 Lstat 信息以准确识别软链接和特殊文件
		lInfo, err := os.Lstat(path)
		if err != nil {
			return err
		}

		mode := lInfo.Mode()

		// 1. 跳过 Socket、Named Pipe、设备等特殊文件
		if mode&(os.ModeSocket|os.ModeNamedPipe|os.ModeDevice|os.ModeIrregular) != 0 {
			return nil
		}
		// 2. 软链接处理
		var linkTarget string
		if mode&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			linkTarget = target
			if err := validateTarLink(filepath.ToSlash(relPath), linkTarget); err != nil {
				// 绝对路径或越界软链接在其他设备上既不可移植又不安全，不写入归档。
				return nil
			}
		}

		header, err := tar.FileInfoHeader(lInfo, linkTarget)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relPath)
		if linkTarget != "" {
			header.Linkname = linkTarget
			header.Typeflag = tar.TypeSymlink
		}

		entries++
		if entries > MaxArchiveFiles {
			return fmt.Errorf("归档文件数超过上限 %d", MaxArchiveFiles)
		}
		var fileData []byte
		if mode.IsRegular() {
			if lInfo.Size() < 0 || lInfo.Size() > MaxArchiveBytes-totalSize {
				return fmt.Errorf("归档大小超过上限 %d 字节", MaxArchiveBytes)
			}
			fileData, err = readFileAtMost(path, MaxArchiveBytes-totalSize)
			if err != nil {
				return err
			}
			if int64(len(fileData)) != lInfo.Size() {
				return fmt.Errorf("打包期间文件发生变化: %s", path)
			}
			if scanner.ContainsSensitiveContent(fileData) {
				return nil
			}
			totalSize += lInfo.Size()
		}
		if err := tw.WriteHeader(header); err != nil {
			return err
		}

		// 如果是软链接或目录，写完 Header 后直接跳过文件内容复制
		if mode&os.ModeSymlink != 0 || mode.IsDir() {
			return nil
		}

		// 3. 常规普通文件，复制内容
		_, err = tw.Write(fileData)
		return err
	})

	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func readFileAtMost(filePath string, maxBytes int64) ([]byte, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("文件读取期间增长并超过 %d 字节上限: %s", maxBytes, filePath)
	}
	return data, nil
}

// applyTarArchive 解压 tar 归档到目标目录
func applyTarArchive(dstDir string, tarData []byte, strategy model.Strategy) error {
	if err := ValidateTarArchive(tarData); err != nil {
		return err
	}
	parent := filepath.Dir(dstDir)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return err
	}
	stageDir, err := os.MkdirTemp(parent, ".config-mesh-stage-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stageDir)

	if strategy == model.StrategyAppend {
		if info, statErr := os.Stat(dstDir); statErr == nil && info.IsDir() {
			if err := copyDir(dstDir, stageDir); err != nil {
				return fmt.Errorf("构建目录合并快照失败: %w", err)
			}
		} else if statErr != nil && !os.IsNotExist(statErr) {
			return statErr
		}
	}
	if err := extractTarArchive(stageDir, tarData); err != nil {
		return err
	}

	oldDir, err := os.MkdirTemp(parent, ".config-mesh-old-*")
	if err != nil {
		return err
	}
	if err := os.Remove(oldDir); err != nil {
		return err
	}
	hadOld := false
	if _, statErr := os.Lstat(dstDir); statErr == nil {
		if err := os.Rename(dstDir, oldDir); err != nil {
			return fmt.Errorf("移出旧目录失败: %w", err)
		}
		hadOld = true
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	if err := os.Rename(stageDir, dstDir); err != nil {
		if hadOld {
			_ = os.Rename(oldDir, dstDir)
		}
		return fmt.Errorf("原子替换配置目录失败: %w", err)
	}
	if hadOld {
		if err := os.RemoveAll(oldDir); err != nil {
			return fmt.Errorf("清理旧目录失败: %w", err)
		}
	}
	return nil
}

type pendingSymlink struct {
	name string
	link string
}

func extractTarArchive(dstDir string, tarData []byte) error {
	tr := tar.NewReader(bytes.NewReader(tarData))
	var links []pendingSymlink
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		name, err := validateTarName(header.Name)
		if err != nil {
			return err
		}
		targetPath, err := safeJoin(dstDir, filepath.FromSlash(name))
		if err != nil {
			return err
		}
		if err := ensureNoSymlinkParents(dstDir, targetPath); err != nil {
			return err
		}

		switch header.Typeflag {
		case tar.TypeDir:
			mode := header.FileInfo().Mode().Perm()
			if err := os.MkdirAll(targetPath, mode); err != nil {
				return err
			}
			if err := os.Chmod(targetPath, mode); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := validateTarLink(name, header.Linkname); err != nil {
				return err
			}
			links = append(links, pendingSymlink{name: name, link: header.Linkname})
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
				return err
			}
			if err := os.RemoveAll(targetPath); err != nil {
				return err
			}
			fileMode := header.FileInfo().Mode().Perm()
			outFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, fileMode)
			if err != nil {
				return err
			}
			if _, err := io.Copy(outFile, tr); err != nil {
				outFile.Close()
				return err
			}
			if err := outFile.Sync(); err != nil {
				outFile.Close()
				return err
			}
			if err := outFile.Close(); err != nil {
				return err
			}
			if err := os.Chmod(targetPath, fileMode); err != nil {
				return err
			}
		default:
			return fmt.Errorf("不支持的 tar 条目类型 %d", header.Typeflag)
		}
	}

	// 最后创建软链接，普通文件写入时就不可能穿过它们。
	for _, link := range links {
		targetPath, err := safeJoin(dstDir, filepath.FromSlash(link.name))
		if err != nil {
			return err
		}
		if err := ensureNoSymlinkParents(dstDir, targetPath); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
			return err
		}
		if err := os.RemoveAll(targetPath); err != nil {
			return err
		}
		if err := os.Symlink(link.link, targetPath); err != nil {
			return err
		}
	}
	return nil
}
