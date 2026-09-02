package main

import (
	"fmt"

	meshlock "config-mesh/internal/lock"
	"config-mesh/internal/sync"
	"github.com/spf13/cobra"
)

var rollbackCmd = &cobra.Command{
	Use:   "rollback [backup_id]",
	Short: "根据历史快照回滚还原配置 (默认回滚最近一次)",
	RunE: func(cmd *cobra.Command, args []string) error {
		operationLock, err := meshlock.Acquire()
		if err != nil {
			return err
		}
		defer operationLock.Release()

		bm, err := sync.NewBackupManager()
		if err != nil {
			return err
		}

		backups, err := bm.ListBackups()
		if err != nil {
			return fmt.Errorf("查询备份失败: %w", err)
		}

		if len(backups) == 0 {
			fmt.Println("ℹ️ 未检测到任何本地历史备份快照。")
			return nil
		}

		backupID := "latest"
		if len(args) > 0 && args[0] != "" {
			backupID = args[0]
		}

		fmt.Printf("🔄 正在从快照 [%s] 执行回滚还原...\n", backupID)
		if err := bm.Rollback(backupID); err != nil {
			return fmt.Errorf("回滚失败: %w", err)
		}

		fmt.Println("🎉 配置已成功回滚还原至备份状态！")
		return nil
	},
}
