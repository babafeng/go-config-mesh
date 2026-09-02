package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"config-mesh/internal/crypto"
	gitengine "config-mesh/internal/git"
	ghapi "config-mesh/internal/github"
	meshlock "config-mesh/internal/lock"
	"github.com/spf13/cobra"
)

var keyCmd = &cobra.Command{
	Use:   "key",
	Short: "管理 vault 的多设备加密接收者",
}

var keyAddCmd = &cobra.Command{
	Use:   "add <public-key-or-path> [github_username]",
	Short: "在已授权设备上添加新设备 SSH 公钥",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		operationLock, err := meshlock.Acquire()
		if err != nil {
			return err
		}
		defer operationLock.Release()

		owner := ""
		if len(args) == 2 {
			owner = strings.TrimSpace(args[1])
		}
		authMgr := ghapi.NewAuthManager()
		token, err := authMgr.GetToken(owner)
		if err != nil {
			return err
		}
		ghClient := ghapi.NewRepoClient(token)
		authUser, err := ghClient.GetAuthenticatedUser()
		if err != nil {
			return err
		}
		if owner == "" {
			owner = authUser
		}
		if !repoComponentPattern.MatchString(owner) || !repoComponentPattern.MatchString(RepoName) {
			return fmt.Errorf("GitHub owner 或仓库名包含非法字符")
		}
		repo, err := ghClient.EnsurePrivateRepo(owner, RepoName)
		if err != nil {
			return err
		}
		repoDir := CustomDir
		if repoDir == "" {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			repoDir = filepath.Join(homeDir, ".config-mesh", "repos", owner, RepoName)
		}
		repoURL := repo.GetCloneURL()
		if repoURL == "" {
			repoURL = fmt.Sprintf("https://github.com/%s/%s.git", owner, RepoName)
		}
		manager, err := gitengine.NewRepositoryManager(repoDir, repoURL, token, authUser)
		if err != nil {
			return err
		}
		manager.DefaultBranch = repo.GetDefaultBranch()
		if err := manager.CloneOrPull(); err != nil {
			return err
		}
		fingerprint, added, err := crypto.AddRecipient(filepath.Join(repoDir, "recipients.pub"), args[0])
		if err != nil {
			return err
		}
		if !added {
			fmt.Printf("ℹ️ 该公钥已在 recipients.pub 中: %s\n", fingerprint)
		}
		if err := manager.CommitAndPush("keys: add recipient "+fingerprint, "recipients.pub"); err != nil {
			return err
		}
		if !added {
			fmt.Println("✅ 已确认本地接收者变更全部推送至远端。")
			return nil
		}
		fmt.Printf("✅ 已添加新设备接收者: %s\n", fingerprint)
		fmt.Println("下一次上传的快照将同时加密给该设备；历史快照不会自动重新加密。")
		return nil
	},
}

func init() {
	keyCmd.AddCommand(keyAddCmd)
}
