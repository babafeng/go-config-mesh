package tui

import "github.com/charmbracelet/lipgloss"

var (
	// 主题色彩
	PrimaryColor   = lipgloss.Color("#7D56F4") // 紫色
	SecondaryColor = lipgloss.Color("#04B575") // 绿色
	WarningColor   = lipgloss.Color("#FFAF00") // 黄色
	DangerColor    = lipgloss.Color("#FF5F87") // 红色
	SubtleColor    = lipgloss.Color("#626262") // 灰色
	HighlightColor = lipgloss.Color("#00D7D7") // 青色

	// 样式定义
	TitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FAFAFA")).
			Background(PrimaryColor).
			Padding(0, 1).
			MarginBottom(1)

	SubtitleStyle = lipgloss.NewStyle().
			Foreground(SubtleColor).
			MarginBottom(1)

	CategoryBadgeStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#FAFAFA")).
				Background(lipgloss.Color("#3C3836")).
				Padding(0, 1).
				MarginRight(1)

	SelectedStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(SecondaryColor)

	CursorStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(PrimaryColor)

	DimStyle = lipgloss.NewStyle().
			Foreground(SubtleColor)

	RecommendedBadgeStyle = lipgloss.NewStyle().
				Foreground(WarningColor).
				Bold(true)

	// 差异状态徽章样式
	StatusSyncedStyle = lipgloss.NewStyle().
				Foreground(SecondaryColor).
				Bold(true)

	StatusLocalModifiedStyle = lipgloss.NewStyle().
					Foreground(HighlightColor).
					Bold(true)

	StatusRemoteModifiedStyle = lipgloss.NewStyle().
					Foreground(WarningColor).
					Bold(true)

	StatusConflictStyle = lipgloss.NewStyle().
				Foreground(DangerColor).
				Bold(true)

	StatusNewStyle = lipgloss.NewStyle().
			Foreground(PrimaryColor).
			Bold(true)

	HelpStyle = lipgloss.NewStyle().
			Foreground(SubtleColor).
			MarginTop(1)

	SuccessBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(SecondaryColor).
			Padding(1, 2).
			Margin(1, 0)
)
