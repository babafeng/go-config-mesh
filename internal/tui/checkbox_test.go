package tui

import (
	"fmt"
	"strings"
	"testing"

	"config-mesh/internal/model"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func checkboxItems(count int) []model.ConfigItem {
	items := make([]model.ConfigItem, count)
	for i := range items {
		items[i] = model.ConfigItem{
			ID:       fmt.Sprintf("item-%02d", i+1),
			Name:     fmt.Sprintf("配置条目-%02d-这是用于验证横向截断的很长名称", i+1),
			Category: model.CategoryGit,
			Exists:   true,
		}
	}
	return items
}

func updateCheckbox(t *testing.T, m CheckboxModel, msg tea.Msg) CheckboxModel {
	t.Helper()
	updated, _ := m.Update(msg)
	result, ok := updated.(CheckboxModel)
	if !ok {
		t.Fatalf("Update 返回了意外模型类型 %T", updated)
	}
	return result
}

func TestCheckboxViewKeepsCursorInsideSmallViewport(t *testing.T) {
	m := NewCheckboxModel("长列表", checkboxItems(30))
	m = updateCheckbox(t, m, tea.WindowSizeMsg{Width: 40, Height: 10})
	for range 20 {
		m = updateCheckbox(t, m, tea.KeyMsg{Type: tea.KeyDown})
	}

	view := m.View()
	plain := ansi.Strip(view)
	if !strings.Contains(plain, "👉 [ ]   配置条目-20") {
		t.Fatalf("当前光标条目未显示:\n%s", plain)
	}
	if got := len(strings.Split(view, "\n")); got > 10 {
		t.Fatalf("渲染高度为 %d，超过终端高度 10:\n%s", got, plain)
	}
	for _, line := range strings.Split(view, "\n") {
		if width := ansi.StringWidth(line); width > 40 {
			t.Fatalf("渲染行宽为 %d，超过终端宽度 40: %q", width, ansi.Strip(line))
		}
	}
}

func TestCheckboxNavigationForLongList(t *testing.T) {
	m := NewCheckboxModel("长列表", checkboxItems(30))
	m = updateCheckbox(t, m, tea.WindowSizeMsg{Width: 80, Height: 10})

	m = updateCheckbox(t, m, tea.KeyMsg{Type: tea.KeyPgDown})
	if m.Cursor <= 0 {
		t.Fatalf("PgDn 后光标未向下移动: %d", m.Cursor)
	}
	m = updateCheckbox(t, m, tea.KeyMsg{Type: tea.KeyEnd})
	if m.Cursor != 30 {
		t.Fatalf("End 后光标=%d，期望 30", m.Cursor)
	}
	m = updateCheckbox(t, m, tea.KeyMsg{Type: tea.KeyHome})
	if m.Cursor != 0 {
		t.Fatalf("Home 后光标=%d，期望 0", m.Cursor)
	}
}

func TestCheckboxTinyTerminalStillShowsCursor(t *testing.T) {
	m := NewCheckboxModel("极小终端", checkboxItems(3))
	m.Cursor = 3
	m = updateCheckbox(t, m, tea.WindowSizeMsg{Width: 24, Height: 1})

	view := ansi.Strip(m.View())
	if !strings.Contains(view, "👉 [ ]   配置条目-03") {
		t.Fatalf("极小终端未显示当前光标条目: %q", view)
	}
	if got := len(strings.Split(view, "\n")); got != 1 {
		t.Fatalf("极小终端渲染了 %d 行，期望 1", got)
	}
}

func TestCheckboxCategoryEntryTogglesWholeCategory(t *testing.T) {
	items := checkboxItems(3)
	items = append(items, model.ConfigItem{ID: "shell", Name: "~/.zshrc", Category: model.CategoryShell})
	m := NewCheckboxModel("分类选择", items)

	// 初始焦点位于 Git 分类总选择框。
	m = updateCheckbox(t, m, tea.KeyMsg{Type: tea.KeySpace})
	for i := 0; i < 3; i++ {
		if !m.Items[i].Selected {
			t.Fatalf("分类全选没有选中第 %d 项", i+1)
		}
	}
	if m.Items[3].Selected {
		t.Fatal("分类全选不应影响其他分类")
	}
	if !strings.Contains(ansi.Strip(m.View()), "[✓]  Git & 开发  (3/3)") {
		t.Fatalf("分类总选择状态未正确渲染:\n%s", ansi.Strip(m.View()))
	}

	// 移到第一条子项并取消后，分类应显示半选。
	m = updateCheckbox(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = updateCheckbox(t, m, tea.KeyMsg{Type: tea.KeySpace})
	if !strings.Contains(ansi.Strip(m.View()), "[-]  Git & 开发  (2/3)") {
		t.Fatalf("分类半选状态未正确渲染:\n%s", ansi.Strip(m.View()))
	}
}
