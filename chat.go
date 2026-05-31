package main

import (
	"strings"
	"sync"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/ssh"
)

// ----------------------------------------------------------------------------
// Chat: shared backend + per-session screen
//
// Unlike the other screens, chat has two halves:
//
//   1. A single, process-wide `room` value (declared at package level below)
//      that holds the message history and the set of connected clients.
//   2. A `chatScreen` per SSH session — the UI for one user.
//
// Multiple goroutines touch the room concurrently (one per session, plus the
// goroutine that watches for disconnects), so it's protected by a mutex.
// Messages flow from the room into each screen through a channel.
// ----------------------------------------------------------------------------

// chatMsg is a single line of chat. `from == ""` means a system message
// (joins, leaves, nick changes).
type chatMsg struct {
	from string
	text string
}

// room is the shared state for the chatroom. Every field after `mu` is
// protected by it — anything that reads or writes them must hold the lock.
//
// `sync.Mutex` is Go's basic lock. The convention is to put the mutex right
// next to the fields it guards so it's obvious what it covers.
type room struct {
	mu       sync.Mutex
	messages []chatMsg
	// `map[*client]struct{}` is Go's idiomatic "set": a map whose values
	// are empty structs (zero bytes). We only care about the keys.
	clients map[*client]struct{}
}

// client represents one connected user. `send` is the channel the room
// uses to push new messages at the client's screen.
type client struct {
	send chan chatMsg
	ip   string
	nick string
}

// Methods on `*client` (pointer receiver) operate on the original value
// rather than a copy. We use pointers here because we want all goroutines
// to see updates to `nick`.
func (c *client) displayName() string {
	if c.nick != "" {
		return c.nick
	}
	return c.ip
}

// Package-level state. `var ( ... )` mirrors the const block — these are
// initialised once when the program starts.
//
//   chatRoom — the single global room. `&room{...}` takes the address of
//              a struct literal, giving us a `*room`.
//   nicksMu  — guards the `nicks` map. Separate from the room mutex so the
//              two don't contend.
//   nicks    — remembers a chosen nick across reconnects, keyed by IP.
var (
	chatRoom = &room{
		clients: make(map[*client]struct{}),
	}
	nicksMu sync.Mutex
	nicks   = make(map[string]string)
)

func getNick(ip string) string {
	nicksMu.Lock()
	// `defer` schedules a call to run when the function returns. Using it
	// for Unlock is the standard Go pattern — it guarantees the lock is
	// released even if the function panics or returns early.
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

// join adds a client to the room. Returns whether it succeeded plus the
// resulting client count. Go lets functions return multiple values
// natively, no need for a wrapper struct or out-params.
func (r *room) join(c *client) (bool, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.clients) >= maxClients {
		return false, len(r.clients)
	}
	r.broadcast(chatMsg{from: "", text: c.displayName() + " joined"})
	// Setting a map key to the empty-struct value is how you "add to a set".
	r.clients[c] = struct{}{}
	return true, len(r.clients)
}

// leave is idempotent — calling it twice for the same client is a no-op.
// This matters because we leave on both manual exit (esc / /exit) and on
// session disconnect, and those can both fire for the same client.
func (r *room) leave(c *client) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// `value, ok := map[key]` is the comma-ok idiom for map lookups: `ok`
	// tells you whether the key was actually present. Without it you can't
	// distinguish "missing" from "present with zero value".
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

// broadcast appends to history and fans out to every connected client.
// Callers must already hold r.mu — note there's no Lock/Unlock here. This
// is a common Go pattern: an unexported helper that assumes the lock, used
// by exported methods that take it.
func (r *room) broadcast(msg chatMsg) {
	r.messages = append(r.messages, msg)
	if len(r.messages) > maxMessages {
		// Slice expression: keep only the most recent `maxMessages` entries.
		r.messages = r.messages[len(r.messages)-maxMessages:]
	}
	for c := range r.clients {
		// `select` is like a switch for channel operations. Here it tries
		// to send on `c.send`; if the channel buffer is full the `default`
		// branch fires immediately, so we drop the message rather than
		// block the whole room on one slow client.
		select {
		case c.send <- msg:
		default:
		}
	}
}

// history returns a *copy* of the messages slice. Returning the original
// would let the caller see further appends (and potentially race with
// other goroutines), so we copy under the lock.
func (r *room) history() []chatMsg {
	r.mu.Lock()
	defer r.mu.Unlock()
	h := make([]chatMsg, len(r.messages))
	copy(h, r.messages)
	return h
}

// chatIncomingMsg is the Bubble Tea message type we use to deliver a new
// chat line into our Update. Defining `type X chatMsg` creates a new
// distinct type with the same underlying layout — handy because the type
// switch in Update needs to tell our messages apart from other tea.Msgs.
type chatIncomingMsg chatMsg

type chatScreen struct {
	width    int
	height   int
	input    textarea.Model
	messages []chatMsg
	client   *client
	sub      chan chatMsg
	showHelp bool
}

// newChatScreen is also where we hook up the per-session goroutine that
// watches for disconnects. Returns a `Screen` (the interface) rather than
// a concrete type so we can also return a `fullScreen` from here when the
// room is at capacity.
func newChatScreen(s ssh.Session, ip string, width, height int) Screen {
	c := &client{
		// Buffered channel: holds up to 100 messages before sends block
		// (which is when `broadcast` will drop them via the select-default
		// above).
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
	//
	// `s.Context().Done()` returns a channel that's closed when the SSH
	// session ends. Receiving from a closed channel returns immediately,
	// so this goroutine blocks until disconnect, then runs the cleanup.
	go func() {
		<-s.Context().Done()
		chatRoom.leave(c)
	}()

	// Set up the textarea widget from bubbles. Multi-line, soft-wrapping,
	// dynamically grows up to 5 rows as the user types — beyond that it
	// scrolls internally. Enter sends the message (handled in Update),
	// ctrl+j or alt+enter inserts a newline.
	//
	// Focus() has a *pointer* receiver and mutates the model in place.
	// We call it here (on the local `ta` variable, which is addressable)
	// before storing the value into the chatScreen struct, so the focus
	// state travels along with the value. Doing this in Init() instead
	// would mutate a copy that gets discarded.
	ta := textarea.New()
	ta.Prompt = "> "
	ta.ShowLineNumbers = false
	ta.DynamicHeight = true
	ta.MinHeight = 1
	ta.MaxHeight = 5
	// Rebind newline insertion off "enter" so plain enter can mean "send".
	ta.KeyMap.InsertNewline = key.NewBinding(
		key.WithKeys("ctrl+j", "alt+enter"),
		key.WithHelp("ctrl+j", "newline"),
	)
	styles := ta.Styles()
	styles.Focused.Prompt = lipgloss.NewStyle().Foreground(colorAmber).Bold(true)
	styles.Focused.Text = lipgloss.NewStyle().Foreground(colorCream)
	// Clear the default "highlight the line the cursor is on" — looks
	// out of place in a chat input.
	styles.Focused.CursorLine = lipgloss.NewStyle()
	styles.Cursor.Color = colorAmber
	ta.SetStyles(styles)
	ta.Focus()
	if width > 4 {
		ta.SetWidth(width - 4)
	}

	return chatScreen{
		width:    width,
		height:   height,
		input:    ta,
		client:   c,
		sub:      c.send,
		messages: chatRoom.history(),
	}
}

// chatWaitForMsg is the bridge between the channel-based room and Bubble
// Tea's message-based Update loop. It returns a tea.Cmd (a function) that,
// when Bubble Tea runs it, blocks on the channel and then hands the value
// back as a tea.Msg. We re-arm this after each delivery so the screen
// keeps receiving forever.
func chatWaitForMsg(sub chan chatMsg) tea.Cmd {
	return func() tea.Msg {
		return chatIncomingMsg(<-sub)
	}
}

// Init wires up the first chatWaitForMsg call. After this Bubble Tea has a
// goroutine sitting in the channel receive; every time a message arrives
// it lands in Update as a chatIncomingMsg.
func (m chatScreen) title() string { return "chat" }

func (m chatScreen) Init() tea.Cmd {
	// Batch lets us return multiple commands from one place — Bubble Tea
	// will run them concurrently. We need both the chat receiver and the
	// textarea's blink-loop kickoff. (The input itself was already focused
	// in newChatScreen — see the comment there.)
	return tea.Batch(chatWaitForMsg(m.sub), textarea.Blink)
}

func (m chatScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		if m.width > 4 {
			m.input.SetWidth(m.width - 4)
		}
	case chatIncomingMsg:
		// Append the incoming chat line and re-arm the receiver so the
		// *next* message also reaches us.
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
			// `strings.TrimSpace` strips leading/trailing whitespace. Stdlib.
			text := strings.TrimSpace(m.input.Value())
			m.input.Reset()
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
			return m, nil
		}
	}
	// Anything else (typing chars, backspace, cursor moves, paste, cursor
	// blink ticks) goes through textinput. It returns an updated Model
	// and an optional Cmd we need to forward.
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	cmds = append(cmds, cmd)
	return m, tea.Batch(cmds...)
}

func (m chatScreen) View() string {
	chatWidth := m.width - 4
	if chatWidth < 20 {
		chatWidth = 20
	}

	systemStyle := lipgloss.NewStyle().Foreground(colorAmberDim).Italic(true)
	senderStyle := lipgloss.NewStyle().Foreground(colorAmber).Bold(true)
	msgStyle := lipgloss.NewStyle().Foreground(colorCream)
	helpStyle := lipgloss.NewStyle().Foreground(colorAmberDim)

	wrap := lipgloss.NewStyle().Width(chatWidth)

	// textarea wraps soft lines and grows vertically as the user types
	// (capped by MaxHeight, configured in newChatScreen). We query its
	// current height so the chat log can shrink to make room.
	input := m.input.View()
	inputHeight := m.input.Height()

	chatHeight := m.height - 4 - inputHeight
	if chatHeight < 1 {
		chatHeight = 1
	}

	// Build the rendered chat lines. We wrap each message to the chat
	// width, then split on \n so soft-wraps count as separate lines for
	// the truncation/padding logic below.
	var lines []string
	for _, msg := range m.messages {
		var line string
		if msg.from == "" {
			line = systemStyle.Render("* " + msg.text)
		} else {
			line = senderStyle.Render(msg.from+": ") + msgStyle.Render(msg.text)
		}
		// `slice...` is *variadic spread*: it passes each element of the
		// slice as a separate argument. Here we expand the split result
		// into individual `append` args.
		lines = append(lines, strings.Split(wrap.Render(line), "\n")...)
	}

	// Keep only the most recent lines that fit, and top-pad if we have
	// fewer lines than rows available (so text grows down from the top
	// once full, but the input stays pinned to the bottom).
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
				"enter          send\n" +
				"ctrl+j         insert a newline\n" +
				"ctrl+c         disconnect\n\n" +
				lipgloss.NewStyle().Foreground(colorAmberDim).Render("press any key to close"),
		)

	// Composite the modal over the base view using v2's layer compositor.
	// This is purely a Lipgloss v2 feature — it lets you stack pre-rendered
	// strings at absolute (x, y) coordinates rather than concatenating them.
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

// fullScreen is a tiny placeholder shown when the chat room is at capacity.
// Same Screen interface as everything else; any key sends the user back to
// the directory.
type fullScreen struct {
	width  int
	height int
}

func newFullScreen(width, height int) Screen {
	return fullScreen{width: width, height: height}
}

func (s fullScreen) title() string { return "full" }

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
