package tui

import (
	"fmt"
	"strings"

	"config-mesh/internal/model"
	tea "github.com/charmbracelet/bubbletea"
)

// StrategyOption 策略选项
type StrategyOption struct {
	Strategy    model.Strategy
	Title       string
	Description string
}

// StrategyModel 策略单选模型
type StrategyModel struct {
	Options  []StrategyOption
	Cursor   int
	Selected model.Strategy
	Canceled bool
}

// DefaultStrategyOptions 默认提供的策略选项列表
var DefaultStrategyOptions = []StrategyOption{
	{
		Strategy:    model.StrategyOverwrite,
		Title:       "覆盖 (Overwrite) 【安全默认】",
		Description: "经过完整备份和负载校验后，使用远端版本替换本地配置",
	},
	{
		Strategy:    model.StrategyAppend,
		Title:       "追加/合并 (Append)",
		Description: "仅支持 Shell/Git 标记块、JSON 深度合并和目录 overlay；其他格式会被拒绝",
	},
	{
		Strategy:    model.StrategySkip,
		Title:       "跳过 (Skip)",
		Description: "保持本地现有文件不变，不执行同步修改",
	},
}

// NewStrategyModel 创建策略选择模型
func NewStrategyModel() StrategyModel {
	return StrategyModel{
		Options:  DefaultStrategyOptions,
		Cursor:   0,
		Selected: model.StrategyOverwrite,
	}
}

func (m StrategyModel) Init() tea.Cmd {
	return nil
}

func (m StrategyModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
			m.Selected = m.Options[m.Cursor].Strategy
			return m, tea.Quit
		}
	}

	return m, nil
}

func (m StrategyModel) View() string {
	if m.Canceled {
		return DimStyle.Render("已取消策略选择。\n")
	}

	var b strings.Builder
	b.WriteString(TitleStyle.Render(":: 请选择本地已有配置的合并/同步策略"))
	b.WriteString("\n\n")

	for i, opt := range m.Options {
		cursor := "  "
		if m.Cursor == i {
			cursor = CursorStyle.Render("> ")
		}

		title := opt.Title
		if m.Cursor == i {
			title = SelectedStyle.Render(opt.Title)
		}

		b.WriteString(fmt.Sprintf("%s %s\n", cursor, title))
		b.WriteString(fmt.Sprintf("     %s\n\n", DimStyle.Render(opt.Description)))
	}

	b.WriteString(HelpStyle.Render("快捷键: [↑/↓ 或 j/k] 移动 • [Enter] 确认选择 • [q/Esc] 取消"))
	b.WriteString("\n")

	return b.String()
}

// RunStrategySelector 运行策略选择器
func RunStrategySelector() (model.Strategy, error) {
	p := tea.NewProgram(NewStrategyModel())
	m, err := p.Run()
	if err != nil {
		return model.StrategyAppend, fmt.Errorf("运行策略选择器失败: %w", err)
	}

	sm := m.(StrategyModel)
	if sm.Canceled {
		return model.StrategyAppend, fmt.Errorf("用户取消选择")
	}

	return sm.Selected, nil
}
