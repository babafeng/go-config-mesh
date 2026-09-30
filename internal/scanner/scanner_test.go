package scanner

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"config-mesh/internal/model"
	"golang.org/x/crypto/ssh"
)

func TestBlacklist(t *testing.T) {
	cases := []struct {
		path      string
		sensitive bool
	}{
		{"/etc/shadow", true},
		{"/etc/passwd", true},
		{"/Users/alice/master.key", true},
		{"/Users/alice/cert.p12", true},
		{"/Users/alice/.ssh/id_ed25519.pub", false},
		{"/Users/alice/.ssh/id_ed25519", true},
		{"/Users/alice/.ssh/work_rsa", true},
		{"/Users/alice/.ssh/config", false},
		{"/Users/alice/.zshrc", false},
		{"/Users/alice/.gitconfig", false},
		{"/Users/alice/.aws/config", false},
		{"/Users/alice/.aws/credentials", true},
		{"/Users/alice/.kube/config", true},
		{"/Users/alice/.codex/auth.json", true},
		{"/Users/alice/project/.env.production", true},
	}

	for _, c := range cases {
		got := IsSensitiveFile(c.path)
		if got != c.sensitive {
			t.Errorf("IsSensitiveFile(%s) = %v; want %v", c.path, got, c.sensitive)
		}
	}
}

func TestSensitiveConfigRoots(t *testing.T) {
	for _, tc := range []struct {
		path string
		dir  bool
	}{
		{path: ".ssh", dir: true},
		{path: ".aws", dir: true},
		{path: ".kube/config"},
		{path: "../outside"},
	} {
		if !IsSensitiveConfigPath(tc.path, tc.dir) {
			t.Errorf("IsSensitiveConfigPath(%q) 应拒绝", tc.path)
		}
	}
	if IsSensitiveConfigPath(".ssh/config", false) {
		t.Error("SSH config 不应被当作私钥")
	}
	if !IsSensitiveConfigPath(".ssh/work-prod", false) || IsSensitiveConfigPath(".ssh/work-prod.pub", false) {
		t.Error("自定义 SSH 私钥应敏感，配套公钥不应被阻止")
	}
	for _, tc := range []struct {
		path string
		kind model.SecretKind
	}{
		{path: ".aws/credentials", kind: model.SecretKindAWSCredentials},
		{path: ".aliyun/config.json", kind: model.SecretKindAliyunConfig},
		{path: ".ssh/work-prod", kind: model.SecretKindSSHPrivateKey},
		{path: ".zshrc", kind: model.SecretKindDetectedConfig},
	} {
		if !IsAllowedSecretConfigPath(tc.path, false, tc.kind) {
			t.Errorf("受控凭据路径应被允许: %s (%s)", tc.path, tc.kind)
		}
	}
	if IsAllowedSecretConfigPath(".ssh/authorized_keys", false, model.SecretKindSSHPrivateKey) ||
		IsAllowedSecretConfigPath(".config/evil", false, model.SecretKindSSHPrivateKey) ||
		IsAllowedSecretConfigPath(".unknown-config", false, model.SecretKindDetectedConfig) {
		t.Fatal("SSH 凭据类型不得绕过任意路径限制")
	}
}

func TestSensitiveContentDetection(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name      string
		content   string
		sensitive bool
	}{
		{name: "literal-token", content: `api_key = "sk-abcdefghijklmnopqrstuvwxyz"`, sensitive: true},
		{name: "private-key", content: "-----BEGIN OPENSSH PRIVATE KEY-----\nabc", sensitive: true},
		{name: "env-reference", content: `api_key = "${OPENAI_API_KEY}"`, sensitive: false},
		{name: "placeholder", content: `password: "changeme"`, sensitive: false},
		{name: "ordinary", content: `theme = "dark"`, sensitive: false},
	}
	for _, tc := range cases {
		path := filepath.Join(dir, tc.name)
		if err := os.WriteFile(path, []byte(tc.content), 0600); err != nil {
			t.Fatal(err)
		}
		got, err := FileContainsSensitiveContent(path)
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.sensitive {
			t.Errorf("FileContainsSensitiveContent(%s) = %v; want %v", tc.name, got, tc.sensitive)
		}
	}
}

func TestScannerMarksSensitivePresetAndRejectsRootSymlink(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte(`export API_KEY="literal-production-secret"`), 0600); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "vimrc")
	if err := os.WriteFile(external, []byte("set number\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(home, ".vimrc")); err != nil {
		t.Skipf("当前文件系统不支持软链接: %v", err)
	}
	items, err := (&Scanner{HomeDir: home}).Scan()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "shell_zshrc" ||
		items[0].SecretKind != model.SecretKindDetectedConfig || items[0].FileMode != 0600 {
		t.Fatalf("含凭据的已知配置应进入受控同步，根软链接仍须拒绝: %+v", items)
	}
}

func TestShouldIgnorePath(t *testing.T) {
	cases := []struct {
		relPath string
		ignore  bool
	}{
		{"antigravity-cli/brain/transcript.jsonl", true},
		{"antigravity-cli/logs/app.log", true},
		{"extensions/vscode.some-ext/dist/index.js", true},
		{"models/llama3.70b.gguf", true},
		{"sessions/session-1.jsonl", true},
		{"index/vectors.lance", true},
		{"transcripts/conversation.jsonl", true},
		{"vector_cache/embeddings.db", true},
		{"History/file_state.json", true},
		{"tmp/cache.dat", true},
		{"settings.json", false},
		{"config/skills/skill.md", false},
		{"config.json", false},
		{".DS_Store", true},
		{"agent.1234.sock", true},
	}

	for _, c := range cases {
		got := ShouldIgnorePath(c.relPath)
		if got != c.ignore {
			t.Errorf("ShouldIgnorePath(%s) = %v; want %v", c.relPath, got, c.ignore)
		}
	}
}

func TestScanner(t *testing.T) {
	tmpHome := t.TempDir()
	zshrc := filepath.Join(tmpHome, ".zshrc")
	content := "export TEST=1\n"
	_ = os.WriteFile(zshrc, []byte(content), 0644)

	// 模拟创建 .gemini 目录，其中包含核心配置文件和被忽略的 brain 缓存
	geminiDir := filepath.Join(tmpHome, ".gemini")
	_ = os.MkdirAll(filepath.Join(geminiDir, "brain"), 0755)
	_ = os.WriteFile(filepath.Join(geminiDir, "settings.json"), []byte(`{"theme":"dark"}`), 0644)
	_ = os.WriteFile(filepath.Join(geminiDir, "brain", "huge.log"), make([]byte, 1024*1024), 0644) // 1MB 缓存

	s := &Scanner{HomeDir: tmpHome}
	items, err := s.Scan()
	if err != nil {
		t.Fatalf("Scan() 失败: %v", err)
	}

	// 验证未创建的文件绝不会出现在 items 列表中，只有 .zshrc 和 .gemini/settings.json 出现
	if len(items) != 2 {
		t.Fatalf("扫描结果数量异常: 期望 2 项, 实际返回 %d 项", len(items))
	}

	for _, item := range items {
		if item.RelHomePath == ".gemini/settings.json" {
			if !item.Exists {
				t.Errorf(".gemini/settings.json 应该存在")
			}
		}
	}
}

func TestScannerAntigravityCLIConfigOnlyWhenPresent(t *testing.T) {
	home := t.TempDir()
	cliDir := filepath.Join(home, ".gemini", "antigravity-cli")
	if err := os.MkdirAll(cliDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cliDir, "settings.json"), []byte(`{"colorScheme":"dark"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cliDir, "mcp_config.json"), []byte(`{"mcpServers":{}}`), 0600); err != nil {
		t.Fatal(err)
	}

	check := func(wantKeybindings bool) {
		t.Helper()
		items, err := (&Scanner{HomeDir: home}).Scan()
		if err != nil {
			t.Fatal(err)
		}
		byID := make(map[string]model.ConfigItem)
		for _, item := range items {
			byID[item.ID] = item
		}
		settings, ok := byID["ai_antigravity_cli_settings"]
		if !ok || !settings.Selected || !settings.Recommended || settings.RelHomePath != ".gemini/antigravity-cli/settings.json" {
			t.Fatalf("CLI settings 预设不正确: %+v", settings)
		}
		mcp, ok := byID["ai_antigravity_cli_mcp"]
		if !ok || mcp.Selected || mcp.Recommended {
			t.Fatalf("CLI MCP 配置应存在但默认不勾选: %+v", mcp)
		}
		_, foundKeybindings := byID["ai_antigravity_cli_keybindings"]
		if foundKeybindings != wantKeybindings {
			t.Fatalf("CLI keybindings 存在状态错误: got %v, want %v", foundKeybindings, wantKeybindings)
		}
	}

	check(false)
	if err := os.WriteFile(filepath.Join(cliDir, "keybindings.json"), []byte(`{"submit":["enter"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	check(true)
}

func TestScannerIncludesExplicitCredentialItems(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".aws"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".aws", "credentials"), []byte("[default]\naws_access_key_id=AKIAIOSFODNN7EXAMPLE\naws_secret_access_key=literal-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".aliyun"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".aliyun", "config.json"), []byte(`{"profiles":[{"access_key_id":"id","access_key_secret":"secret"}]}`), 0600); err != nil {
		t.Fatal(err)
	}

	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	privateBlock, err := ssh.MarshalPrivateKey(privateKey, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "work-prod"), pem.EncodeToMemory(privateBlock), 0600); err != nil {
		t.Fatal(err)
	}
	sshPublic, err := ssh.NewPublicKey(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "work-prod.pub"), ssh.MarshalAuthorizedKey(sshPublic), 0644); err != nil {
		t.Fatal(err)
	}

	items, err := (&Scanner{HomeDir: home}).Scan()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]model.SecretKind{
		"cloud_aws_credentials": model.SecretKindAWSCredentials,
		"cloud_aliyun_config":   model.SecretKindAliyunConfig,
		"ssh_private_work-prod": model.SecretKindSSHPrivateKey,
	}
	seenPublic := false
	for _, item := range items {
		if kind, ok := want[item.ID]; ok {
			if item.SecretKind != kind || item.Selected || item.Recommended || item.FileMode != 0600 {
				t.Errorf("凭据项安全属性错误: %+v", item)
			}
			delete(want, item.ID)
		}
		if item.ID == "ssh_public_work-prod" {
			seenPublic = true
		}
	}
	if len(want) != 0 || !seenPublic {
		t.Fatalf("缺少凭据或 SSH 公钥扫描项: missing=%v public=%v items=%+v", want, seenPublic, items)
	}
}

func TestFormatSize(t *testing.T) {
	tests := []struct {
		bytes    int64
		expected string
	}{
		{500, "500 B"},
		{1024, "1.0 KB"},
		{2048, "2.0 KB"},
		{1048576, "1.0 MB"},
		{1073741824, "1.0 GB"},
	}

	for _, tt := range tests {
		got := FormatSize(tt.bytes)
		if got != tt.expected {
			t.Errorf("FormatSize(%d) = %s; want %s", tt.bytes, got, tt.expected)
		}
	}
}

func TestScanClaudeSettingsFiles(t *testing.T) {
	home := t.TempDir()
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0700); err != nil {
		t.Fatal(err)
	}

	testFiles := map[string]string{
		"settings.json":          `{"model":"default"}`,
		"settings.json.xiaomi":   `{"model":"xiaomi"}`,
		"settings.json.opencode": `{"model":"opencode"}`,
		"settings.json.bai":      `{"model":"bai"}`,
		"settings.json.gmi":      `{"model":"gmi"}`,
		"settings.json.chatgpt":  `{"model":"chatgpt"}`,
		"settings.local.json":    `{"local":true}`,
		"CLAUDE.md":              `# Guidelines`,
		"unrelated.log":          `some logs`,
	}

	for filename, content := range testFiles {
		if err := os.WriteFile(filepath.Join(claudeDir, filename), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	s := &Scanner{HomeDir: home}
	items, err := s.Scan()
	if err != nil {
		t.Fatalf("Scan 失败: %v", err)
	}

	foundMap := make(map[string]model.ConfigItem)
	for _, item := range items {
		if item.Category == model.CategoryAI {
			foundMap[item.RelHomePath] = item
		}
	}

	expectedPaths := []string{
		".claude/settings.json",
		".claude/settings.json.xiaomi",
		".claude/settings.json.opencode",
		".claude/settings.json.bai",
		".claude/settings.json.gmi",
		".claude/settings.json.chatgpt",
		".claude/settings.local.json",
		".claude/CLAUDE.md",
	}

	for _, expected := range expectedPaths {
		item, exists := foundMap[expected]
		if !exists {
			t.Errorf("未扫描到期望的 Claude 配置文件: %s", expected)
			continue
		}
		if !item.Recommended || !item.Selected || !item.Exists {
			t.Errorf("配置项状态不符合预期: %+v", item)
		}
		if item.VaultFile == "" || item.ID == "" {
			t.Errorf("配置项 ID 或 VaultFile 为空: %+v", item)
		}
		// 校验白名单判定
		if !IsAllowedSecretConfigPath(item.RelHomePath, false, model.SecretKindDetectedConfig) {
			t.Errorf("IsAllowedSecretConfigPath 应允许 Claude settings 路径: %s", item.RelHomePath)
		}
	}

	if _, exists := foundMap[".claude/unrelated.log"]; exists {
		t.Errorf("不应扫描非 settings/预设文件: .claude/unrelated.log")
	}
}
