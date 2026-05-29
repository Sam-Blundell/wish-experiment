package main

import "charm.land/lipgloss/v2"

// Lipgloss is Charm's styling library — think CSS for terminal output. You
// build a `Style` value and call `Render(text)` to get an ANSI-coloured
// string. The colours below are just named constants we reuse from every
// screen so the palette stays consistent.
//
// `lipgloss.Color("214")` refers to a 256-colour terminal palette index, not
// a hex code. The comment beside each gives the rough hex equivalent.
//
// `var ( ... )` is a grouped variable declaration — the package-level
// equivalent of the `const` block in main.go. These are evaluated once when
// the package is loaded.

// CRT amber palette. Approximates the warm phosphor glow of an old amber
// monitor: a bright amber for highlights, a softer cream for body text, and
// a dim amber for chrome and secondary information.
var (
	colorAmber    = lipgloss.Color("214") // #ffaf00 — primary highlight
	colorCream    = lipgloss.Color("222") // #ffd787 — body text
	colorAmberDim = lipgloss.Color("130") // #af5f00 — separators, hints
)
