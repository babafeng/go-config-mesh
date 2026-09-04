package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"config-mesh/internal/git"
	"config-mesh/internal/github"
	meshlock "config-mesh/internal/lock"
	"config-mesh/internal/model"
	"config-mesh/internal/scanner"
	"config-mesh/internal/state"
	"config-mesh/internal/tui"

	"github.com/spf13/cobra"
)

var pushYesFlag bool

var pushCmd = &cobra.Command{
	Use:   "push [github_username]",
	Short: "无人值守/增量备份：仅将本地变动配置加密上传至云端本机槽位 (不改写本地文件)",
	Long: `push 命令专门用于无人值守和日常增量备份：
1. 仅单向将本地变动的配置加密并推送到云端分配给本机的专属槽位；
2. 绝对不执行任何拉取、下载或本地文件覆盖操作，确保 100% 本地安全；
3. 支持 -y/--yes 非交互模式，自动继承上次已选配置；若无文件改动则自动跳过上传，适合放入 launchd/cron 定时任务。`,
	RunE: func(cmd *cobra.Command, args []string) error {
		operationLock, err := meshlock.Acquire()
		if err != nil {
			return err
		}
		defer operationLock.Release()

		var targetUsername string
		if len(args) > 0 {
			targetUsername = strings.TrimSpace(args[0])
		}

		fmt.Println(":: 启动 config-mesh 本地配置增量推送...")

		// 1. GitHub 认证检测
		authMgr := github.NewAuthManager()
		token, err := authMgr.GetToken(targetUsername)
		if err != nil {
			return fmt.Errorf("GitHub 认证失败: %w", err)
		}

		// 2. 初始化 GitHub 客户端并检测用户名与私有仓库
		ghClient := github.NewRepoClient(token)
		authUsername, err := ghClient.GetAuthenticatedUser()
		if err != nil {
			return fmt.Errorf("获取 GitHub 用户信息失败: %w", err)
		}
		if targetUsername == "" {
			targetUsername = authUsername
		}

		if !repoComponentPattern.MatchString(targetUsername) || !repoComponentPattern.MatchString(RepoName) {
			return fmt.Errorf("GitHub 目标账号或仓库名称格式不合法")
		}

		repo, err := ghClient.EnsurePrivateRepo(targetUsername, RepoName)
		if err != nil {
			return err
		}

		// 3. 克隆或拉取仓库
		repoDir := CustomDir
		if repoDir == "" {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			repoDir = filepath.Join(homeDir, ".config-mesh", "repos", targetUsername, RepoName)
		}

		repoURL := repo.GetCloneURL()
		if repoURL == "" {
			repoURL = fmt.Sprintf("https://github.com/%s/%s.git", targetUsername, RepoName)
		}

		gitMgr, err := git.NewRepositoryManager(repoDir, repoURL, token, authUsername)
		if err != nil {
			return err
		}
		gitMgr.DefaultBranch = repo.GetDefaultBranch()
		if err := gitMgr.CloneOrPull(); err != nil {
			return fmt.Errorf("拉取配置仓库失败: %w", err)
		}

		// 4. 扫描本地配置并分析差异
		vaultID := fmt.Sprintf("%s/%s", targetUsername, RepoName)
		s, err := scanner.NewScanner()
		if err != nil {
			return err
		}
		items, err := s.Scan()
		if err != nil {
			return fmt.Errorf("配置扫描失败: %w", err)
		}

		stateMgr, err := state.NewStateManagerForVault(vaultID)
		if err != nil {
			return err
		}
		localState, err := stateMgr.LoadState()
		if err != nil {
			return err
		}

		items = state.IncludeTrackedMissingItems(items, localState, s.HomeDir)
		items, err = state.AnalyzeDiff(items, nil, localState)
		if err != nil {
			return err
		}

		var selectedItems []model.ConfigItem
		if pushYesFlag {
			hasTrackedSelection := false
			for _, it := range items {
				if tr, ok := localState.TrackedItems[it.ID]; ok && tr.Selected {
					hasTrackedSelection = true
					break
				}
			}

			for _, it := range items {
				if hasTrackedSelection {
					// 用户此前在 TUI 中挑选过配置项，严格继承用户的选择集
					if tr, ok := localState.TrackedItems[it.ID]; ok && tr.Selected {
						it.Selected = true
					} else {
						it.Selected = false
					}
				} else {
					// 首次静默上传，使用推荐项
					if it.Recommended {
						it.Selected = true
					}
				}
				selectedItems = append(selectedItems, it)
			}
		} else {
			selectedItems, err = tui.RunCheckboxTUI(":: 选择需要加密并上传同步的本地配置项", items)
			if err != nil {
				return err
			}
		}

		var toUpload []model.ConfigItem
		for _, item := range selectedItems {
			if item.Selected && (item.Exists || item.Deleted) {
				toUpload = append(toUpload, item)
			}
		}

		if len(toUpload) == 0 {
			fmt.Println("[*] 未选择任何存在的配置项，上传已跳过。")
			return nil
		}

		return uploadConfigItems(repoDir, gitMgr, vaultID, toUpload, selectedItems, "backup: daily push", false)
	},
}

func init() {
	pushCmd.Flags().BoolVarP(&pushYesFlag, "yes", "y", false, "非交互模式：自动继承之前配置的选择项，跳过 TUI 确认")
}
