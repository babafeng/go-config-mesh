package main

import (
	"fmt"
	"os"
	"runtime"

	"github.com/spf13/cobra"
)

var (
	Version       = "v1.0.0"
	CustomDir     string
	RepoName      string
	applyUsername string
)

var rootCmd = &cobra.Command{
	Use:   "config-mesh",
	Short: "config-mesh - 专为 macOS 打造的端到端加密配置同步工具",
	Long: `config-mesh 是一个安全、轻量的 macOS 开发环境配置同步工具。
通过 GitHub 私有仓库托管，使用本地 SSH 密钥 (Age 算法) 进行端到端加密，
配备美观的 TUI 终端复选框交互，支持单行标记块追加、JSON 深度合并与自动快照备份。`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// 兼容支持 config-mesh --apply <username>
		if cmd.Flags().Changed("apply") {
			var applyArgs []string
			if applyUsername != "" {
				applyArgs = append(applyArgs, applyUsername)
			}
			return applyCmd.RunE(applyCmd, applyArgs)
		}

		// 若未指定参数，打印帮助信息
		return cmd.Help()
	},
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&CustomDir, "repo-dir", "d", "", "自定义本地 Git 仓库克隆存储路径")
	rootCmd.PersistentFlags().StringVarP(&RepoName, "repo-name", "r", "config-mesh-vault", "GitHub 私有仓库名称 (默认: config-mesh-vault)")
	rootCmd.Flags().StringVar(&applyUsername, "apply", "", "一键同步指定 GitHub 账号的配置 (等价于 config-mesh apply <username>)")

	// 注册子命令
	rootCmd.AddCommand(applyCmd)
	rootCmd.AddCommand(scanCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(rollbackCmd)
	rootCmd.AddCommand(keyCmd)
	rootCmd.AddCommand(versionCmd)
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "查看 config-mesh 版本信息",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("config-mesh %s (%s/%s)\n", Version, runtime.GOOS, runtime.GOARCH)
		fmt.Println("GitHub: https://github.com/babafeng/go-config-mesh")
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
