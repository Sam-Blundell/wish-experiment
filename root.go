package main

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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

type rootModel struct {
	active   Screen
	session  ssh.Session
	ip       string
	width    int
	height   int
	renderer *lipgloss.Renderer
}

func newRoot(s ssh.Session, ip string, r *lipgloss.Renderer) rootModel {
	return rootModel{
		session:  s,
		ip:       ip,
		renderer: r,
		active:   newWelcomeScreen(r),
	}
}

func (m rootModel) Init() tea.Cmd {
	return m.active.Init()
}

func (m rootModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// ctrl+c closes the SSH session from anywhere in the app.
	if k, ok := msg.(tea.KeyMsg); ok && k.Type == tea.KeyCtrlC {
		return m, tea.Quit
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case ShowDirectoryMsg:
		m.active = newDirectoryScreen(m.renderer, m.width, m.height)
		return m, m.active.Init()
	case EnterChatMsg:
		m.active = newChatScreen(m.session, m.renderer, m.ip, m.width, m.height)
		return m, m.active.Init()
	}

	active, cmd := m.active.Update(msg)
	m.active = active
	return m, cmd
}

func (m rootModel) View() string {
	return m.active.View()
}
