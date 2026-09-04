package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const (
	launchAgentLabel = "com.babafeng.config-mesh"
	launchAgentPlist = "com.babafeng.config-mesh.plist"
)

var (
	scheduleTime     string
	scheduleInterval string
	scheduleBinPath  string
)

var scheduleCmd = &cobra.Command{
	Use:   "schedule",
	Short: "配置与管理 macOS 每日无人值守自动备份服务 (LaunchAgent)",
	Long: `schedule 命令用于配置 macOS 原生系统级定时任务 (LaunchAgent)，
实现每日自动静默调用 config-mesh push -y，将本地配置增量备份到云端。

常用子命令:
  install   安装并启动每日自动备份服务 (默认每天凌晨 03:00)
  status    查看当前定时服务运行状态与最近备份日志
  uninstall 停止并卸载定时备份服务
  run       立即手动触发一次后台备份测试`,
}

var scheduleInstallCmd = &cobra.Command{
	Use:     "install",
	Aliases: []string{"enable"},
	Short:   "安装并启用 macOS 原生每日自动备份定时服务",
	RunE: func(cmd *cobra.Command, args []string) error {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return err
		}

		// 1. 确定可执行文件的安全持久路径
		binPath := scheduleBinPath
		if binPath == "" {
			defaultLocalBin := filepath.Join(homeDir, ".local", "bin", "config-mesh")
			if _, err := os.Stat(defaultLocalBin); err == nil {
				binPath = defaultLocalBin
			} else {
				exePath, err := os.Executable()
				if err != nil {
					return fmt.Errorf("无法获取当前二进制路径: %w", err)
				}
				exePath, _ = filepath.EvalSymlinks(exePath)
				// 如果当前程序不在系统标准路径下，自动安装一份副本到 ~/.local/bin/config-mesh
				if !strings.HasPrefix(exePath, "/usr/local/bin") && !strings.HasPrefix(exePath, filepath.Join(homeDir, ".local", "bin")) {
					if err := os.MkdirAll(filepath.Dir(defaultLocalBin), 0755); err != nil {
						return fmt.Errorf("创建 ~/.local/bin 目录失败: %w", err)
					}
					if err := copyBinary(exePath, defaultLocalBin); err != nil {
						return fmt.Errorf("安装二进制文件至 ~/.local/bin 失败: %w", err)
					}
					binPath = defaultLocalBin
					fmt.Printf("[+] 已自动将 config-mesh 复制安装至: %s\n", binPath)
				} else {
					binPath = exePath
				}
			}
		}

		if err := os.Chmod(binPath, 0755); err != nil {
			return fmt.Errorf("设置二进制执行权限失败: %w", err)
		}

		// 2. 准备日志目录
		logDir := filepath.Join(homeDir, ".config-mesh", "logs")
		if err := os.MkdirAll(logDir, 0755); err != nil {
			return fmt.Errorf("创建日志目录失败: %w", err)
		}
		stdoutLog := filepath.Join(logDir, "sync-daily.log")
		stderrLog := filepath.Join(logDir, "sync-daily.err")

		// 3. 生成 plist 内容
		plistContent, triggerDesc, err := generateLaunchAgentPlist(binPath, homeDir, stdoutLog, stderrLog, scheduleTime, scheduleInterval)
		if err != nil {
			return err
		}

		// 4. 写入 ~/Library/LaunchAgents/
		agentsDir := filepath.Join(homeDir, "Library", "LaunchAgents")
		if err := os.MkdirAll(agentsDir, 0755); err != nil {
			return fmt.Errorf("创建 LaunchAgents 目录失败: %w", err)
		}
		plistPath := filepath.Join(agentsDir, launchAgentPlist)

		// 停止已存在的服务
		_ = exec.Command("launchctl", "unload", plistPath).Run()

		if err := os.WriteFile(plistPath, []byte(plistContent), 0644); err != nil {
			return fmt.Errorf("写入 LaunchAgent plist 失败: %w", err)
		}

		// 5. 注册并启动 LaunchAgent
		loadCmd := exec.Command("launchctl", "load", plistPath)
		if out, err := loadCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("注册 launchctl 失败: %v, 输出: %s", err, string(out))
		}

		fmt.Println("\n[ok] 自动化定时备份服务已成功安装并启动！")
		fmt.Printf("  [•] 服务标签:   %s\n", launchAgentLabel)
		fmt.Printf("  [•] 执行周期:   %s\n", triggerDesc)
		fmt.Printf("  [•] 执行程序:   %s push -y\n", binPath)
		fmt.Printf("  [•] 标准输出日志: %s\n", stdoutLog)
		fmt.Printf("  [•] 错误输出日志: %s\n", stderrLog)
		fmt.Println("\n[*] 提示: 您可随时运行 `config-mesh schedule status` 检查运行状态，或运行 `config-mesh schedule run` 立即测试。")

		return nil
	},
}

var scheduleStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "查看当前定时备份服务的注册状态与最近执行日志",
	RunE: func(cmd *cobra.Command, args []string) error {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		plistPath := filepath.Join(homeDir, "Library", "LaunchAgents", launchAgentPlist)
		stdoutLog := filepath.Join(homeDir, ".config-mesh", "logs", "sync-daily.log")
		stderrLog := filepath.Join(homeDir, ".config-mesh", "logs", "sync-daily.err")

		fmt.Println(":: 检查定时备份服务状态:")
		if _, err := os.Stat(plistPath); os.IsNotExist(err) {
			fmt.Println("   [状态] 尚未安装定时任务 (运行 `config-mesh schedule install` 安装)")
			return nil
		}

		listCmd := exec.Command("launchctl", "list", launchAgentLabel)
		out, err := listCmd.CombinedOutput()
		if err != nil {
			fmt.Println("   [状态] 已生成配置文件，但尚未在 launchctl 中激活")
		} else {
			fmt.Println("   [状态] 服务已在 launchd 中激活运行")
			outStr := strings.TrimSpace(string(out))
			if outStr != "" {
				for _, line := range strings.Split(outStr, "\n") {
					line = strings.TrimSpace(line)
					if strings.HasPrefix(line, "\"PID\"") || strings.HasPrefix(line, "\"LastExitStatus\"") {
						fmt.Printf("   %s\n", line)
					}
				}
			}
		}

		fmt.Printf("   [配置] %s\n", plistPath)
		fmt.Printf("   [日志] %s\n", stdoutLog)

		if info, err := os.Stat(stdoutLog); err == nil && info.Size() > 0 {
			fmt.Println("\n:: 最近备份日志 (末尾 10 行):")
			lines := tailLines(stdoutLog, 10)
			for _, l := range lines {
				fmt.Printf("   %s\n", l)
			}
		} else {
			fmt.Println("\n[*] 暂无执行日志记录 (服务将在下一个定时触发点自动执行)。")
		}

		if info, err := os.Stat(stderrLog); err == nil && info.Size() > 0 {
			fmt.Println("\n:: 最近异常日志:")
			lines := tailLines(stderrLog, 5)
			for _, l := range lines {
				fmt.Printf("   [!] %s\n", l)
			}
		}

		return nil
	},
}

var scheduleUninstallCmd = &cobra.Command{
	Use:     "uninstall",
	Aliases: []string{"disable"},
	Short:   "停止并卸载 macOS 定时备份服务",
	RunE: func(cmd *cobra.Command, args []string) error {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		plistPath := filepath.Join(homeDir, "Library", "LaunchAgents", launchAgentPlist)

		if _, err := os.Stat(plistPath); os.IsNotExist(err) {
			fmt.Println("[*] 未检测到已安装的 LaunchAgent 服务。")
			return nil
		}

		_ = exec.Command("launchctl", "unload", plistPath).Run()
		_ = os.Remove(plistPath)

		fmt.Println("[ok] 自动备份服务已成功停止并卸载。")
		return nil
	},
}

var scheduleRunCmd = &cobra.Command{
	Use:   "run",
	Short: "立即手动触发一次后台备份任务",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println(":: 正在执行单次静默备份任务...")
		pushYesFlag = true
		return pushCmd.RunE(pushCmd, nil)
	},
}

func init() {
	scheduleInstallCmd.Flags().StringVar(&scheduleTime, "time", "03:00", "每天执行的具体时间 (格式 HH:MM，如 03:00 或 12:30)")
	scheduleInstallCmd.Flags().StringVar(&scheduleInterval, "interval", "", "执行时间间隔 (如 12h, 6h，设置此项将覆盖 --time)")
	scheduleInstallCmd.Flags().StringVar(&scheduleBinPath, "bin-path", "", "自定义 config-mesh 绝对执行路径")

	scheduleCmd.AddCommand(scheduleInstallCmd)
	scheduleCmd.AddCommand(scheduleStatusCmd)
	scheduleCmd.AddCommand(scheduleUninstallCmd)
	scheduleCmd.AddCommand(scheduleRunCmd)
}

func copyBinary(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

func generateLaunchAgentPlist(binPath, homeDir, stdoutLog, stderrLog, timeStr, intervalStr string) (string, string, error) {
	var triggerXml string
	var desc string

	if intervalStr != "" {
		dur, err := time.ParseDuration(intervalStr)
		if err != nil || dur <= 0 {
			return "", "", fmt.Errorf("无效的间隔时间格式 %q (应形如 12h, 30m, 3600s)", intervalStr)
		}
		seconds := int(dur.Seconds())
		triggerXml = fmt.Sprintf("    <key>StartInterval</key>\n    <integer>%d</integer>", seconds)
		desc = fmt.Sprintf("每隔 %s 执行一次", intervalStr)
	} else {
		parts := strings.Split(timeStr, ":")
		if len(parts) != 2 {
			return "", "", fmt.Errorf("时间格式不正确: %q (必须为 HH:MM，如 03:00)", timeStr)
		}
		hour, err1 := strconv.Atoi(parts[0])
		minute, err2 := strconv.Atoi(parts[1])
		if err1 != nil || err2 != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
			return "", "", fmt.Errorf("时间数值无效: %q (小时 0-23, 分钟 0-59)", timeStr)
		}
		triggerXml = fmt.Sprintf(`    <key>StartCalendarInterval</key>
    <dict>
        <key>Hour</key>
        <integer>%d</integer>
        <key>Minute</key>
        <integer>%d</integer>
    </dict>`, hour, minute)
		desc = fmt.Sprintf("每天 %02d:%02d 自动执行", hour, minute)
	}

	envPath := fmt.Sprintf("/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin:%s/.local/bin:%s/go/bin", homeDir, homeDir)

	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%s</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>push</string>
        <string>-y</string>
    </array>
%s
    <key>EnvironmentVariables</key>
    <dict>
        <key>HOME</key>
        <string>%s</string>
        <key>PATH</key>
        <string>%s</string>
    </dict>
    <key>StandardOutPath</key>
    <string>%s</string>
    <key>StandardErrorPath</key>
    <string>%s</string>
</dict>
</plist>
`, launchAgentLabel, binPath, triggerXml, homeDir, envPath, stdoutLog, stderrLog)

	return plist, desc, nil
}

func tailLines(filePath string, n int) []string {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	start := len(lines) - n
	if start < 0 {
		start = 0
	}
	var res []string
	for _, l := range lines[start:] {
		res = append(res, string(l))
	}
	return res
}
