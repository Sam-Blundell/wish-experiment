package main

import (
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/ssh"
)

// ----------------------------------------------------------------------------
// Bubble Tea, briefly
//
// Bubble Tea uses the Elm Architecture. Every TUI is a value (the "model")
// with three methods:
//
//   Init()   returns an initial command (something to do at startup).
//   Update() takes a message and returns a new model plus an optional command.
//   View()   renders the model to a string.
//
// Messages flow in from the runtime (key presses, window resizes, ticks, or
// anything you send yourself). Update is the only place state changes. View
// is pure — it just turns state into a string. That's the whole loop.
//
// `tea.Model` is Bubble Tea's interface for "a thing that has Init/Update/
// View". Below we define our own narrower interface, `Screen`, for the
// individual sub-apps (welcome, directory, chat, about) that the root model
// swaps between.
// ----------------------------------------------------------------------------

// Screen is the contract every sub-app implements. In Go, an interface is
// just a set of method signatures — any type that has these methods
// automatically satisfies the interface. There's no `implements` keyword;
// the relationship is implicit (often called "structural typing").
//
// Note the Update signature: it returns a `Screen`, not a `tea.Model`. We
// narrow the type here so the root knows it's still dealing with one of our
// screens after each update.
type Screen interface {
	Init() tea.Cmd
	Update(msg tea.Msg) (Screen, tea.Cmd)
	View() string
	// title returns the SSH client's window title for this screen. We pass
	// it up via the root model's View, which is the only place Bubble Tea
	// reads window-title config from.
	title() string
}

// These three are *navigation messages*. A screen can return a `tea.Cmd`
// that produces one of these, and the root's Update will see it and swap
// to the matching screen. They're just empty structs because they carry
// no data — the type itself is the signal.
//
// `struct{}` is Go's empty struct: zero bytes, useful purely as a marker.
type ShowDirectoryMsg struct{}
type EnterChatMsg struct{}
type EnterAboutMsg struct{}
type EnterGameMsg struct{}

// rootModel is the top-level Bubble Tea model. It holds whichever Screen
// is currently active plus some shared session state, and forwards each
// message to the active screen.
//
// Fields starting with a lower-case letter are *unexported* (package-
// private). Upper-case would make them visible to other packages. Same
// rule applies to types, functions, methods — capitalisation controls
// visibility in Go.
type rootModel struct {
	active  Screen
	session ssh.Session
	ip      string
	width   int
	height  int
}

// `newRoot` is a constructor function. Go has no constructors as a language
// feature — by convention you write a `newFoo` function that returns a Foo.
func newRoot(s ssh.Session, ip string) rootModel {
	return rootModel{
		session: s,
		ip:      ip,
		active:  newWelcomeScreen(),
	}
}

// Methods in Go are functions with a *receiver* — the `(m rootModel)` before
// the name. This attaches Init to the rootModel type. `m` is a value copy of
// the receiver; if we wanted to mutate it in place we'd use `*rootModel`
// (a pointer receiver). Bubble Tea models conventionally use value receivers
// and return the modified copy from Update.
func (m rootModel) Init() tea.Cmd {
	return m.active.Init()
}

func (m rootModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// `msg.(tea.KeyPressMsg)` is a *type assertion*: it asks "is this msg
	// actually a KeyPressMsg?" and returns the value plus an `ok` bool.
	// The `if ... ; ok { ... }` form is a Go idiom — declare a variable in
	// the if-statement's init clause and use it only inside the block.
	//
	// ctrl+c closes the SSH session from anywhere in the app.
	if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == "ctrl+c" {
		return m, tea.Quit
	}

	// A *type switch* — like the type assertion above but for many types at
	// once. Each `case` binds `msg` to the concrete type inside that branch.
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
	case EnterGameMsg:
		m.active = newGameScreen(m.session, m.ip, m.width, m.height)
		return m, m.active.Init()
	}

	// For anything we didn't handle here, forward the message to the active
	// screen. This is the bit that makes the screens work: per-screen logic
	// lives in their Update; this one just routes.
	active, cmd := m.active.Update(msg)
	m.active = active
	return m, cmd
}

// In Bubble Tea v2, View returns a `tea.View` (not just a string) so you
// can configure things like alt-screen mode and window title alongside the
// rendered output. `AltScreen = true` makes the program take over the whole
// terminal and restore it on exit — the standard fullscreen-app behaviour.
//
// `Cursor = nil` hides the terminal cursor. Bubble Tea v2 shows a cursor
// by default; we explicitly hide it because none of our screens use it
// (chat draws its own block cursor inside the input). Individual screens
// can override this by including a `tea.Cursor` in their own state and
// surfacing it — but at present nobody does.
func (m rootModel) View() tea.View {
	v := tea.NewView(m.active.View())
	v.AltScreen = true
	v.Cursor = nil
	v.WindowTitle = "wish · " + m.active.title()
	return v
}
