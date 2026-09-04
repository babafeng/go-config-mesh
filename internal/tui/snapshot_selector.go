package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// SnapshotOption 快照选项
type SnapshotOption struct {
	SnapshotID string
	Hostname   string
	DeviceID   string
	Ownership  string // "本机" 或 "其他设备"
	CreatedAt  time.Time
}

// SnapshotSelectorModel 快照选择模型
type SnapshotSelectorModel struct {
	Title    string
	Options  []SnapshotOption
	Cursor   int
	Selected int
	Canceled bool
}

// NewSnapshotSelectorModel 创建快照选择模型
func NewSnapshotSelectorModel(title string, options []SnapshotOption, defaultIndex int) SnapshotSelectorModel {
	if defaultIndex < 0 || defaultIndex >= len(options) {
		defaultIndex = 0
	}
	return SnapshotSelectorModel{
		Title:    title,
		Options:  options,
		Cursor:   defaultIndex,
		Selected: defaultIndex,
	}
}

func (m SnapshotSelectorModel) Init() tea.Cmd {
	return nil
}

func (m SnapshotSelectorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q", "esc":
			m.Canceled = true
			return m, tea.Quit

		case "up", "k":
			if m.Cursor > 0 {
				m.Cursor--
			} else {
				m.Cursor = len(m.Options) - 1
			}

		case "down", "j":
			if m.Cursor < len(m.Options)-1 {
				m.Cursor++
			} else {
				m.Cursor = 0
			}

		case "enter", " ":
			m.Selected = m.Cursor
			return m, tea.Quit
		}
	}

	return m, nil
}

func (m SnapshotSelectorModel) View() string {
	if m.Canceled {
		return DimStyle.Render("已取消快照选择。\n")
	}

	var b strings.Builder
	b.WriteString(TitleStyle.Render(m.Title))
	b.WriteString("\n\n")

	for i, opt := range m.Options {
		cursor := "  "
		if m.Cursor == i {
			cursor = CursorStyle.Render("> ")
		}

		ownershipTag := fmt.Sprintf("[%s]", opt.Ownership)
		if opt.Ownership == "其他设备" {
			ownershipTag = RecommendedBadgeStyle.Render(ownershipTag)
		} else {
			ownershipTag = DimStyle.Render(ownershipTag)
		}

		name := opt.SnapshotID
		if m.Cursor == i {
			name = SelectedStyle.Render(opt.SnapshotID)
		}

		created := "时间未知"
		if !opt.CreatedAt.IsZero() {
			created = opt.CreatedAt.Local().Format("2006-01-02 15:04:05")
		}

		hostInfo := ""
		if opt.Hostname != "" {
			hostInfo = fmt.Sprintf(" (主机: %s, 时间: %s)", opt.Hostname, created)
		} else {
			hostInfo = fmt.Sprintf(" (时间: %s)", created)
		}

		b.WriteString(fmt.Sprintf("%s %s %s%s\n\n", cursor, ownershipTag, name, DimStyle.Render(hostInfo)))
	}

	b.WriteString(HelpStyle.Render("快捷键: [↑/↓ 或 j/k] 移动 • [Enter] 确认选择 • [q/Esc] 取消"))
	b.WriteString("\n")

	return b.String()
}

// RunSnapshotSelector 运行快照选择器
func RunSnapshotSelector(title string, options []SnapshotOption, defaultIndex int) (int, error) {
	if len(options) == 0 {
		return -1, fmt.Errorf("没有可用的快照选项")
	}
	p := tea.NewProgram(NewSnapshotSelectorModel(title, options, defaultIndex))
	m, err := p.Run()
	if err != nil {
		return -1, fmt.Errorf("运行快照选择器失败: %w", err)
	}

	sm := m.(SnapshotSelectorModel)
	if sm.Canceled {
		return -1, fmt.Errorf("用户取消了快照选择")
	}

	return sm.Selected, nil
}

// OperationOption 操作模式选项
type OperationOption struct {
	Mode        string
	Title       string
	Description string
}

// OperationSelectorModel 操作单选模型
type OperationSelectorModel struct {
	Title    string
	Options  []OperationOption
	Cursor   int
	Selected string
	Canceled bool
}

// DefaultOperationOptions 默认操作选项列表
func DefaultOperationOptions() []OperationOption {
	return []OperationOption{
		{
			Mode:        "download",
			Title:       "从云端拉取配置并合并到当前设备 (推荐用于新设备同步)",
			Description: "可挑选云端主机快照，并在勾选所需配置项后解密合并到本机",
		},
		{
			Mode:        "upload",
			Title:       "将当前设备配置更新到本机固定目录（仅上传变化项）",
			Description: "扫描当前设备配置，通过 Age 加密后上传推送到 GitHub 专属槽位",
		},
	}
}

// NewOperationSelectorModel 创建操作选择模型
func NewOperationSelectorModel(title string, options []OperationOption, defaultIndex int) OperationSelectorModel {
	if defaultIndex < 0 || defaultIndex >= len(options) {
		defaultIndex = 0
	}
	return OperationSelectorModel{
		Title:    title,
		Options:  options,
		Cursor:   defaultIndex,
		Selected: options[defaultIndex].Mode,
	}
}

func (m OperationSelectorModel) Init() tea.Cmd {
	return nil
}

func (m OperationSelectorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q", "esc":
			m.Canceled = true
			return m, tea.Quit

		case "up", "k":
			if m.Cursor > 0 {
				m.Cursor--
			} else {
				m.Cursor = len(m.Options) - 1
			}

		case "down", "j":
			if m.Cursor < len(m.Options)-1 {
				m.Cursor++
			} else {
				m.Cursor = 0
			}

		case "enter", " ":
			m.Selected = m.Options[m.Cursor].Mode
			return m, tea.Quit
		}
	}

	return m, nil
}

func (m OperationSelectorModel) View() string {
	if m.Canceled {
		return DimStyle.Render("已取消操作选择。\n")
	}

	var b strings.Builder
	title := m.Title
	if title == "" {
		title = ":: 请选择本次执行的操作"
	}
	b.WriteString(TitleStyle.Render(title))
	b.WriteString("\n\n")

	for i, opt := range m.Options {
		cursor := "  "
		if m.Cursor == i {
			cursor = CursorStyle.Render("> ")
		}

		optTitle := opt.Title
		if m.Cursor == i {
			optTitle = SelectedStyle.Render(opt.Title)
		}

		b.WriteString(fmt.Sprintf("%s %s\n", cursor, optTitle))
		if opt.Description != "" {
			b.WriteString(fmt.Sprintf("     %s\n\n", DimStyle.Render(opt.Description)))
		}
	}

	b.WriteString(HelpStyle.Render("快捷键: [↑/↓ 或 j/k] 移动 • [Enter] 确认选择 • [q/Esc] 取消"))
	b.WriteString("\n")

	return b.String()
}

// RunOperationSelector 运行操作模式选择器
func RunOperationSelector(title string, options []OperationOption, defaultIndex int) (string, error) {
	if len(options) == 0 {
		return "", fmt.Errorf("没有可用的操作选项")
	}
	p := tea.NewProgram(NewOperationSelectorModel(title, options, defaultIndex))
	m, err := p.Run()
	if err != nil {
		return "", fmt.Errorf("运行操作选择器失败: %w", err)
	}

	om := m.(OperationSelectorModel)
	if om.Canceled {
		return "", fmt.Errorf("用户取消了操作选择")
	}

	return om.Selected, nil
}
