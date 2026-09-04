package git

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// RepositoryManager 负责本地与远程 Git 仓库的交互
type RepositoryManager struct {
	RepoDir  string
	RepoURL  string
	AuthUser string
	Token    string
	// DefaultBranch 由 GitHub 仓库元数据提供，默认为 main。
	DefaultBranch string
}

var (
	snapshotPathPattern      = regexp.MustCompile(`^hosts/[A-Za-z0-9._-]{1,200}$`)
	authorizationHeaderRegex = regexp.MustCompile(`(?i)(Authorization:\s*Basic\s+)[A-Za-z0-9+/=]+`)
)

// NewRepositoryManager 创建 Git 仓库管理器
func NewRepositoryManager(repoDir, repoURL, token, authUser string) (*RepositoryManager, error) {
	if repoDir == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("无法获取家目录: %w", err)
		}
		repoDir = filepath.Join(homeDir, ".config-mesh", "repo")
	}

	if err := os.MkdirAll(repoDir, 0700); err != nil {
		return nil, fmt.Errorf("创建仓库目录失败: %w", err)
	}

	return &RepositoryManager{
		RepoDir:       repoDir,
		RepoURL:       repoURL,
		Token:         token,
		AuthUser:      authUser,
		DefaultBranch: "main",
	}, nil
}

// CloneOrPull 克隆或拉取最新的远端仓库
func (rm *RepositoryManager) CloneOrPull() error {
	gitDir := filepath.Join(rm.RepoDir, ".git")

	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		cmd := exec.Command("git", "clone", rm.RepoURL, rm.RepoDir)
		cmd.Env = rm.gitEnv()
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("git clone 失败: %s (%w)", redactAuth(string(output), rm.Token), err)
		}
		return nil
	}
	if err := rm.validateRemote(); err != nil {
		return err
	}

	// 远端 URL 中只保存无凭据的地址，同时清理旧版可能留下的 Token。
	if out, err := exec.Command("git", "-C", rm.RepoDir, "remote", "set-url", "origin", rm.RepoURL).CombinedOutput(); err != nil {
		return fmt.Errorf("更新 Git 远端地址失败: %s (%w)", string(out), err)
	}

	// 1. 检查并清理可能遗留的未决 rebase 状态
	rebaseMerge := filepath.Join(rm.RepoDir, ".git", "rebase-merge")
	rebaseApply := filepath.Join(rm.RepoDir, ".git", "rebase-apply")
	if _, err := os.Stat(rebaseMerge); err == nil {
		abortCmd := exec.Command("git", "-C", rm.RepoDir, "rebase", "--abort")
		abortCmd.Env = rm.gitEnv()
		_ = abortCmd.Run()
	} else if _, err := os.Stat(rebaseApply); err == nil {
		abortCmd := exec.Command("git", "-C", rm.RepoDir, "rebase", "--abort")
		abortCmd.Env = rm.gitEnv()
		_ = abortCmd.Run()
	}

	branch := rm.branch()
	pullCmd := exec.Command("git", "-C", rm.RepoDir, "pull", "--rebase", "origin", branch)
	pullCmd.Env = rm.gitEnv()
	if out, err := pullCmd.CombinedOutput(); err != nil {
		// pull --rebase 失败（如遇到代码/密钥冲突）时立即执行 rebase --abort 清理现场，防止工作区卡死
		abortCmd := exec.Command("git", "-C", rm.RepoDir, "rebase", "--abort")
		abortCmd.Env = rm.gitEnv()
		_ = abortCmd.Run()
		return fmt.Errorf("git pull --rebase 失败: %s (%w)", redactAuth(string(out), rm.Token), err)
	}
	return nil
}

// CommitAndPush 只提交调用方明确列出的 config-mesh 管理路径并推送。
func (rm *RepositoryManager) CommitAndPush(commitMsg string, managedPaths ...string) error {
	if err := rm.validateRemote(); err != nil {
		return err
	}
	if out, err := exec.Command("git", "-C", rm.RepoDir, "remote", "set-url", "origin", rm.RepoURL).CombinedOutput(); err != nil {
		return fmt.Errorf("更新 Git 远端地址失败: %s (%w)", string(out), err)
	}

	if len(managedPaths) == 0 {
		return fmt.Errorf("未指定要提交的托管路径")
	}
	for _, managedPath := range managedPaths {
		if managedPath != "recipients.pub" && !snapshotPathPattern.MatchString(filepath.ToSlash(managedPath)) {
			return fmt.Errorf("拒绝提交非托管路径: %s", managedPath)
		}
	}
	addArgs := append([]string{"-C", rm.RepoDir, "add", "-A", "--"}, managedPaths...)
	addCmd := exec.Command("git", addArgs...)
	if out, err := addCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add 失败: %s (%w)", string(out), err)
	}

	// 检查是否有变动
	statusArgs := append([]string{"-C", rm.RepoDir, "status", "--porcelain", "--"}, managedPaths...)
	statusCmd := exec.Command("git", statusArgs...)
	statusOut, err := statusCmd.Output()
	if err != nil {
		return fmt.Errorf("读取 Git 状态失败: %w", err)
	}
	if len(strings.TrimSpace(string(statusOut))) > 0 {
		// 只提交显式 pathspec；即使调用前已有无关文件被暂存，也不会混入提交。
		commitArgs := []string{"-C", rm.RepoDir,
			"-c", "user.name=config-mesh", "-c", "user.email=config-mesh@localhost",
			"commit", "-m", commitMsg, "--",
		}
		commitArgs = append(commitArgs, managedPaths...)
		commitCmd := exec.Command("git", commitArgs...)
		if out, err := commitCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git commit 失败: %s (%w)", string(out), err)
		}
	}

	// 确保主分支是 main
	branch := rm.branch()
	if out, err := exec.Command("git", "-C", rm.RepoDir, "branch", "-M", branch).CombinedOutput(); err != nil {
		return fmt.Errorf("重命名 Git 主分支失败: %s (%w)", string(out), err)
	}

	// 即使本次没有新提交也执行 push，以重试上一次“本地提交成功、远端推送失败”的状态。
	pushCmd := exec.Command("git", "-C", rm.RepoDir, "push", "-u", "origin", branch)
	pushCmd.Env = rm.gitEnv()
	if out, err := pushCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git push 失败: %s (%w)", redactAuth(string(out), rm.Token), err)
	}

	return nil
}

// gitEnv 通过子进程环境中的临时 Git 配置传递 Authorization Header。
// Token 不会出现在命令行或 .git/config 中。
func (rm *RepositoryManager) gitEnv() []string {
	env := make([]string, 0, len(os.Environ())+4)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "GIT_CONFIG_COUNT=") ||
			strings.HasPrefix(entry, "GIT_CONFIG_KEY_") ||
			strings.HasPrefix(entry, "GIT_CONFIG_VALUE_") ||
			strings.HasPrefix(entry, "GIT_TERMINAL_PROMPT=") ||
			strings.HasPrefix(entry, "GIT_TRACE") ||
			strings.HasPrefix(entry, "GIT_CURL_VERBOSE=") {
			continue
		}
		env = append(env, entry)
	}
	env = append(env, "GIT_TERMINAL_PROMPT=0")
	parsedURL, err := url.Parse(rm.RepoURL)
	if rm.Token == "" || err != nil || parsedURL.Scheme != "https" || !strings.EqualFold(parsedURL.Hostname(), "github.com") {
		return env
	}
	user := rm.AuthUser
	if user == "" {
		user = "oauth2"
	}
	auth := base64.StdEncoding.EncodeToString([]byte(user + ":" + rm.Token))
	return append(env,
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.extraHeader",
		"GIT_CONFIG_VALUE_0=Authorization: Basic "+auth,
	)
}

func (rm *RepositoryManager) validateRemote() error {
	out, err := exec.Command("git", "-C", rm.RepoDir, "remote", "get-url", "origin").CombinedOutput()
	if err != nil {
		return fmt.Errorf("读取 Git 远端失败: %s (%w)", string(out), err)
	}
	actual := normalizeRemote(strings.TrimSpace(string(out)))
	expected := normalizeRemote(rm.RepoURL)
	if actual != expected {
		return fmt.Errorf("本地仓库属于其他远端：%s（期望 %s），请使用独立 --repo-dir", actual, expected)
	}
	return nil
}

func normalizeRemote(raw string) string {
	if parsed, err := url.Parse(raw); err == nil && parsed.Scheme != "" {
		parsed.User = nil
		return strings.TrimSuffix(parsed.String(), "/")
	}
	return strings.TrimSuffix(raw, "/")
}

func redactAuth(message, token string) string {
	message = authorizationHeaderRegex.ReplaceAllString(message, "${1}[REDACTED]")
	if token == "" {
		return message
	}
	return strings.ReplaceAll(message, token, "[REDACTED]")
}

func (rm *RepositoryManager) branch() string {
	if rm.DefaultBranch == "" {
		return "main"
	}
	return rm.DefaultBranch
}
