package main

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type directoryScreen struct {
	width    int
	height   int
	selected int
	options  []string
}

func newDirectoryScreen(width, height int) directoryScreen {
	return directoryScreen{
		width:   width,
		height:  height,
		options: []string{"testchat", "about", "exit"},
	}
}

func (s directoryScreen) Init() tea.Cmd { return nil }

func (s directoryScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		s.width = m.Width
		s.height = m.Height
	case tea.KeyPressMsg:
		switch m.String() {
		case "up", "k":
			if s.selected > 0 {
				s.selected--
			}
		case "down", "j":
			if s.selected < len(s.options)-1 {
				s.selected++
			}
		case "enter":
			switch s.options[s.selected] {
			case "testchat":
				return s, func() tea.Msg { return EnterChatMsg{} }
			case "about":
				return s, func() tea.Msg { return EnterAboutMsg{} }
			case "exit":
				return s, tea.Quit
			}
		case "q", "esc":
			return s, tea.Quit
		}
	}
	return s, nil
}

func (s directoryScreen) View() string {
	selectedStyle := lipgloss.NewStyle().Foreground(colorAmber).Bold(true)
	normalStyle := lipgloss.NewStyle().Foreground(colorCream)
	helpStyle := lipgloss.NewStyle().Foreground(colorAmberDim)
	titleStyle := lipgloss.NewStyle().Foreground(colorAmber).Bold(true)

	// Pad options to the widest one so the marker stays in a stable position
	// no matter which option is selected.
	maxOpt := 0
	for _, opt := range s.options {
		if w := lipgloss.Width(opt); w > maxOpt {
			maxOpt = w
		}
	}

	var menu []string
	for i, opt := range s.options {
		padded := opt + strings.Repeat(" ", maxOpt-lipgloss.Width(opt))
		if i == s.selected {
			menu = append(menu, selectedStyle.Render("► "+padded))
		} else {
			menu = append(menu, normalStyle.Render("  "+padded))
		}
	}

	help := "↑↓ to move · enter to select · q to quit"

	contentWidth := lipgloss.Width(help)
	if w := maxOpt + 2; w > contentWidth {
		contentWidth = w
	}
	center := lipgloss.NewStyle().Width(contentWidth).Align(lipgloss.Center)

	inner := strings.Join([]string{
		center.Render(titleStyle.Render("Directory")),
		"",
		center.Render(strings.Join(menu, "\n")),
		"",
		center.Render(helpStyle.Render(help)),
	}, "\n")

	box := lipgloss.NewStyle().
		Border(lipgloss.DoubleBorder()).
		BorderForeground(colorAmber).
		Padding(1, 4).
		Render(inner)

	if s.width > 0 {
		return lipgloss.PlaceHorizontal(s.width, lipgloss.Center,
			lipgloss.PlaceVertical(s.height, lipgloss.Center, box))
	}
	return box
}
