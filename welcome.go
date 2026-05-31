package main

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// welcomeScreen is the first thing a user sees after connecting. It just
// shows a banner and waits for any key to move on.
//
// Like the other screens, it's a plain Go struct with three methods
// (Init/Update/View) so it satisfies the `Screen` interface from root.go.
type welcomeScreen struct {
	width  int
	height int
}

// Constructor. Returns a zero-valued struct — Go gives every type a useful
// "zero value" by default (0 for ints, "" for strings, nil for slices/maps/
// pointers), so we don't have to spell out the fields.
func newWelcomeScreen() welcomeScreen {
	return welcomeScreen{}
}

func (s welcomeScreen) title() string { return "welcome" }

// Init runs once when the screen becomes active. We have nothing to do at
// startup, so we return `nil` — the Bubble Tea convention for "no command".
func (s welcomeScreen) Init() tea.Cmd { return nil }

// Update is called for every incoming message. We mutate a local copy of
// the screen (`s` is a value receiver) and return it; the root model swaps
// the new value in for the old one.
func (s welcomeScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		// Bubble Tea sends a WindowSizeMsg whenever the terminal is resized
		// (and once on startup). We stash the dimensions for View to use.
		s.width = m.Width
		s.height = m.Height
	case tea.KeyPressMsg:
		// `_ = m` silences the "declared but not used" error: Go won't let
		// you leave variables unused, but here we just care that a key was
		// pressed — not which one.
		_ = m
		// Returning a `tea.Cmd` (a function that returns a tea.Msg) is how
		// you ask the runtime to deliver a message later. Here we emit a
		// navigation message; the root model picks it up and swaps screens.
		return s, func() tea.Msg { return ShowDirectoryMsg{} }
	}
	return s, nil
}

// View renders the screen to a string. No state is mutated here — View is
// called by the runtime whenever a redraw is needed, possibly many times
// per second, so it must be cheap and side-effect-free.
func (s welcomeScreen) View() string {
	// Lipgloss styles are *immutable, chainable* values: each `.Border(...)`,
	// `.Padding(...)`, etc. returns a new Style. The final `.Render(text)`
	// turns the style + text into an ANSI-coloured string.
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

	// On the very first frame we might not have a window size yet (the
	// WindowSizeMsg hasn't been delivered). In that case just return the
	// box unpositioned. Otherwise centre it in the terminal.
	if s.width > 0 {
		return lipgloss.PlaceHorizontal(s.width, lipgloss.Center,
			lipgloss.PlaceVertical(s.height, lipgloss.Center, box))
	}
	return box
}
