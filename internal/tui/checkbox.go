package tui

import (
	"fmt"
	"strings"

	"config-mesh/internal/model"
	"config-mesh/internal/scanner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// CheckboxModel 复选框 TUI 数据模型
type CheckboxModel struct {
	Title     string
	Items     []model.ConfigItem
	Cursor    int
	Confirmed bool
	Canceled  bool
	Width     int
	Height    int
}

// NewCheckboxModel 创建复选框模型
func NewCheckboxModel(title string, items []model.ConfigItem) CheckboxModel {
	return CheckboxModel{
		Title:  title,
		Items:  items,
		Cursor: 0,
	}
}

func (m CheckboxModel) Init() tea.Cmd {
	return nil
}

func (m CheckboxModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.Width = msg.Width
		m.Height = msg.Height

	case tea.KeyMsg:
		entryCount := len(m.entries())
		switch msg.String() {
		case "ctrl+c", "q", "esc":
			m.Canceled = true
			return m, tea.Quit

		case "up", "k":
			if entryCount == 0 {
				break
			}
			if m.Cursor > 0 {
				m.Cursor--
			} else {
				m.Cursor = entryCount - 1
			}

		case "down", "j":
			if entryCount == 0 {
				break
			}
			if m.Cursor < entryCount-1 {
				m.Cursor++
			} else {
				m.Cursor = 0
			}

		case "home", "g":
			m.Cursor = 0

		case "end", "G":
			if entryCount > 0 {
				m.Cursor = entryCount - 1
			}

		case "pgup":
			m.Cursor -= m.pageSize()
			if m.Cursor < 0 {
				m.Cursor = 0
			}

		case "pgdown":
			if entryCount > 0 {
				m.Cursor += m.pageSize()
				if m.Cursor >= entryCount {
					m.Cursor = entryCount - 1
				}
			}

		case " ":
			m.toggleFocusedEntry()

		case "a": // 全选
			for i := range m.Items {
				m.Items[i].Selected = true
			}

		case "n": // 全部取消
			for i := range m.Items {
				m.Items[i].Selected = false
			}

		case "i": // 反选
			for i := range m.Items {
				m.Items[i].Selected = !m.Items[i].Selected
			}

		case "enter":
			m.Confirmed = true
			return m, tea.Quit
		}
	}

	return m, nil
}

func (m CheckboxModel) pageSize() int {
	// 分类标题也占一行，因此翻页步长稍小于列表视口高度。
	size := m.listViewportHeight() - 1
	if size < 1 {
		return 1
	}
	return size
}

func (m CheckboxModel) layoutFlags() (showTitle, showSubtitle, showStats, showHelp bool) {
	height := m.Height
	if height <= 0 {
		height = 24
	}

	// 极小终端中优先保留至少一行列表，保证当前光标不会消失。
	return height >= 2, height >= 6, height >= 3, height >= 8
}

func (m CheckboxModel) listViewportHeight() int {
	height := m.Height
	if height <= 0 {
		height = 24
	}

	showTitle, showSubtitle, showStats, showHelp := m.layoutFlags()
	fixedRows := 0
	for _, shown := range []bool{showTitle, showSubtitle, showStats, showHelp} {
		if shown {
			fixedRows++
		}
	}

	listHeight := height - fixedRows
	if listHeight < 1 {
		return 1
	}
	return listHeight
}

type checkboxRow struct {
	text string
}

type checkboxEntry struct {
	category  model.ConfigCategory
	itemIndex int
}

func (m CheckboxModel) entries() []checkboxEntry {
	entries := make([]checkboxEntry, 0, len(m.Items)*2)
	var currentCategory model.ConfigCategory
	for i, item := range m.Items {
		if item.Category != currentCategory && item.Category != "" {
			currentCategory = item.Category
			entries = append(entries, checkboxEntry{category: currentCategory, itemIndex: -1})
		}
		entries = append(entries, checkboxEntry{itemIndex: i})
	}
	return entries
}

func (m *CheckboxModel) toggleFocusedEntry() {
	entries := m.entries()
	if m.Cursor < 0 || m.Cursor >= len(entries) {
		return
	}
	entry := entries[m.Cursor]
	if entry.itemIndex >= 0 {
		m.Items[entry.itemIndex].Selected = !m.Items[entry.itemIndex].Selected
		return
	}

	allSelected := true
	found := false
	for i := range m.Items {
		if m.Items[i].Category == entry.category {
			found = true
			allSelected = allSelected && m.Items[i].Selected
		}
	}
	if !found {
		return
	}
	for i := range m.Items {
		if m.Items[i].Category == entry.category {
			m.Items[i].Selected = !allSelected
		}
	}
}

func (m CheckboxModel) categorySelection(category model.ConfigCategory) (selected, total int) {
	for _, item := range m.Items {
		if item.Category == category {
			total++
			if item.Selected {
				selected++
			}
		}
	}
	return selected, total
}

func (m CheckboxModel) itemRows() []checkboxRow {
	entries := m.entries()
	rows := make([]checkboxRow, 0, len(entries))
	for entryIndex, entry := range entries {
		cursor := "  "
		if m.Cursor == entryIndex {
			cursor = CursorStyle.Render("👉")
		}

		if entry.itemIndex < 0 {
			selected, total := m.categorySelection(entry.category)
			checked := "[ ]"
			switch {
			case total > 0 && selected == total:
				checked = SelectedStyle.Render("[✓]")
			case selected > 0:
				checked = RecommendedBadgeStyle.Render("[-]")
			}
			rows = append(rows, checkboxRow{text: fmt.Sprintf("%s %s %s %s",
				cursor,
				checked,
				CategoryBadgeStyle.MarginRight(0).Render(string(entry.category)),
				DimStyle.Render(fmt.Sprintf("(%d/%d)", selected, total)),
			)})
			continue
		}

		i := entry.itemIndex
		item := m.Items[i]

		checked := "[ ]"
		if item.Selected {
			checked = SelectedStyle.Render("[✓]")
		}

		itemName := item.Name
		sizeInfo := ""
		if item.Deleted {
			itemName = DimStyle.Render(item.Name + " (已删除，选中后传播删除)")
		} else if !item.Exists {
			itemName = DimStyle.Render(item.Name + " (本地未创建)")
		} else {
			sizeInfo = " " + DimStyle.Render(fmt.Sprintf("(%s)", scanner.FormatSize(item.Size)))
		}

		recBadge := ""
		if item.Recommended {
			recBadge = " " + RecommendedBadgeStyle.Render("★推荐")
		}
		secretBadge := ""
		if item.SecretKind != "" {
			label := "[🔐凭据·默认不选]"
			if item.SecretKind == model.SecretKindDetectedConfig {
				label = "[🔐检测到凭据]"
			}
			secretBadge = " " + StatusConflictStyle.Render(label)
		}

		diffBadge := ""
		switch item.DiffStatus {
		case model.DiffStatusSynced:
			diffBadge = " " + StatusSyncedStyle.Render("[已同步]")
		case model.DiffStatusLocalModified:
			diffBadge = " " + StatusLocalModifiedStyle.Render("[本地修改]")
		case model.DiffStatusRemoteModified:
			diffBadge = " " + StatusRemoteModifiedStyle.Render("[云端更新]")
		case model.DiffStatusConflict:
			diffBadge = " " + StatusConflictStyle.Render("[有冲突]")
		case model.DiffStatusNew:
			diffBadge = " " + StatusNewStyle.Render("[未同步]")
		}

		rows = append(rows, checkboxRow{
			text: fmt.Sprintf("%s %s   %s%s%s%s%s", cursor, checked, itemName, sizeInfo, diffBadge, secretBadge, recBadge),
		})
	}

	return rows
}

func (m CheckboxModel) visibleItemRows() []checkboxRow {
	rows := m.itemRows()
	if len(rows) == 0 {
		return nil
	}

	height := m.listViewportHeight()
	top := m.Cursor - height + 1
	if top < 0 {
		top = 0
	}
	bottom := top + height
	if bottom > len(rows) {
		bottom = len(rows)
	}
	return rows[top:bottom]
}

func (m CheckboxModel) truncateLine(line string) string {
	if m.Width <= 0 {
		return line
	}
	return ansi.Truncate(line, m.Width, "…")
}

func (m CheckboxModel) View() string {
	if m.Canceled {
		return DimStyle.Render("操作已取消。\n")
	}

	showTitle, showSubtitle, showStats, showHelp := m.layoutFlags()
	lines := make([]string, 0, m.Height)
	if showTitle {
		lines = append(lines, TitleStyle.MarginBottom(0).Render(m.Title))
	}
	if showSubtitle {
		lines = append(lines, SubtitleStyle.MarginBottom(0).Render("方向键/j/k 移动，空格切换分类或单项，PgUp/PgDn 翻页，Enter 确认。"))
	}
	for _, row := range m.visibleItemRows() {
		lines = append(lines, row.text)
	}

	// 统计选中的数量和总大小
	selectedCount := 0
	var totalSelectedSize int64
	for _, item := range m.Items {
		if item.Selected {
			selectedCount++
			if item.Exists {
				totalSelectedSize += item.Size
			}
		}
	}

	totalSizeStr := scanner.FormatSize(totalSelectedSize)
	if showStats {
		entryCount := len(m.entries())
		position := 0
		if entryCount > 0 {
			position = m.Cursor + 1
		}
		lines = append(lines, fmt.Sprintf("位置: %d/%d • 已选: %s/%d %s",
			position,
			entryCount,
			SelectedStyle.Render(fmt.Sprintf("%d", selectedCount)),
			len(m.Items),
			DimStyle.Render(fmt.Sprintf("(总计: %s)", totalSizeStr)),
		))
	}
	if showHelp {
		lines = append(lines, HelpStyle.MarginTop(0).Render("[空格] 选择 • [a/n/i] 全选/全不选/反选 • [Home/End] 首尾 • [Enter] 确认 • [q/Esc] 退出"))
	}

	for i := range lines {
		lines[i] = m.truncateLine(lines[i])
	}
	return strings.Join(lines, "\n")
}

// RunCheckboxTUI 启动复选框 TUI 交互并返回用户选择的结果
func RunCheckboxTUI(title string, items []model.ConfigItem) ([]model.ConfigItem, error) {
	if len(items) == 0 {
		return items, nil
	}

	p := tea.NewProgram(NewCheckboxModel(title, items))
	m, err := p.Run()
	if err != nil {
		return nil, fmt.Errorf("启动 TUI 界面失败: %w", err)
	}

	cbModel := m.(CheckboxModel)
	if cbModel.Canceled {
		return nil, fmt.Errorf("用户取消了操作")
	}

	return cbModel.Items, nil
}
