package main

import (
	"fmt"

	"config-mesh/internal/scanner"
	"config-mesh/internal/state"
	"config-mesh/internal/tui"
	"github.com/spf13/cobra"
)

var scanCmd = &cobra.Command{
	Use:   "scan",
	Short: "扫描本地 macOS 常见配置文件并以 TUI 列表展示",
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := scanner.NewScanner()
		if err != nil {
			return err
		}

		items, err := s.Scan()
		if err != nil {
			return fmt.Errorf("扫描失败: %w", err)
		}

		stateMgr, err := state.NewStateManager()
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

		fmt.Println("🔍 正在扫描 macOS 预设配置项...")
		selected, err := tui.RunCheckboxTUI("🔍 本地配置项扫描结果 (可预览勾选)", items)
		if err != nil {
			return err
		}

		selectedCount := 0
		var totalSize int64
		for _, item := range selected {
			if item.Selected {
				selectedCount++
				if item.Exists {
					totalSize += item.Size
				}
			}
		}

		fmt.Printf("\n✅ 预览完成，当前共勾选 %d 项配置 (总计大小: %s)。\n", selectedCount, scanner.FormatSize(totalSize))
		return nil
	},
}
