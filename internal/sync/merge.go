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
	parentPerm := os.FileMode(0755)
	if isPrivateConfig(item) {
		parentPerm = 0700
	}
	parentDir := filepath.Dir(item.LocalPath)
	if err := os.MkdirAll(parentDir, parentPerm); err != nil {
		return fmt.Errorf("创建父目录失败: %w", err)
	}
	if isPrivateConfig(item) {
		_ = os.Chmod(parentDir, 0700)
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
	lowerPath := strings.ToLower(item.LocalPath)
	if strings.HasSuffix(lowerPath, ".json") || strings.HasSuffix(lowerPath, ".jsonc") {
		// JSON 结构化深度合并 (支持 Object 与 Array，并兼容带注释的 JSONC)
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
	lower := strings.ToLower(item.LocalPath)
	if strategy != model.StrategyAppend || item.IsDir || strings.HasSuffix(lower, ".json") || strings.HasSuffix(lower, ".jsonc") {
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
	parentPerm := os.FileMode(0755)
	if mode.Perm() == 0600 || isPrivateDirPath(parent) {
		parentPerm = 0700
	}
	if err := os.MkdirAll(parent, parentPerm); err != nil {
		return err
	}
	if parentPerm == 0700 {
		_ = os.Chmod(parent, 0700)
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

// cleanJSONC 剔除 JSONC 中的单行注释 //、多行块注释 /*...*/ 以及逗号尾随 (trailing comma)
func cleanJSONC(data []byte) []byte {
	var out bytes.Buffer
	out.Grow(len(data))
	inString := false
	escape := false
	n := len(data)

	for i := 0; i < n; i++ {
		c := data[i]

		if inString {
			out.WriteByte(c)
			if escape {
				escape = false
			} else if c == '\\' {
				escape = true
			} else if c == '"' {
				inString = false
			}
			continue
		}

		// 处于字符串外部
		if c == '"' {
			inString = true
			out.WriteByte(c)
			continue
		}

		// 单行注释 //
		if c == '/' && i+1 < n && data[i+1] == '/' {
			i += 2
			for i < n && data[i] != '\n' {
				i++
			}
			if i < n {
				out.WriteByte('\n')
			}
			continue
		}

		// 多行注释 /* ... */
		if c == '/' && i+1 < n && data[i+1] == '*' {
			i += 2
			for i+1 < n && !(data[i] == '*' && data[i+1] == '/') {
				if data[i] == '\n' {
					out.WriteByte('\n')
				}
				i++
			}
			i++ // 跳过 '/'
			continue
		}

		// 检查尾随逗号 (trailing comma): 紧随其后的非空白/非注释字符是 '}' 或 ']'
		if c == ',' {
			j := i + 1
			isTrailing := false
			for j < n {
				if data[j] == ' ' || data[j] == '\t' || data[j] == '\r' || data[j] == '\n' {
					j++
					continue
				}
				if data[j] == '/' && j+1 < n && data[j+1] == '/' {
					j += 2
					for j < n && data[j] != '\n' {
						j++
					}
					continue
				}
				if data[j] == '/' && j+1 < n && data[j+1] == '*' {
					j += 2
					for j+1 < n && !(data[j] == '*' && data[j+1] == '/') {
						j++
					}
					j += 2
					continue
				}
				if data[j] == '}' || data[j] == ']' {
					isTrailing = true
				}
				break
			}
			if isTrailing {
				continue // 跳过尾随逗号
			}
		}

		out.WriteByte(c)
	}

	return out.Bytes()
}

// deepMergeJSON 针对两个 JSON 文本做结构化合并，兼容 JSONC 注释与末尾逗号，支持 Object 与 Array 合并
func deepMergeJSON(localJSON, remoteJSON []byte) ([]byte, error) {
	cleanLocal := cleanJSONC(localJSON)
	cleanRemote := cleanJSONC(remoteJSON)

	// 1. 优先尝试作为 JSON Object (map) 递归合并
	var localMap, remoteMap map[string]interface{}
	errLocalMap := json.Unmarshal(cleanLocal, &localMap)
	errRemoteMap := json.Unmarshal(cleanRemote, &remoteMap)
	if errLocalMap == nil && errRemoteMap == nil {
		merged := mergeMaps(localMap, remoteMap)
		return json.MarshalIndent(merged, "", "  ")
	}

	// 2. 尝试作为 JSON Array 合并 (如 VSCode / Cursor keybindings.json)
	var localArray, remoteArray []interface{}
	errLocalArr := json.Unmarshal(cleanLocal, &localArray)
	errRemoteArr := json.Unmarshal(cleanRemote, &remoteArray)
	if errLocalArr == nil && errRemoteArr == nil {
		merged := mergeJSONArrays(localArray, remoteArray)
		return json.MarshalIndent(merged, "", "  ")
	}

	// 3. 错误诊断
	if errLocalMap != nil && errLocalArr != nil {
		return nil, fmt.Errorf("解析本地 JSON 失败: %w", errLocalMap)
	}
	if errRemoteMap != nil && errRemoteArr != nil {
		return nil, fmt.Errorf("解析远端 JSON 失败: %w", errRemoteMap)
	}
	return nil, fmt.Errorf("本地与远端 JSON 根数据类型不匹配，无法合并 (Map 与 Array 冲突)")
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

func mergeJSONArrays(a, b []interface{}) []interface{} {
	out := make([]interface{}, 0, len(a)+len(b))
	seen := make(map[string]bool, len(a)+len(b))

	for _, item := range a {
		key := canonicalJSONKey(item)
		seen[key] = true
		out = append(out, item)
	}
	for _, item := range b {
		key := canonicalJSONKey(item)
		if !seen[key] {
			seen[key] = true
			out = append(out, item)
		}
	}
	return out
}

func canonicalJSONKey(item interface{}) string {
	b, err := json.Marshal(item)
	if err != nil {
		return fmt.Sprintf("%v", item)
	}
	return string(b)
}

func isPrivateConfig(item model.ConfigItem) bool {
	if item.SecretKind != "" || item.Category == model.CategorySSH {
		return true
	}
	rel := filepath.ToSlash(item.RelHomePath)
	return strings.HasPrefix(rel, ".ssh/") || strings.HasPrefix(rel, ".aws/")
}

func isPrivateDirPath(dirPath string) bool {
	clean := filepath.ToSlash(dirPath)
	return strings.HasSuffix(clean, "/.ssh") || strings.HasSuffix(clean, "/.aws")
}

// ArchivePreviewEntry 记录一次目录打包决策；跳过的目录代表其所有子项也被跳过。
type ArchivePreviewEntry struct {
	Path     string
	Included bool
	Reason   string
	Size     int64
	IsDir    bool
}

// ArchivePreview 与真实打包共用遍历逻辑，仅记录路径和决策，不包含文件内容。
type ArchivePreview struct {
	Entries       []ArchivePreviewEntry
	IncludedCount int
	SkippedCount  int
	IncludedBytes int64
}

func (p *ArchivePreview) add(path string, included bool, reason string, size int64, isDir bool) {
	if p == nil {
		return
	}
	p.Entries = append(p.Entries, ArchivePreviewEntry{
		Path: filepath.ToSlash(path), Included: included, Reason: reason, Size: size, IsDir: isDir,
	})
	if included {
		p.IncludedCount++
		p.IncludedBytes += size
	} else {
		p.SkippedCount++
	}
}

// PreviewTarArchive 使用与 CreateTarArchive 相同的过滤和校验流程生成预览。
func PreviewTarArchive(srcDir string) (ArchivePreview, error) {
	var preview ArchivePreview
	_, err := createTarArchive(srcDir, &preview)
	return preview, err
}

// CreateTarArchive 将本地目录打包为 tar 字节切片 (自动跳过大缓存/临时文件、Socket 与特殊文件，妥善处理软链接与目录)
func CreateTarArchive(srcDir string) ([]byte, error) {
	return createTarArchive(srcDir, nil)
}

func createTarArchive(srcDir string, preview *ArchivePreview) ([]byte, error) {
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

		if scanner.IsSensitiveFile(path) {
			preview.add(relPath, false, "敏感路径", 0, info.IsDir())
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if scanner.ShouldIgnorePath(relPath) {
			preview.add(relPath, false, "忽略规则", 0, info.IsDir())
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
			preview.add(relPath, false, "特殊文件", 0, false)
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
				preview.add(relPath, false, "越界或绝对路径软链接", 0, false)
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
				preview.add(relPath, false, "内容检测到凭据", 0, false)
				return nil
			}
			totalSize += lInfo.Size()
		}
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if mode.IsDir() {
			preview.add(relPath, true, "", 0, true)
		} else if mode&os.ModeSymlink != 0 {
			preview.add(relPath, true, "软链接", 0, false)
		} else {
			preview.add(relPath, true, "", int64(len(fileData)), false)
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
