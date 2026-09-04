package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestSnapshotSelectorModel_Navigation(t *testing.T) {
	opts := []SnapshotOption{
		{
			SnapshotID: "host-1",
			Hostname:   "macbook-1",
			Ownership:  "其他设备",
			CreatedAt:  time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC),
		},
		{
			SnapshotID: "host-2",
			Hostname:   "macbook-2",
			Ownership:  "本机",
			CreatedAt:  time.Date(2026, 9, 4, 13, 0, 0, 0, time.UTC),
		},
	}

	model := NewSnapshotSelectorModel("测试快照选择", opts, 0)
	if model.Cursor != 0 {
		t.Fatalf("期望初始光标在 0，实际: %d", model.Cursor)
	}

	// 测试向下移动
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	m := updated.(SnapshotSelectorModel)
	if m.Cursor != 1 {
		t.Fatalf("向下移动后期望光标在 1，实际: %d", m.Cursor)
	}

	// 测试回卷到顶部
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(SnapshotSelectorModel)
	if m.Cursor != 0 {
		t.Fatalf("回卷后期望光标在 0，实际: %d", m.Cursor)
	}

	// 测试回卷到底部
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(SnapshotSelectorModel)
	if m.Cursor != 1 {
		t.Fatalf("向上回卷后期望光标在 1，实际: %d", m.Cursor)
	}

	// 测试确认选择
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(SnapshotSelectorModel)
	if m.Selected != 1 {
		t.Fatalf("确认后期望选中的索引为 1，实际: %d", m.Selected)
	}
	if cmd == nil {
		t.Fatal("确认后期望返回 tea.Quit")
	}

	// 测试取消
	model = NewSnapshotSelectorModel("测试取消", opts, 0)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(SnapshotSelectorModel)
	if !m.Canceled {
		t.Fatal("按 Esc 期望 Canceled 为 true")
	}

	view := m.View()
	if !strings.Contains(view, "已取消快照选择") {
		t.Fatalf("取消状态视图输出不符合预期: %s", view)
	}
}

func TestSnapshotSelectorModel_View(t *testing.T) {
	opts := []SnapshotOption{
		{
			SnapshotID: "xf-air-1234",
			Hostname:   "xf-air",
			Ownership:  "其他设备",
			CreatedAt:  time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC),
		},
	}

	model := NewSnapshotSelectorModel("选择测试", opts, 0)
	view := model.View()
	if !strings.Contains(view, "xf-air-1234") {
		t.Fatalf("视图未包含快照标识: %s", view)
	}
	if !strings.Contains(view, "其他设备") {
		t.Fatalf("视图未包含设备归属: %s", view)
	}
}

func TestOperationSelectorModel_Navigation(t *testing.T) {
	opts := DefaultOperationOptions()
	model := NewOperationSelectorModel("请选择操作", opts, 0)
	if model.Cursor != 0 || model.Selected != "download" {
		t.Fatalf("初始状态不符合预期: cursor=%d, selected=%s", model.Cursor, model.Selected)
	}

	// 向下移动
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	m := updated.(OperationSelectorModel)
	if m.Cursor != 1 {
		t.Fatalf("移动后 cursor 期望 1，实际 %d", m.Cursor)
	}

	// 确认
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(OperationSelectorModel)
	if m.Selected != "upload" {
		t.Fatalf("确认后期望选择 upload，实际 %s", m.Selected)
	}
	if cmd == nil {
		t.Fatal("确认后期望退出")
	}

	// 取消
	model = NewOperationSelectorModel("请选择操作", opts, 0)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	m = updated.(OperationSelectorModel)
	if !m.Canceled {
		t.Fatal("按 q 应取消")
	}
}
