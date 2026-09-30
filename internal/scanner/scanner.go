package scanner

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"config-mesh/internal/crypto"
	"config-mesh/internal/model"
)

// DefaultPresets 定义 macOS 系统下全面的开发、AI、云原生与 IDE 配置预设
var DefaultPresets = []struct {
	ID          string
	Name        string
	Category    model.ConfigCategory
	RelHomePath string
	VaultFile   string
	IsDir       bool
	Recommended bool
	SecretKind  model.SecretKind
}{
	// ==================== 1. AI 助手 & 命令行工具 ====================
	{ID: "ai_claude_json", Name: "Claude Code (~/.claude.json)", Category: model.CategoryAI, RelHomePath: ".claude.json", VaultFile: "claude_root.json.age", IsDir: false, Recommended: true},
	{ID: "ai_claude_md", Name: "Claude Code Guidelines (~/.claude/CLAUDE.md)", Category: model.CategoryAI, RelHomePath: ".claude/CLAUDE.md", VaultFile: "claude_md.age", IsDir: false, Recommended: true},
	{ID: "ai_claude_commands", Name: "Claude Code Commands (~/.claude/commands)", Category: model.CategoryAI, RelHomePath: ".claude/commands", VaultFile: "claude_commands.tar.age", IsDir: true, Recommended: true},
	{ID: "ai_codex_config", Name: "Codex Config (~/.codex/config.toml)", Category: model.CategoryAI, RelHomePath: ".codex/config.toml", VaultFile: "codex_config.toml.age", IsDir: false, Recommended: true},
	{ID: "ai_codex_agents", Name: "Codex Guidelines (~/.codex/AGENTS.md)", Category: model.CategoryAI, RelHomePath: ".codex/AGENTS.md", VaultFile: "codex_agents.age", IsDir: false, Recommended: true},
	{ID: "ai_codex_skills", Name: "Codex Skills (~/.codex/skills)", Category: model.CategoryAI, RelHomePath: ".codex/skills", VaultFile: "codex_skills.tar.age", IsDir: true, Recommended: false},
	{ID: "ai_antigravity_ide_settings", Name: "Antigravity IDE Settings (~/Library/Application Support/Antigravity/User/settings.json)", Category: model.CategoryAI, RelHomePath: "Library/Application Support/Antigravity/User/settings.json", VaultFile: "antigravity_ide_settings.json.age", IsDir: false, Recommended: true},
	{ID: "ai_antigravity_ide_keybindings", Name: "Antigravity IDE Keybindings (~/Library/Application Support/Antigravity/User/keybindings.json)", Category: model.CategoryAI, RelHomePath: "Library/Application Support/Antigravity/User/keybindings.json", VaultFile: "antigravity_ide_keybindings.json.age", IsDir: false, Recommended: true},
	{ID: "ai_antigravity_ide_snippets", Name: "Antigravity IDE Snippets (~/Library/Application Support/Antigravity/User/snippets)", Category: model.CategoryAI, RelHomePath: "Library/Application Support/Antigravity/User/snippets", VaultFile: "antigravity_ide_snippets.tar.age", IsDir: true, Recommended: true},
	{ID: "ai_antigravity_argv", Name: "Antigravity Launch Args (~/.antigravity/argv.json)", Category: model.CategoryAI, RelHomePath: ".antigravity/argv.json", VaultFile: "antigravity_argv.json.age", IsDir: false, Recommended: true},
	{ID: "ai_antigravity_cli_settings", Name: "Antigravity CLI Settings (~/.gemini/antigravity-cli/settings.json)", Category: model.CategoryAI, RelHomePath: ".gemini/antigravity-cli/settings.json", VaultFile: "antigravity_cli_settings.json.age", IsDir: false, Recommended: true},
	{ID: "ai_antigravity_cli_keybindings", Name: "Antigravity CLI Keybindings (~/.gemini/antigravity-cli/keybindings.json)", Category: model.CategoryAI, RelHomePath: ".gemini/antigravity-cli/keybindings.json", VaultFile: "antigravity_cli_keybindings.json.age", IsDir: false, Recommended: true},
	{ID: "ai_antigravity_cli_mcp", Name: "Antigravity CLI MCP Config (~/.gemini/antigravity-cli/mcp_config.json)", Category: model.CategoryAI, RelHomePath: ".gemini/antigravity-cli/mcp_config.json", VaultFile: "antigravity_cli_mcp.json.age", IsDir: false, Recommended: false},
	{ID: "ai_gemini_config", Name: "Gemini / Antigravity Shared Config (~/.gemini/config)", Category: model.CategoryAI, RelHomePath: ".gemini/config", VaultFile: "gemini_config.tar.age", IsDir: true, Recommended: true},
	{ID: "ai_gemini_settings", Name: "Gemini Settings (~/.gemini/settings.json)", Category: model.CategoryAI, RelHomePath: ".gemini/settings.json", VaultFile: "gemini_settings.json.age", IsDir: false, Recommended: true},
	{ID: "ai_gemini_md", Name: "Gemini Guidelines (~/.gemini/GEMINI.md)", Category: model.CategoryAI, RelHomePath: ".gemini/GEMINI.md", VaultFile: "gemini_md.age", IsDir: false, Recommended: true},
	{ID: "ai_gemini_projects", Name: "Gemini Projects (~/.gemini/projects.json)", Category: model.CategoryAI, RelHomePath: ".gemini/projects.json", VaultFile: "gemini_projects.json.age", IsDir: false, Recommended: true},
	{ID: "ai_cursor_settings", Name: "Cursor Settings (~/Library/Application Support/Cursor/User/settings.json)", Category: model.CategoryAI, RelHomePath: "Library/Application Support/Cursor/User/settings.json", VaultFile: "cursor_settings.json.age", IsDir: false, Recommended: true},
	{ID: "ai_cursor_keybindings", Name: "Cursor Keybindings (~/Library/Application Support/Cursor/User/keybindings.json)", Category: model.CategoryAI, RelHomePath: "Library/Application Support/Cursor/User/keybindings.json", VaultFile: "cursor_keybindings.json.age", IsDir: false, Recommended: true},
	{ID: "ai_cursor_snippets", Name: "Cursor Snippets (~/Library/Application Support/Cursor/User/snippets)", Category: model.CategoryAI, RelHomePath: "Library/Application Support/Cursor/User/snippets", VaultFile: "cursor_snippets.tar.age", IsDir: true, Recommended: true},
	{ID: "ai_continue", Name: "Continue.dev Config (~/.continue)", Category: model.CategoryAI, RelHomePath: ".continue/config.json", VaultFile: "continue_config.json.age", IsDir: false, Recommended: true},
	{ID: "ai_aider", Name: "Aider Config (~/.aider.conf.yml)", Category: model.CategoryAI, RelHomePath: ".aider.conf.yml", VaultFile: "aider_conf.yml.age", IsDir: false, Recommended: true},
	{ID: "ai_opencode", Name: "OpenCode Config (~/.config/opencode)", Category: model.CategoryAI, RelHomePath: ".config/opencode/opencode.jsonc", VaultFile: "opencode.jsonc.age", IsDir: false, Recommended: false},
	{ID: "ai_mimocode", Name: "MimoCode Config (~/.config/mimocode)", Category: model.CategoryAI, RelHomePath: ".config/mimocode/mimocode.jsonc", VaultFile: "mimocode.jsonc.age", IsDir: false, Recommended: false},

	// ==================== 2. SSH 配置（私钥由 scanSSHKeyFiles 动态发现） ====================
	{ID: "ssh_config", Name: "SSH Config (~/.ssh/config)", Category: model.CategorySSH, RelHomePath: ".ssh/config", VaultFile: "ssh_config.age", IsDir: false, Recommended: true},

	// ==================== 3. 云平台 & DevOps CLI (整目录同步) ====================
	{ID: "cloud_aws_config", Name: "AWS CLI Config (~/.aws/config，不含 credentials)", Category: model.CategoryCloud, RelHomePath: ".aws/config", VaultFile: "aws_config.age", IsDir: false, Recommended: false},
	{ID: "cloud_aws_credentials", Name: "AWS Credentials (~/.aws/credentials)", Category: model.CategoryCloud, RelHomePath: ".aws/credentials", VaultFile: "aws_credentials.age", IsDir: false, Recommended: false, SecretKind: model.SecretKindAWSCredentials},
	{ID: "cloud_aliyun_config", Name: "Alibaba Cloud CLI Credentials (~/.aliyun/config.json)", Category: model.CategoryCloud, RelHomePath: ".aliyun/config.json", VaultFile: "aliyun_config.age", IsDir: false, Recommended: false, SecretKind: model.SecretKindAliyunConfig},

	// ==================== 4. 编辑器 & IDE ====================
	{ID: "editor_vscode_settings", Name: "VSCode Settings (~/Library/Application Support/Code/User/settings.json)", Category: model.CategoryEditor, RelHomePath: "Library/Application Support/Code/User/settings.json", VaultFile: "vscode_settings.json.age", IsDir: false, Recommended: true},
	{ID: "editor_vscode_keybindings", Name: "VSCode Keybindings (~/Library/Application Support/Code/User/keybindings.json)", Category: model.CategoryEditor, RelHomePath: "Library/Application Support/Code/User/keybindings.json", VaultFile: "vscode_keybindings.json.age", IsDir: false, Recommended: true},
	{ID: "editor_vscode_snippets", Name: "VSCode Snippets (~/Library/Application Support/Code/User/snippets)", Category: model.CategoryEditor, RelHomePath: "Library/Application Support/Code/User/snippets", VaultFile: "vscode_snippets.tar.age", IsDir: true, Recommended: true},
	{ID: "editor_vscode_mcp", Name: "VSCode MCP Config (~/Library/Application Support/Code/User/mcp.json)", Category: model.CategoryEditor, RelHomePath: "Library/Application Support/Code/User/mcp.json", VaultFile: "vscode_mcp.json.age", IsDir: false, Recommended: true},
	{ID: "editor_vscode_chat_models", Name: "VSCode Chat Models Config (~/Library/Application Support/Code/User/chatLanguageModels.json)", Category: model.CategoryEditor, RelHomePath: "Library/Application Support/Code/User/chatLanguageModels.json", VaultFile: "vscode_chat_models.json.age", IsDir: false, Recommended: true},
	{ID: "editor_vscode_argv", Name: "VSCode Launch Args (~/.vscode/argv.json)", Category: model.CategoryEditor, RelHomePath: ".vscode/argv.json", VaultFile: "vscode_argv.json.age", IsDir: false, Recommended: true},
	{ID: "editor_nvim", Name: "Neovim (~/.config/nvim)", Category: model.CategoryEditor, RelHomePath: ".config/nvim", VaultFile: "editor_nvim.tar.age", IsDir: true, Recommended: true},
	{ID: "editor_vimrc", Name: "Vim (~/.vimrc)", Category: model.CategoryEditor, RelHomePath: ".vimrc", VaultFile: "editor_vimrc.age", IsDir: false, Recommended: false},
	{ID: "editor_zed_settings", Name: "Zed Settings (~/.config/zed/settings.json)", Category: model.CategoryEditor, RelHomePath: ".config/zed/settings.json", VaultFile: "zed_settings.json.age", IsDir: false, Recommended: true},
	{ID: "editor_zed_keymap", Name: "Zed Keymap (~/.config/zed/keymap.json)", Category: model.CategoryEditor, RelHomePath: ".config/zed/keymap.json", VaultFile: "zed_keymap.json.age", IsDir: false, Recommended: true},
	{ID: "editor_helix", Name: "Helix Config (~/.config/helix/config.toml)", Category: model.CategoryEditor, RelHomePath: ".config/helix/config.toml", VaultFile: "helix_config.toml.age", IsDir: false, Recommended: true},

	// ==================== 5. Shell & 终端 ====================
	{ID: "shell_zshrc", Name: "~/.zshrc", Category: model.CategoryShell, RelHomePath: ".zshrc", VaultFile: "shell_zshrc.age", IsDir: false, Recommended: true},
	{ID: "shell_zprofile", Name: "~/.zprofile", Category: model.CategoryShell, RelHomePath: ".zprofile", VaultFile: "shell_zprofile.age", IsDir: false, Recommended: true},
	{ID: "shell_zshenv", Name: "~/.zshenv", Category: model.CategoryShell, RelHomePath: ".zshenv", VaultFile: "shell_zshenv.age", IsDir: false, Recommended: false},
	{ID: "shell_bashrc", Name: "~/.bashrc", Category: model.CategoryShell, RelHomePath: ".bashrc", VaultFile: "shell_bashrc.age", IsDir: false, Recommended: false},
	{ID: "shell_bash_profile", Name: "~/.bash_profile", Category: model.CategoryShell, RelHomePath: ".bash_profile", VaultFile: "shell_bash_profile.age", IsDir: false, Recommended: false},
	{ID: "shell_tmux", Name: "~/.tmux.conf", Category: model.CategoryShell, RelHomePath: ".tmux.conf", VaultFile: "shell_tmux.age", IsDir: false, Recommended: true},
	{ID: "shell_starship", Name: "~/.config/starship.toml", Category: model.CategoryShell, RelHomePath: ".config/starship.toml", VaultFile: "shell_starship.age", IsDir: false, Recommended: true},
	{ID: "shell_fish", Name: "~/.config/fish/config.fish", Category: model.CategoryShell, RelHomePath: ".config/fish/config.fish", VaultFile: "shell_fish.age", IsDir: false, Recommended: false},

	// ==================== 6. Git & 开发 ====================
	{ID: "git_config", Name: "~/.gitconfig", Category: model.CategoryGit, RelHomePath: ".gitconfig", VaultFile: "git_gitconfig.age", IsDir: false, Recommended: true},
	{ID: "git_ignore_global", Name: "~/.gitignore_global", Category: model.CategoryGit, RelHomePath: ".gitignore_global", VaultFile: "git_gitignore_global.age", IsDir: false, Recommended: true},
	{ID: "git_gh_cli", Name: "GitHub CLI Config (~/.config/gh/config.yml)", Category: model.CategoryGit, RelHomePath: ".config/gh/config.yml", VaultFile: "gh_cli_config.yml.age", IsDir: false, Recommended: true},

	// ==================== 7. 开发工具 & 终端模拟器 ====================
	{ID: "tool_ghostty", Name: "Ghostty Config (~/.config/ghostty/config)", Category: model.CategoryTools, RelHomePath: ".config/ghostty/config", VaultFile: "tool_ghostty.age", IsDir: false, Recommended: true},
	{ID: "tool_alacritty", Name: "Alacritty (~/.config/alacritty/alacritty.toml)", Category: model.CategoryTools, RelHomePath: ".config/alacritty/alacritty.toml", VaultFile: "tool_alacritty.age", IsDir: false, Recommended: false},
	{ID: "tool_kitty", Name: "Kitty (~/.config/kitty/kitty.conf)", Category: model.CategoryTools, RelHomePath: ".config/kitty/kitty.conf", VaultFile: "tool_kitty.age", IsDir: false, Recommended: false},
	{ID: "tool_wezterm", Name: "WezTerm (~/.config/wezterm/wezterm.lua)", Category: model.CategoryTools, RelHomePath: ".config/wezterm/wezterm.lua", VaultFile: "tool_wezterm.lua.age", IsDir: false, Recommended: false},
	{ID: "tool_zellij", Name: "Zellij Config (~/.config/zellij)", Category: model.CategoryTools, RelHomePath: ".config/zellij", VaultFile: "tool_zellij.tar.age", IsDir: true, Recommended: false},
	{ID: "tool_karabiner", Name: "Karabiner Elements", Category: model.CategoryTools, RelHomePath: ".config/karabiner/karabiner.json", VaultFile: "tool_karabiner.age", IsDir: false, Recommended: false},
	{ID: "tool_aerospace", Name: "AeroSpace Config (~/.aerospace.toml)", Category: model.CategoryTools, RelHomePath: ".aerospace.toml", VaultFile: "tool_aerospace.age", IsDir: false, Recommended: false},

	// ==================== 8. 包管理 & 语言环境 ====================
	{ID: "pkg_brewfile", Name: "Homebrew (~/.Brewfile)", Category: model.CategoryPackage, RelHomePath: ".Brewfile", VaultFile: "pkg_brewfile.age", IsDir: false, Recommended: true},
	{ID: "pkg_cargo", Name: "Rust Cargo (~/.cargo/config.toml)", Category: model.CategoryPackage, RelHomePath: ".cargo/config.toml", VaultFile: "pkg_cargo.age", IsDir: false, Recommended: false},
	{ID: "pkg_go_env", Name: "Go Environment (~/.config/go/env)", Category: model.CategoryPackage, RelHomePath: ".config/go/env", VaultFile: "go_env.age", IsDir: false, Recommended: false},
}

// Scanner 配置扫描器
type Scanner struct {
	HomeDir string
}

// NewScanner 创建扫描器实例
func NewScanner() (*Scanner, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("获取家目录失败: %w", err)
	}
	return &Scanner{HomeDir: homeDir}, nil
}

// Scan 扫描本地所有预设配置（仅保留本地存在的文件）
func (s *Scanner) Scan() ([]model.ConfigItem, error) {
	var items []model.ConfigItem

	for _, preset := range DefaultPresets {
		absPath := filepath.Join(s.HomeDir, preset.RelHomePath)

		// 普通配置继续执行敏感拦截；声明的凭据必须匹配 secret_kind 精确白名单。
		pathIsSensitive := IsSensitiveConfigPath(preset.RelHomePath, preset.IsDir) || IsSensitiveFile(absPath)
		if pathIsSensitive && !IsAllowedSecretConfigPath(preset.RelHomePath, preset.IsDir, preset.SecretKind) {
			continue
		}

		info, err := os.Lstat(absPath)
		// 本地不存在则直接跳过
		if err != nil || os.IsNotExist(err) || info.Mode()&os.ModeSymlink != 0 ||
			(!info.Mode().IsRegular() && !info.IsDir()) || info.IsDir() != preset.IsDir {
			continue
		}
		secretKind := preset.SecretKind
		if !preset.IsDir && secretKind == "" {
			sensitive, inspectErr := FileContainsSensitiveContent(absPath)
			if inspectErr != nil {
				return nil, fmt.Errorf("检查配置内容失败 (%s): %w", preset.Name, inspectErr)
			}
			if sensitive {
				secretKind = model.SecretKindDetectedConfig
			}
		}

		item := model.ConfigItem{
			ID:          preset.ID,
			Name:        preset.Name,
			Category:    preset.Category,
			LocalPath:   absPath,
			RelHomePath: preset.RelHomePath,
			VaultFile:   preset.VaultFile,
			IsDir:       preset.IsDir,
			Recommended: preset.Recommended,
			Exists:      true,
			Selected:    preset.Recommended,
			FileMode:    uint32(info.Mode().Perm()),
			SecretKind:  secretKind,
		}
		if item.SecretKind != "" {
			item.FileMode = 0600
		}

		if preset.IsDir {
			item.Size = CalculateDirSize(absPath)
		} else {
			item.Size = info.Size()
			if hash, err := crypto.CalculateFileSHA256(absPath); err == nil {
				item.ContentHash = hash
			}
		}

		items = append(items, item)
	}

	claudeItems, err := s.scanClaudeSettingsFiles()
	if err != nil {
		return nil, err
	}
	if len(claudeItems) > 0 {
		insertAt := 0
		for i, item := range items {
			if item.Category == model.CategoryAI {
				insertAt = i + 1
			}
		}
		items = append(items, make([]model.ConfigItem, len(claudeItems))...)
		copy(items[insertAt+len(claudeItems):], items[insertAt:len(items)-len(claudeItems)])
		copy(items[insertAt:], claudeItems)
	}

	sshKeyItems, err := s.scanSSHKeyFiles()
	if err != nil {
		return nil, err
	}
	// 动态发现的 SSH 密钥插入 SSH 分类末尾，避免 TUI 出现两个分离的 SSH 大项。
	insertAt := len(items)
	for i, item := range items {
		if item.Category == model.CategorySSH {
			insertAt = i + 1
		}
	}
	items = append(items, make([]model.ConfigItem, len(sshKeyItems))...)
	copy(items[insertAt+len(sshKeyItems):], items[insertAt:len(items)-len(sshKeyItems)])
	copy(items[insertAt:], sshKeyItems)

	return items, nil
}

func (s *Scanner) scanSSHKeyFiles() ([]model.ConfigItem, error) {
	sshDir := filepath.Join(s.HomeDir, ".ssh")
	entries, err := os.ReadDir(sshDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取 ~/.ssh 失败: %w", err)
	}

	var items []model.ConfigItem
	for _, entry := range entries {
		name := entry.Name()
		if !safeSecretFilename.MatchString(name) || strings.HasSuffix(name, ".pub") {
			continue
		}
		relPath := filepath.ToSlash(filepath.Join(".ssh", name))
		if !IsAllowedSecretConfigPath(relPath, false, model.SecretKindSSHPrivateKey) {
			continue
		}
		privatePath := filepath.Join(sshDir, name)
		info, err := os.Lstat(privatePath)
		if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > MaxContentInspectionBytes {
			continue
		}
		privateData, err := readScannerFile(privatePath, MaxContentInspectionBytes)
		if err != nil {
			return nil, fmt.Errorf("读取 SSH 私钥候选失败 (%s): %w", name, err)
		}
		if !crypto.IsSSHPrivateKeyData(privateData) {
			continue
		}
		safeName := name
		items = append(items, model.ConfigItem{
			ID: "ssh_private_" + safeName, Name: "SSH Private Key (~/.ssh/" + name + ")",
			Category: model.CategorySSH, LocalPath: privatePath, RelHomePath: relPath,
			VaultFile: "ssh_private_" + safeName + ".age", Recommended: false,
			Exists: true, Selected: false, FileMode: 0600, Size: info.Size(),
			SecretKind: model.SecretKindSSHPrivateKey,
		})

		publicName := name + ".pub"
		publicPath := filepath.Join(sshDir, publicName)
		publicInfo, statErr := os.Lstat(publicPath)
		if statErr != nil || !publicInfo.Mode().IsRegular() || publicInfo.Size() < 0 || publicInfo.Size() > 1<<20 {
			continue
		}
		publicData, readErr := readScannerFile(publicPath, 1<<20)
		if readErr != nil || !crypto.IsSSHPublicKeyData(publicData) {
			continue
		}
		items = append(items, model.ConfigItem{
			ID: "ssh_public_" + safeName, Name: "SSH Public Key (~/.ssh/" + publicName + ")",
			Category: model.CategorySSH, LocalPath: publicPath,
			RelHomePath: filepath.ToSlash(filepath.Join(".ssh", publicName)),
			VaultFile:   "ssh_public_" + safeName + ".age", Recommended: false,
			Exists: true, Selected: false, FileMode: uint32(publicInfo.Mode().Perm()), Size: publicInfo.Size(),
		})
	}
	return items, nil
}

func readScannerFile(filePath string, maxBytes int64) ([]byte, error) {
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
		return nil, fmt.Errorf("文件超过 %d 字节上限: %s", maxBytes, filePath)
	}
	return data, nil
}

// CalculateDirSize 递归计算目录内有效配置文件的总大小 (自动跳过大缓存/临时文件)
func CalculateDirSize(dirPath string) int64 {
	var totalSize int64
	_ = filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}

		relPath, _ := filepath.Rel(dirPath, path)
		if relPath != "." && (ShouldIgnorePath(relPath) || IsSensitiveFile(path)) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if info.Mode().IsRegular() {
			sensitive, inspectErr := FileContainsSensitiveContent(path)
			if inspectErr != nil || sensitive {
				return nil
			}
			totalSize += info.Size()
		}
		return nil
	})
	return totalSize
}

func (s *Scanner) scanClaudeSettingsFiles() ([]model.ConfigItem, error) {
	claudeDir := filepath.Join(s.HomeDir, ".claude")
	entries, err := os.ReadDir(claudeDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取 ~/.claude 目录失败: %w", err)
	}

	var items []model.ConfigItem
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !IsClaudeSettingsFilename(name) {
			continue
		}
		filePath := filepath.Join(claudeDir, name)
		info, err := os.Lstat(filePath)
		if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > MaxContentInspectionBytes {
			continue
		}

		relPath := filepath.ToSlash(filepath.Join(".claude", name))
		secretKind := model.SecretKind("")
		sensitive, inspectErr := FileContainsSensitiveContent(filePath)
		if inspectErr != nil {
			return nil, fmt.Errorf("检查配置内容失败 (%s): %w", name, inspectErr)
		}
		if sensitive {
			secretKind = model.SecretKindDetectedConfig
		}

		id := "ai_claude_" + sanitizePresetID(name)
		vaultFile := "claude_" + sanitizeVaultFilename(name) + ".age"

		item := model.ConfigItem{
			ID:          id,
			Name:        fmt.Sprintf("Claude Code Settings (~/.claude/%s)", name),
			Category:    model.CategoryAI,
			LocalPath:   filePath,
			RelHomePath: relPath,
			VaultFile:   vaultFile,
			IsDir:       false,
			Recommended: true,
			Exists:      true,
			Selected:    true,
			FileMode:    uint32(info.Mode().Perm()),
			Size:        info.Size(),
			SecretKind:  secretKind,
		}
		if item.SecretKind != "" {
			item.FileMode = 0600
		}
		if hash, err := crypto.CalculateFileSHA256(filePath); err == nil {
			item.ContentHash = hash
		}

		items = append(items, item)
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].RelHomePath < items[j].RelHomePath
	})

	return items, nil
}

var nonAlphaNumRegex = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

func sanitizePresetID(name string) string {
	cleaned := nonAlphaNumRegex.ReplaceAllString(name, "_")
	cleaned = strings.Trim(cleaned, "_")
	if cleaned == "settings_json" || cleaned == "settings" {
		return "settings"
	}
	return cleaned
}

func sanitizeVaultFilename(name string) string {
	cleaned := nonAlphaNumRegex.ReplaceAllString(name, "_")
	cleaned = strings.Trim(cleaned, "_")
	if cleaned == "settings_json" || cleaned == "settings" {
		return "settings.json"
	}
	return cleaned
}

// FormatSize 人性化格式化字节大小 (如 512 B, 12.4 KB, 3.2 MB, 1.1 GB)
func FormatSize(bytes int64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)

	switch {
	case bytes >= GB:
		return fmt.Sprintf("%.1f GB", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%.1f MB", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%.1f KB", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}
