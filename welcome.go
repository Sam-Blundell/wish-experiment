package main

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type welcomeScreen struct {
	width    int
	height   int
	renderer *lipgloss.Renderer
}

func newWelcomeScreen(r *lipgloss.Renderer) welcomeScreen {
	return welcomeScreen{renderer: r}
}

func (s welcomeScreen) Init() tea.Cmd { return nil }

func (s welcomeScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		s.width = m.Width
		s.height = m.Height
	case tea.KeyMsg:
		_ = m
		return s, func() tea.Msg { return ShowDirectoryMsg{} }
	}
	return s, nil
}

func (s welcomeScreen) View() string {
	r := s.renderer
	purple := lipgloss.Color("212")
	dim := lipgloss.Color("241")
	white := lipgloss.Color("252")

	box := r.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(purple).
		Padding(1, 3).
		Foreground(white).
		Bold(true).
		Align(lipgloss.Center).
		Render(
			"Welcome\n\n" +
				"This server is open to anyone.\n" +
				"Everything is public.\n" +
				"You have been warned.\n\n" +
				r.NewStyle().Foreground(dim).Render("press any key to continue"),
		)

	if s.width > 0 {
		return r.PlaceHorizontal(s.width, lipgloss.Center,
			r.PlaceVertical(s.height, lipgloss.Center, box))
	}
	return box
}
