package main

import (
	"fmt"

	"config-mesh/internal/model"
	"config-mesh/internal/scanner"
	"config-mesh/internal/state"
	"config-mesh/internal/tui"
	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "查看本地配置相对于上次同步基线的差异",
	RunE: func(cmd *cobra.Command, args []string) error {
		stateMgr, err := state.NewStateManager()
		if err != nil {
			return err
		}

		localState, err := stateMgr.LoadState()
		if err != nil {
			return fmt.Errorf("加载本地状态失败: %w", err)
		}

		s, err := scanner.NewScanner()
		if err != nil {
			return err
		}

		items, err := s.Scan()
		if err != nil {
			return fmt.Errorf("扫描失败: %w", err)
		}
		items = state.IncludeTrackedMissingItems(items, localState, s.HomeDir)

		analyzed, err := state.AnalyzeDiff(items, nil, localState)
		if err != nil {
			return err
		}

		fmt.Println("📊 本地配置相对于上次同步基线的差异概览：")
		if localState.VaultID != "" {
			fmt.Printf("🗄️ 当前基线 vault: %s\n", localState.VaultID)
		}
		fmt.Printf("📋 状态文件: %s\n", stateMgr.StateFilePath)
		if localState.UploadSnapshotID != "" {
			fmt.Printf("💻 本机稳定上传目录: hosts/%s\n", localState.UploadSnapshotID)
		}
		if !localState.LastUploadedAt.IsZero() {
			fmt.Printf("📤 上次上传确认: %s\n", localState.LastUploadedAt.Format("2006-01-02 15:04:05"))
		}
		if !localState.LastDownloadedAt.IsZero() {
			fmt.Printf("📥 上次下载应用: %s\n", localState.LastDownloadedAt.Format("2006-01-02 15:04:05"))
		}
		if !localState.LastSyncedAt.IsZero() {
			fmt.Printf("⏱️ 上次同步时间: %s (快照: %s)\n\n",
				localState.LastSyncedAt.Format("2006-01-02 15:04:05"),
				localState.RemoteSnapshotID,
			)
		} else {
			fmt.Printf("⏱️ 上次同步时间: %s\n\n", tui.DimStyle.Render("尚未进行过全量同步"))
		}

		var currentCategory model.ConfigCategory = ""

		for _, item := range analyzed {
			if item.Category != currentCategory {
				currentCategory = item.Category
				fmt.Printf("\n%s\n", tui.CategoryBadgeStyle.Render(string(currentCategory)))
			}

			statusStr := ""
			switch item.DiffStatus {
			case model.DiffStatusSynced:
				statusStr = tui.StatusSyncedStyle.Render("[已同步]")
			case model.DiffStatusLocalModified:
				statusStr = tui.StatusLocalModifiedStyle.Render("[本地修改]")
			case model.DiffStatusRemoteModified:
				statusStr = tui.StatusRemoteModifiedStyle.Render("[云端更新]")
			case model.DiffStatusConflict:
				statusStr = tui.StatusConflictStyle.Render("[有冲突]")
			case model.DiffStatusNew:
				statusStr = tui.StatusNewStyle.Render("[未同步]")
			}

			hashSnippet := ""
			if len(item.ContentHash) >= 8 {
				hashSnippet = tui.DimStyle.Render("sha:" + item.ContentHash[:8])
			}

			sizeStr := tui.DimStyle.Render(fmt.Sprintf("(%s)", scanner.FormatSize(item.Size)))

			fmt.Printf("  %s %-50s %-12s %s\n", statusStr, item.Name, sizeStr, hashSnippet)
		}

		fmt.Println("\n💡 此命令不访问网络；云端更新会在 `config-mesh apply` 成功拉取 Manifest 后计算。")
		return nil
	},
}
