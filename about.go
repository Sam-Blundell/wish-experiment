package main

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type aboutScreen struct {
	width  int
	height int
}

func newAboutScreen(width, height int) aboutScreen {
	return aboutScreen{width: width, height: height}
}

func (s aboutScreen) Init() tea.Cmd { return nil }

func (s aboutScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		s.width = m.Width
		s.height = m.Height
	case tea.KeyPressMsg:
		switch m.String() {
		case "esc", "q", "enter":
			return s, func() tea.Msg { return ShowDirectoryMsg{} }
		}
	}
	return s, nil
}

func (s aboutScreen) View() string {
	contentWidth := 60
	if s.width > 0 && s.width-8 < contentWidth {
		contentWidth = s.width - 8
		if contentWidth < 20 {
			contentWidth = 20
		}
	}

	title := lipgloss.NewStyle().Foreground(colorAmber).Bold(true).Render("About")
	underline := lipgloss.NewStyle().Foreground(colorAmberDim).Render("═════")

	body := lipgloss.NewStyle().Foreground(colorCream).Width(contentWidth).Render(strings.Join([]string{
		"Before the modern website there were bulletin board systems. Servers people dialed into over a phone line to read messages, chat, and play games.",
		"Some were big professional operations with dozens of phone lines and thousands of users; many more were small boards run by a single host out of a spare room.",
		"",
		"The barrier to entry filtered for people who actually wanted to be there. Communities formed around a particular host, a particular flavour, a particular set of door games.",
		"",
		"I never personally experienced that era of the internet but i've always been interested in it.",
		"",
		"This is an attempt to build a platform that could recapture a little of that spirit using modern tools. There's nothing to sell and no plan to be useful, just a place to wander.",
		"",
		"I'll be adding to it slowly. Currently there's just a simple chat room, the next planned step is a more persistent messageboard.",
		"",
		"-Sam",
	}, "\n"))

	help := lipgloss.NewStyle().Foreground(colorAmberDim).Render("esc or q to go back")

	box := strings.Join([]string{title, underline, "", body, "", help}, "\n")

	if s.width > 0 {
		return lipgloss.PlaceHorizontal(s.width, lipgloss.Center,
			lipgloss.PlaceVertical(s.height, lipgloss.Center, box))
	}
	return box
}
