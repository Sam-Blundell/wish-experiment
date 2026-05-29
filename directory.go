package main

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// directoryScreen is the main menu — a vertical list the user can navigate
// with arrow keys or vi-style j/k.
//
// `options []string` is a *slice*: a dynamically-sized, ordered sequence
// of strings. Slices are one of Go's main collection types; the others
// are arrays (fixed size, rare in practice) and maps (key-value).
type directoryScreen struct {
	width    int
	height   int
	selected int
	options  []string
}

func newDirectoryScreen(width, height int) directoryScreen {
	return directoryScreen{
		width:   width,
		height:  height,
		// A slice literal — like a list/array in most languages.
		options: []string{"testchat", "about", "exit"},
	}
}

func (s directoryScreen) Init() tea.Cmd { return nil }

func (s directoryScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		s.width = m.Width
		s.height = m.Height
	case tea.KeyPressMsg:
		// `m.String()` is a Bubble Tea helper that turns a key event into
		// a canonical string ("up", "enter", "ctrl+c", "a", etc.) — much
		// easier than inspecting the raw key fields.
		switch m.String() {
		case "up", "k":
			if s.selected > 0 {
				s.selected--
			}
		case "down", "j":
			// `len(s.options)` works on slices, arrays, strings, maps and
			// channels — it's a built-in, not a method.
			if s.selected < len(s.options)-1 {
				s.selected++
			}
		case "enter":
			switch s.options[s.selected] {
			case "testchat":
				return s, func() tea.Msg { return EnterChatMsg{} }
			case "about":
				return s, func() tea.Msg { return EnterAboutMsg{} }
			case "exit":
				// `tea.Quit` is Bubble Tea's built-in command that ends
				// the program cleanly.
				return s, tea.Quit
			}
		case "q", "esc":
			return s, tea.Quit
		}
	}
	return s, nil
}

func (s directoryScreen) View() string {
	// Define the styles we'll use. These are cheap to create; you could
	// hoist them to package level if you wanted to avoid the per-render
	// allocations, but at this scale it doesn't matter.
	selectedStyle := lipgloss.NewStyle().Foreground(colorAmber).Bold(true)
	normalStyle := lipgloss.NewStyle().Foreground(colorCream)
	helpStyle := lipgloss.NewStyle().Foreground(colorAmberDim)
	titleStyle := lipgloss.NewStyle().Foreground(colorAmber).Bold(true)

	// Pad options to the widest one so the marker stays in a stable position
	// no matter which option is selected.
	//
	// `range` over a slice yields (index, value). We don't need the index
	// here so we use `_` to discard it.
	maxOpt := 0
	for _, opt := range s.options {
		if w := lipgloss.Width(opt); w > maxOpt {
			maxOpt = w
		}
	}

	// `var menu []string` declares a nil slice. Calling `append` on a nil
	// slice is fine in Go — it just allocates the first backing array.
	var menu []string
	for i, opt := range s.options {
		// `strings.Repeat` is a standard-library helper. `lipgloss.Width`
		// measures *visible* width, which differs from `len(opt)` when the
		// string contains multi-byte runes or ANSI escape codes.
		padded := opt + strings.Repeat(" ", maxOpt-lipgloss.Width(opt))
		if i == s.selected {
			menu = append(menu, selectedStyle.Render("► "+padded))
		} else {
			menu = append(menu, normalStyle.Render("  "+padded))
		}
	}

	help := "↑↓ to move · enter to select · q to quit"

	contentWidth := lipgloss.Width(help)
	if w := maxOpt + 2; w > contentWidth {
		contentWidth = w
	}
	center := lipgloss.NewStyle().Width(contentWidth).Align(lipgloss.Center)

	// `strings.Join` glues a slice together with a separator — equivalent
	// to "\n".join(...) in other languages.
	inner := strings.Join([]string{
		center.Render(titleStyle.Render("Directory")),
		"",
		center.Render(strings.Join(menu, "\n")),
		"",
		center.Render(helpStyle.Render(help)),
	}, "\n")

	box := lipgloss.NewStyle().
		Border(lipgloss.DoubleBorder()).
		BorderForeground(colorAmber).
		Padding(1, 4).
		Render(inner)

	if s.width > 0 {
		return lipgloss.PlaceHorizontal(s.width, lipgloss.Center,
			lipgloss.PlaceVertical(s.height, lipgloss.Center, box))
	}
	return box
}
