package main

import (
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/ssh"
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
const maxMessages = 500

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

// leave is idempotent — calling it twice for the same client is a no-op.
// This matters because we leave on both manual exit (esc / /exit) and on
// session disconnect, and those can both fire for the same client.
func (r *room) leave(c *client) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.clients[c]; !ok {
		return
	}
	delete(r.clients, c)
	r.broadcast(chatMsg{from: "", text: c.displayName() + " left"})
}

func (r *room) send(msg chatMsg) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.broadcast(msg)
}

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

type chatIncomingMsg chatMsg

type chatScreen struct {
	width    int
	height   int
	input    string
	messages []chatMsg
	client   *client
	sub      chan chatMsg
	showHelp bool
}

func newChatScreen(s ssh.Session, ip string, width, height int) Screen {
	c := &client{
		send: make(chan chatMsg, 100),
		ip:   ip,
		nick: getNick(ip),
	}

	ok, _ := chatRoom.join(c)
	if !ok {
		return newFullScreen(width, height)
	}

	// Ensure we leave the room if the user disconnects entirely. Normal exit
	// (esc / /exit) also calls leave; that's fine because leave is idempotent.
	go func() {
		<-s.Context().Done()
		chatRoom.leave(c)
	}()

	return chatScreen{
		width:    width,
		height:   height,
		client:   c,
		sub:      c.send,
		messages: chatRoom.history(),
	}
}

func chatWaitForMsg(sub chan chatMsg) tea.Cmd {
	return func() tea.Msg {
		return chatIncomingMsg(<-sub)
	}
}

func (m chatScreen) Init() tea.Cmd {
	return chatWaitForMsg(m.sub)
}

func (m chatScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case chatIncomingMsg:
		m.messages = append(m.messages, chatMsg(msg))
		return m, chatWaitForMsg(m.sub)
	case tea.KeyPressMsg:
		// When the help modal is open, any key (other than ctrl+c, which the
		// root handles) just closes it. Messages keep arriving in the
		// background via chatIncomingMsg above.
		if m.showHelp {
			m.showHelp = false
			return m, nil
		}
		switch msg.String() {
		case "esc":
			chatRoom.leave(m.client)
			return m, func() tea.Msg { return ShowDirectoryMsg{} }
		case "enter":
			text := strings.TrimSpace(m.input)
			m.input = ""
			if text == "" {
				return m, nil
			}
			if text == "/exit" {
				chatRoom.leave(m.client)
				return m, func() tea.Msg { return ShowDirectoryMsg{} }
			}
			if text == "/help" {
				m.showHelp = true
				return m, nil
			}
			if strings.HasPrefix(text, "/nick ") {
				newNick := strings.TrimSpace(strings.TrimPrefix(text, "/nick "))
				if newNick != "" {
					oldName := m.client.displayName()
					m.client.nick = newNick
					setNick(m.client.ip, newNick)
					chatRoom.send(chatMsg{from: "", text: oldName + " is now " + newNick})
				}
				return m, nil
			}
			chatRoom.send(chatMsg{from: m.client.displayName(), text: text})
		case "backspace":
			if len(m.input) > 0 {
				m.input = m.input[:len(m.input)-1]
			}
		default:
			// Any printable text — single character or multi-rune paste — comes
			// through here. msg.Text is empty for purely non-text keys.
			if msg.Text != "" {
				m.input += msg.Text
			}
		}
	}
	return m, nil
}

func (m chatScreen) View() string {
	chatWidth := m.width - 4
	if chatWidth < 20 {
		chatWidth = 20
	}

	inputStyle := lipgloss.NewStyle().Foreground(colorCream)
	promptStyle := lipgloss.NewStyle().Foreground(colorAmber).Bold(true)
	systemStyle := lipgloss.NewStyle().Foreground(colorAmberDim).Italic(true)
	senderStyle := lipgloss.NewStyle().Foreground(colorAmber).Bold(true)
	msgStyle := lipgloss.NewStyle().Foreground(colorCream)
	helpStyle := lipgloss.NewStyle().Foreground(colorAmberDim)

	wrap := lipgloss.NewStyle().Width(chatWidth)

	// Render the input first so we can measure its height. The chat area
	// shrinks as the input grows so the whole layout still fits the terminal.
	input := wrap.Render(promptStyle.Render("> ") + inputStyle.Render(m.input+"█"))
	inputHeight := strings.Count(input, "\n") + 1

	chatHeight := m.height - 4 - inputHeight
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
		lines = append(lines, strings.Split(wrap.Render(line), "\n")...)
	}

	if len(lines) > chatHeight {
		lines = lines[len(lines)-chatHeight:]
	}
	for len(lines) < chatHeight {
		lines = append([]string{""}, lines...)
	}

	chat := strings.Join(lines, "\n")
	separator := lipgloss.NewStyle().Foreground(colorAmberDim).Render(strings.Repeat("═", chatWidth))
	help := helpStyle.Render("type /help for commands")

	base := lipgloss.NewStyle().Padding(0, 2).Render(
		chat + "\n" + separator + "\n" + input + "\n" + help,
	)

	if !m.showHelp {
		return base
	}

	modal := lipgloss.NewStyle().
		Border(lipgloss.DoubleBorder()).
		BorderForeground(colorAmber).
		Padding(1, 3).
		Foreground(colorCream).
		Render(
			lipgloss.NewStyle().Bold(true).Foreground(colorAmber).Render("Commands") + "\n\n" +
				"/nick <name>   set your display name\n" +
				"/help          show this help\n" +
				"/exit          leave the chatroom\n" +
				"esc            leave the chatroom\n" +
				"ctrl+c         disconnect\n\n" +
				lipgloss.NewStyle().Foreground(colorAmberDim).Render("press any key to close"),
		)

	// Composite the modal over the base view using v2's layer compositor.
	if m.width > 0 {
		compositor := lipgloss.NewCompositor(
			lipgloss.NewLayer(base),
			lipgloss.NewLayer(modal).
				X((m.width-lipgloss.Width(modal))/2).
				Y((m.height-lipgloss.Height(modal))/2),
		)
		return compositor.Render()
	}
	return modal
}

type fullScreen struct {
	width  int
	height int
}

func newFullScreen(width, height int) Screen {
	return fullScreen{width: width, height: height}
}

func (s fullScreen) Init() tea.Cmd { return nil }

func (s fullScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
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

func (s fullScreen) View() string {
	box := lipgloss.NewStyle().
		Padding(1, 2).
		Foreground(colorAmberDim).
		Render("Room is full.\n\npress any key to go back")

	if s.width > 0 {
		return lipgloss.PlaceHorizontal(s.width, lipgloss.Center,
			lipgloss.PlaceVertical(s.height, lipgloss.Center, box))
	}
	return box
}
