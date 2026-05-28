package main

import "charm.land/lipgloss/v2"

// CRT amber palette. Approximates the warm phosphor glow of an old amber
// monitor: a bright amber for highlights, a softer cream for body text, and
// a dim amber for chrome and secondary information.
var (
	colorAmber    = lipgloss.Color("214") // #ffaf00 — primary highlight
	colorCream    = lipgloss.Color("222") // #ffd787 — body text
	colorAmberDim = lipgloss.Color("130") // #af5f00 — separators, hints
)
