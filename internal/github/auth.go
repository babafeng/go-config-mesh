package github

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/zalando/go-keyring"
	"golang.org/x/term"
)

const (
	KeyringService = "config-mesh"
	KeyringUser    = "github_token"
)

// AuthManager 管理 GitHub 认证凭据
type AuthManager struct{}

// NewAuthManager 创建认证管理器
func NewAuthManager() *AuthManager {
	return &AuthManager{}
}

// GetToken 尝试从环境/gh cli/Keychain 获取 Token，若均无则引导用户输入并存入 Keychain
func (am *AuthManager) GetToken(username string) (string, error) {
	// 1. 优先检查环境变量
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		return strings.TrimSpace(token), nil
	}
	if token := os.Getenv("GH_TOKEN"); token != "" {
		return strings.TrimSpace(token), nil
	}

	// 2. 尝试复用 GitHub CLI (gh)
	if token, err := am.getTokenFromGHCLI(); err == nil && token != "" {
		return token, nil
	}

	// 3. 尝试从 macOS Keychain 读取
	if token, err := keyring.Get(KeyringService, KeyringUser); err == nil && token != "" {
		return strings.TrimSpace(token), nil
	}

	// 4. 若未找到，引导用户在终端输入
	fmt.Printf("\n[!] 未检测到 GitHub 认证 (未找到 gh cli 登录凭据或本地 Keychain)。\n")
	fmt.Printf("请提供 GitHub Personal Access Token (需要具备 'repo' 权限以创建和管理私有仓库):\n")
	fmt.Printf("-> 创建链接: https://github.com/settings/tokens/new?scopes=repo&description=config-mesh\n\n")
	fmt.Print("请输入 GitHub Token（输入不会回显）: ")

	var inputToken string
	if term.IsTerminal(int(os.Stdin.Fd())) {
		secret, readErr := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if readErr != nil {
			return "", fmt.Errorf("读取 Token 失败: %w", readErr)
		}
		inputToken = strings.TrimSpace(string(secret))
	} else {
		scanner := bufio.NewScanner(os.Stdin)
		if !scanner.Scan() {
			return "", fmt.Errorf("读取输入失败")
		}
		inputToken = strings.TrimSpace(scanner.Text())
	}
	if inputToken != "" {
		// 存入 macOS Keychain
		if err := keyring.Set(KeyringService, KeyringUser, inputToken); err != nil {
			return "", fmt.Errorf("保存 Token 到系统钥匙串失败: %w", err)
		}
		fmt.Println("[ok] Token 已安全保存在 macOS Keychain 钥匙串中。")
		return inputToken, nil
	}

	return "", fmt.Errorf("token 不能为空")
}

// getTokenFromGHCLI 从系统 gh 命令行获取当前登录的 token
func (am *AuthManager) getTokenFromGHCLI() (string, error) {
	cmd := exec.Command("gh", "auth", "token")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ClearSavedToken 清除 Keychain 中保存的 Token
func (am *AuthManager) ClearSavedToken() error {
	return keyring.Delete(KeyringService, KeyringUser)
}
