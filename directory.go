package main

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type directoryScreen struct {
	width    int
	height   int
	selected int
	options  []string
	renderer *lipgloss.Renderer
}

func newDirectoryScreen(r *lipgloss.Renderer, width, height int) directoryScreen {
	return directoryScreen{
		renderer: r,
		width:    width,
		height:   height,
		options:  []string{"testchat", "exit"},
	}
}

func (s directoryScreen) Init() tea.Cmd { return nil }

func (s directoryScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		s.width = m.Width
		s.height = m.Height
	case tea.KeyMsg:
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
	r := s.renderer
	purple := lipgloss.Color("212")
	dim := lipgloss.Color("241")
	white := lipgloss.Color("252")

	selectedStyle := r.NewStyle().Foreground(purple).Bold(true)
	normalStyle := r.NewStyle().Foreground(white)
	helpStyle := r.NewStyle().Foreground(dim)
	titleStyle := r.NewStyle().Foreground(purple).Bold(true)

	// Pad options to the widest one so the "> " marker stays in a stable
	// position no matter which option is selected.
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
			menu = append(menu, selectedStyle.Render("> "+padded))
		} else {
			menu = append(menu, normalStyle.Render("  "+padded))
		}
	}

	help := "↑↓ to move · enter to select · q to quit"

	// Pick a content width that fits the widest piece, then center each piece
	// inside that width so they all share the same horizontal axis.
	contentWidth := lipgloss.Width(help)
	if w := maxOpt + 2; w > contentWidth {
		contentWidth = w
	}
	center := r.NewStyle().Width(contentWidth).Align(lipgloss.Center)

	box := strings.Join([]string{
		center.Render(titleStyle.Render("Directory")),
		"",
		center.Render(strings.Join(menu, "\n")),
		"",
		center.Render(helpStyle.Render(help)),
	}, "\n")

	if s.width > 0 {
		return r.PlaceHorizontal(s.width, lipgloss.Center,
			r.PlaceVertical(s.height, lipgloss.Center, box))
	}
	return box
}
