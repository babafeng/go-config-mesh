package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateLaunchAgentPlistDailyTime(t *testing.T) {
	plist, desc, err := generateLaunchAgentPlist(
		"/Users/testuser/.local/bin/config-mesh",
		"/Users/testuser",
		"/Users/testuser/.config-mesh/logs/sync-daily.log",
		"/Users/testuser/.config-mesh/logs/sync-daily.err",
		"04:15",
		"",
	)
	if err != nil {
		t.Fatalf("生成 plist 失败: %v", err)
	}

	if !strings.Contains(desc, "04:15") {
		t.Errorf("描述未包含时间: %s", desc)
	}
	if !strings.Contains(plist, "<key>Hour</key>\n        <integer>4</integer>") {
		t.Errorf("plist 未包含正确小时: %s", plist)
	}
	if !strings.Contains(plist, "<key>Minute</key>\n        <integer>15</integer>") {
		t.Errorf("plist 未包含正确分钟: %s", plist)
	}
	if !strings.Contains(plist, "<string>push</string>\n        <string>-y</string>") {
		t.Errorf("plist 未包含 push -y 命令参数: %s", plist)
	}
}

func TestGenerateLaunchAgentPlistInterval(t *testing.T) {
	plist, desc, err := generateLaunchAgentPlist(
		"/Users/testuser/.local/bin/config-mesh",
		"/Users/testuser",
		"/Users/testuser/.config-mesh/logs/sync-daily.log",
		"/Users/testuser/.config-mesh/logs/sync-daily.err",
		"",
		"12h",
	)
	if err != nil {
		t.Fatalf("生成 plist 失败: %v", err)
	}

	if !strings.Contains(desc, "12h") {
		t.Errorf("描述未包含间隔: %s", desc)
	}
	// 12h = 43200 seconds
	if !strings.Contains(plist, "<key>StartInterval</key>\n    <integer>43200</integer>") {
		t.Errorf("plist 未包含正确间隔秒数: %s", plist)
	}
}

func TestGenerateLaunchAgentPlistInvalidTime(t *testing.T) {
	_, _, err := generateLaunchAgentPlist(
		"/bin/test", "/home/test", "/tmp/out", "/tmp/err", "25:00", "",
	)
	if err == nil {
		t.Fatalf("非法时间应当报错")
	}

	_, _, err = generateLaunchAgentPlist(
		"/bin/test", "/home/test", "/tmp/out", "/tmp/err", "abc", "",
	)
	if err == nil {
		t.Fatalf("非法时间应当报错")
	}
}

func TestTailLines(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "test.log")
	content := "line1\nline2\nline3\nline4\nline5\n"
	if err := os.WriteFile(logFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	lines := tailLines(logFile, 3)
	if len(lines) != 3 {
		t.Fatalf("期望 3 行，实际 %d", len(lines))
	}
	if lines[0] != "line3" || lines[2] != "line5" {
		t.Fatalf("tail 内容不符合预期: %v", lines)
	}
}
