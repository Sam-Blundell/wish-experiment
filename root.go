package main

import (
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/ssh"
)

// Screen is the contract every sub-app implements. The root model swaps
// whichever Screen is active and forwards messages to it.
type Screen interface {
	Init() tea.Cmd
	Update(msg tea.Msg) (Screen, tea.Cmd)
	View() string
}

// Navigation messages a Screen can return as a tea.Cmd to ask the root to
// swap to a different screen.
type ShowDirectoryMsg struct{}
type EnterChatMsg struct{}
type EnterAboutMsg struct{}

type rootModel struct {
	active  Screen
	session ssh.Session
	ip      string
	width   int
	height  int
}

func newRoot(s ssh.Session, ip string) rootModel {
	return rootModel{
		session: s,
		ip:      ip,
		active:  newWelcomeScreen(),
	}
}

func (m rootModel) Init() tea.Cmd {
	return m.active.Init()
}

func (m rootModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// ctrl+c closes the SSH session from anywhere in the app.
	if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == "ctrl+c" {
		return m, tea.Quit
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case ShowDirectoryMsg:
		m.active = newDirectoryScreen(m.width, m.height)
		return m, m.active.Init()
	case EnterChatMsg:
		m.active = newChatScreen(m.session, m.ip, m.width, m.height)
		return m, m.active.Init()
	case EnterAboutMsg:
		m.active = newAboutScreen(m.width, m.height)
		return m, m.active.Init()
	}

	active, cmd := m.active.Update(msg)
	m.active = active
	return m, cmd
}

func (m rootModel) View() tea.View {
	v := tea.NewView(m.active.View())
	v.AltScreen = true
	return v
}
