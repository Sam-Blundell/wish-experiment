package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
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

type chatMsg struct {
	from string
	text string
}

type room struct {
	mu       sync.Mutex
	messages []chatMsg
	clients  map[*client]struct{}
}

type client struct {
	send chan chatMsg
	ip   string
	nick string
}

func (c *client) displayName() string {
	if c.nick != "" {
		return c.nick
	}
	return c.ip
}

var (
	chatRoom = &room{
		clients: make(map[*client]struct{}),
	}
	nicksMu sync.Mutex
	nicks   = make(map[string]string)
)

func getNick(ip string) string {
	nicksMu.Lock()
	defer nicksMu.Unlock()
	return nicks[ip]
}

func setNick(ip, nick string) {
	nicksMu.Lock()
	defer nicksMu.Unlock()
	nicks[ip] = nick
}

const maxClients = 20

func (r *room) join(c *client) (bool, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.clients) >= maxClients {
		return false, len(r.clients)
	}
	r.broadcast(chatMsg{from: "", text: c.displayName() + " joined"})
	r.clients[c] = struct{}{}
	return true, len(r.clients)
}

func (r *room) leave(c *client) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.clients, c)
	r.broadcast(chatMsg{from: "", text: c.displayName() + " left"})
}

func (r *room) send(msg chatMsg) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.broadcast(msg)
}

const maxMessages = 500

func (r *room) broadcast(msg chatMsg) {
	r.messages = append(r.messages, msg)
	if len(r.messages) > maxMessages {
		r.messages = r.messages[len(r.messages)-maxMessages:]
	}
	for c := range r.clients {
		select {
		case c.send <- msg:
		default:
		}
	}
}

func (r *room) history() []chatMsg {
	r.mu.Lock()
	defer r.mu.Unlock()
	h := make([]chatMsg, len(r.messages))
	copy(h, r.messages)
	return h
}

type fullModel struct{}

func (m fullModel) Init() tea.Cmd                           { return nil }
func (m fullModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) { return m, tea.Quit }
func (m fullModel) View() string {
	return lipgloss.NewStyle().Padding(1, 2).Foreground(lipgloss.Color("241")).Render("Room is full. Try again later.")
}

type incomingMsg chatMsg

type model struct {
	width      int
	height     int
	input      string
	messages   []chatMsg
	client     *client
	sub        chan chatMsg
	showModal  bool
	renderer   *lipgloss.Renderer
}

func waitForMsg(sub chan chatMsg) tea.Cmd {
	return func() tea.Msg {
		return incomingMsg(<-sub)
	}
}

func (m model) Init() tea.Cmd {
	return waitForMsg(m.sub)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case incomingMsg:
		m.messages = append(m.messages, chatMsg(msg))
		return m, waitForMsg(m.sub)

	case tea.KeyMsg:
		if m.showModal {
			m.showModal = false
			return m, nil
		}
		switch msg.Type {
		case tea.KeyCtrlC:
			return m, tea.Quit
		case tea.KeyEnter:
			text := strings.TrimSpace(m.input)
			if text != "" {
				if strings.HasPrefix(text, "/nick ") {
					newNick := strings.TrimSpace(strings.TrimPrefix(text, "/nick "))
					if newNick != "" {
						oldName := m.client.displayName()
						m.client.nick = newNick
						setNick(m.client.ip, newNick)
						chatRoom.send(chatMsg{from: "", text: oldName + " is now " + newNick})
					}
				} else {
					chatRoom.send(chatMsg{from: m.client.displayName(), text: text})
				}
			}
			m.input = ""
		case tea.KeyBackspace:
			if len(m.input) > 0 {
				m.input = m.input[:len(m.input)-1]
			}
		default:
			if msg.Type == tea.KeyRunes {
				m.input += string(msg.Runes)
			} else if msg.Type == tea.KeySpace {
				m.input += " "
			}
		}
	}
	return m, nil
}

func (m model) View() string {
	dim := lipgloss.Color("241")
	purple := lipgloss.Color("212")
	white := lipgloss.Color("252")

	chatWidth := m.width - 4
	if chatWidth < 20 {
		chatWidth = 20
	}

	r := m.renderer

	inputStyle := r.NewStyle().
		Foreground(white)

	promptStyle := r.NewStyle().
		Foreground(purple).
		Bold(true)

	systemStyle := r.NewStyle().
		Foreground(dim).
		Italic(true)

	senderStyle := r.NewStyle().
		Foreground(purple).
		Bold(true)

	msgStyle := r.NewStyle().
		Foreground(white)

	helpStyle := r.NewStyle().
		Foreground(dim)

	// Render messages
	chatHeight := m.height - 5
	if chatHeight < 1 {
		chatHeight = 1
	}

	var lines []string
	for _, msg := range m.messages {
		var line string
		if msg.from == "" {
			line = systemStyle.Render("* " + msg.text)
		} else {
			line = senderStyle.Render(msg.from+": ") + msgStyle.Render(msg.text)
		}
		lines = append(lines, line)
	}

	// Show only the most recent messages that fit
	if len(lines) > chatHeight {
		lines = lines[len(lines)-chatHeight:]
	}

	// Pad with empty lines if needed
	for len(lines) < chatHeight {
		lines = append([]string{""}, lines...)
	}

	chat := strings.Join(lines, "\n")

	separator := r.NewStyle().
		Foreground(dim).
		Render(strings.Repeat("─", chatWidth))

	input := promptStyle.Render("> ") + inputStyle.Render(m.input+"█")

	help := helpStyle.Render("/nick <name> to set name · ctrl+c to quit")

	screen := r.NewStyle().Padding(0, 2).Render(
		chat + "\n" +
			separator + "\n" +
			input + "\n" + help,
	)

	if !m.showModal {
		return screen
	}

	modalStyle := r.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(purple).
		Padding(1, 3).
		Foreground(white).
		Bold(true).
		Align(lipgloss.Center)

	modalText := "Messages here are public and\nvisible to anyone who joins.\n\n" +
		r.NewStyle().Foreground(dim).Render("press any key to continue")

	modal := modalStyle.Render(modalText)

	if m.width > 0 {
		return r.PlaceHorizontal(m.width, lipgloss.Center,
			r.PlaceVertical(m.height, lipgloss.Center, modal))
	}
	return modal
}

func main() {
	s, err := wish.NewServer(
		wish.WithAddress(fmt.Sprintf("%s:%d", host, port)),
		wish.WithHostKeyPath(".ssh/id_ed25519"),
		wish.WithMiddleware(
			bubbletea.Middleware(func(s ssh.Session) (tea.Model, []tea.ProgramOption) {
				ip, _, _ := net.SplitHostPort(s.RemoteAddr().String())

				c := &client{
					send: make(chan chatMsg, 100),
					ip:   ip,
					nick: getNick(ip),
				}

				ok, _ := chatRoom.join(c)
				if !ok {
					return fullModel{}, []tea.ProgramOption{tea.WithAltScreen()}
				}

				go func() {
					<-s.Context().Done()
					chatRoom.leave(c)
				}()

				renderer := bubbletea.MakeRenderer(s)
				m := model{
					messages:  chatRoom.history(),
					client:    c,
					sub:       c.send,
					showModal: true,
					renderer:  renderer,
				}

				return m, []tea.ProgramOption{tea.WithAltScreen()}
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
