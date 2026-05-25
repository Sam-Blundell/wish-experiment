package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	"github.com/charmbracelet/wish/bubbletea"
)

const (
	host = "0.0.0.0"
	port = 2222
)

type link struct {
	name string
	url  string
}

var links = []link{
	{name: "Website", url: "samblundell.co.uk"},
	{name: "GitHub", url: "github.com/Sam-Blundell"},
}

type model struct {
	width  int
	height int
	cursor int
}

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(links)-1 {
				m.cursor++
			}
		}
	}
	return m, nil
}

func (m model) View() string {
	purple := lipgloss.Color("212")
	dim := lipgloss.Color("241")

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(purple).
		MarginBottom(1)

	selectedStyle := lipgloss.NewStyle().
		Foreground(purple).
		Bold(true)

	normalStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("252"))

	urlStyle := lipgloss.NewStyle().
		Foreground(dim)

	helpStyle := lipgloss.NewStyle().
		Foreground(dim).
		MarginTop(1)

	var b strings.Builder

	b.WriteString(titleStyle.Render("Sam Blundell"))
	b.WriteString("\n")

	for i, l := range links {
		cursor := "  "
		style := normalStyle
		if i == m.cursor {
			cursor = "> "
			style = selectedStyle
		}
		line := fmt.Sprintf("%s%-10s %s", cursor, style.Render(l.name), urlStyle.Render(l.url))
		b.WriteString(line)
		b.WriteString("\n")
	}

	b.WriteString(helpStyle.Render("j/k to navigate · q to quit"))

	content := lipgloss.NewStyle().
		Padding(1, 2).
		Render(b.String())

	if m.width > 0 {
		content = lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, content)
	}

	return content
}

func main() {
	s, err := wish.NewServer(
		wish.WithAddress(fmt.Sprintf("%s:%d", host, port)),
		wish.WithHostKeyPath(".ssh/id_ed25519"),
		wish.WithMiddleware(
			bubbletea.Middleware(func(s ssh.Session) (tea.Model, []tea.ProgramOption) {
				return model{}, []tea.ProgramOption{tea.WithAltScreen()}
			}),
		),
	)
	if err != nil {
		log.Fatalf("could not start server: %s", err)
	}

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)

	log.Printf("Starting SSH server on %s:%d", host, port)
	go func() {
		if err := s.ListenAndServe(); err != nil {
			log.Fatalf("server error: %s", err)
		}
	}()

	<-done
	log.Println("Stopping SSH server")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		log.Fatalf("could not stop server: %s", err)
	}
}
