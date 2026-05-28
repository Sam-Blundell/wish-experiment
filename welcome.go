package main

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type welcomeScreen struct {
	width  int
	height int
}

func newWelcomeScreen() welcomeScreen {
	return welcomeScreen{}
}

func (s welcomeScreen) Init() tea.Cmd { return nil }

func (s welcomeScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		s.width = m.Width
		s.height = m.Height
	case tea.KeyPressMsg:
		_ = m
		return s, func() tea.Msg { return ShowDirectoryMsg{} }
	}
	return s, nil
}

func (s welcomeScreen) View() string {
	box := lipgloss.NewStyle().
		Border(lipgloss.DoubleBorder()).
		BorderForeground(colorAmber).
		Padding(1, 3).
		Foreground(colorCream).
		Bold(true).
		Align(lipgloss.Center).
		Render(
			"Welcome\n\n" +
				"This server is open to anyone.\n" +
				"Everything is public.\n" +
				"You have been warned.\n\n" +
				lipgloss.NewStyle().Foreground(colorAmberDim).Render("press any key to continue"),
		)

	if s.width > 0 {
		return lipgloss.PlaceHorizontal(s.width, lipgloss.Center,
			lipgloss.PlaceVertical(s.height, lipgloss.Center, box))
	}
	return box
}
