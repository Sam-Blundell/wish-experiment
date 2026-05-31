package main

import (
	"fmt"
	"math/rand"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/ssh"
)

// ----------------------------------------------------------------------------
// game: a tiny Zelda-style multiplayer prototype.
//
// The map is shared (one world, all sessions see it) and each player has a
// position. The architecture mirrors chat.go:
//
//   theGame     — process-wide game state (mutex-guarded), one per program.
//   gamePlayer  — per-session state (position, send channel) registered with
//                 theGame on join, removed on leave/disconnect.
//
// When anyone moves, theGame snapshots all positions and sends the snapshot
// to every player's channel. Each session's Update receives the snapshot
// and re-renders. Nothing in the world mutates except player positions, so
// the map data itself can be read without locking.
// ----------------------------------------------------------------------------

const (
	gameMinWidth   = 80
	gameMinHeight  = 24
	worldWidth     = 120
	worldHeight    = 60
	gameMaxPlayers = 20
)

type tile rune

const (
	tileGrass tile = '.'
	tileTree  tile = 'T'
	tileWater tile = '~'
)

func (t tile) walkable() bool {
	return t != tileTree && t != tileWater
}

// world is the shared map data. It's generated once at startup (because
// it's assigned at package level) and never mutated, so any goroutine can
// read from it without locking.
var world = generateWorld()

func generateWorld() [][]tile {
	r := rand.New(rand.NewSource(42))
	w := make([][]tile, worldHeight)
	for y := range w {
		w[y] = make([]tile, worldWidth)
		for x := range w[y] {
			w[y][x] = tileGrass
		}
	}
	for x := 0; x < worldWidth; x++ {
		w[0][x] = tileTree
		w[worldHeight-1][x] = tileTree
	}
	for y := 0; y < worldHeight; y++ {
		w[y][0] = tileTree
		w[y][worldWidth-1] = tileTree
	}
	for i := 0; i < 250; i++ {
		x := r.Intn(worldWidth-2) + 1
		y := r.Intn(worldHeight-2) + 1
		w[y][x] = tileTree
	}
	carveLake(w, worldWidth/3, worldHeight/3, 4, 4)
	carveLake(w, 3*worldWidth/4, 2*worldHeight/3, 5, 5)
	return w
}

func carveLake(w [][]tile, cx, cy, rx, ry int) {
	for y := 1; y < worldHeight-1; y++ {
		for x := 1; x < worldWidth-1; x++ {
			dx := float64(x - cx)
			dy := float64(y - cy)
			if dx*dx/float64(rx*rx)+dy*dy/float64(ry*ry) < 1 {
				w[y][x] = tileWater
			}
		}
	}
}

// gamePlayer is the per-session state. We use *gamePlayer (pointer values)
// as identity throughout — two players with the same nick are still
// distinct pointers, so they don't clash as map keys.
type gamePlayer struct {
	send chan gameSnapshot
	ip   string
	nick string
	x, y int
}

func (p *gamePlayer) displayName() string {
	if p.nick != "" {
		return p.nick
	}
	return p.ip
}

// gamePlayerInfo is a frozen copy of a player's public state at a moment
// in time. The broadcast snapshots build these so receivers don't hold
// references to *gamePlayer fields, which the hub mutates under its lock.
type gamePlayerInfo struct {
	name string
	x, y int
}

// gameSnapshot is the message broadcast to every player after each state
// change. It maps each player (by identity) to their info. Each snapshot
// is a freshly-allocated map — receivers can read it without locking
// because no one else has a reference to mutate it.
type gameSnapshot map[*gamePlayer]gamePlayerInfo

// game is the shared room. Same shape as chat's `room`: a mutex guarding
// the set of players. The world itself is the package-level `world` var
// and isn't part of this struct because it's read-only.
type game struct {
	mu      sync.Mutex
	players map[*gamePlayer]struct{}
}

var theGame = &game{
	players: make(map[*gamePlayer]struct{}),
}

func (g *game) join(p *gamePlayer) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.players) >= gameMaxPlayers {
		return false
	}
	if !g.placeSpawn(p) {
		return false
	}
	g.players[p] = struct{}{}
	g.broadcast()
	return true
}

// placeSpawn finds an unoccupied walkable tile starting near the world
// centre and expanding outward, then sets it on p. Caller holds g.mu.
func (g *game) placeSpawn(p *gamePlayer) bool {
	// Build a quick lookup of currently-occupied tiles. `[2]int` is a
	// fixed-size array (not a slice), and arrays are comparable in Go,
	// which makes them valid map keys — useful for "set of coordinates".
	occupied := make(map[[2]int]bool, len(g.players))
	for other := range g.players {
		occupied[[2]int{other.x, other.y}] = true
	}
	cx, cy := worldWidth/2, worldHeight/2
	for r := 0; r < worldWidth; r++ {
		for y := cy - r; y <= cy+r; y++ {
			for x := cx - r; x <= cx+r; x++ {
				if y < 0 || y >= worldHeight || x < 0 || x >= worldWidth {
					continue
				}
				if !world[y][x].walkable() {
					continue
				}
				if occupied[[2]int{x, y}] {
					continue
				}
				p.x, p.y = x, y
				return true
			}
		}
	}
	return false
}

// leave is idempotent — safe to call twice (manual exit + disconnect both
// fire). Same pattern as chat's room.leave.
func (g *game) leave(p *gamePlayer) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.players[p]; !ok {
		return
	}
	delete(g.players, p)
	g.broadcast()
}

// move validates the requested step and applies it if legal. Players
// can't walk through tiles that aren't walkable or onto another player.
func (g *game) move(p *gamePlayer, dx, dy int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.players[p]; !ok {
		return
	}
	nx, ny := p.x+dx, p.y+dy
	if nx < 0 || nx >= worldWidth || ny < 0 || ny >= worldHeight {
		return
	}
	if !world[ny][nx].walkable() {
		return
	}
	for other := range g.players {
		if other != p && other.x == nx && other.y == ny {
			return
		}
	}
	p.x, p.y = nx, ny
	g.broadcast()
}

func (g *game) snapshot() gameSnapshot {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.buildSnapshot()
}

// buildSnapshot constructs a fresh snapshot. Caller holds g.mu.
func (g *game) buildSnapshot() gameSnapshot {
	snap := make(gameSnapshot, len(g.players))
	for p := range g.players {
		snap[p] = gamePlayerInfo{name: p.displayName(), x: p.x, y: p.y}
	}
	return snap
}

// broadcast snapshots state and pushes it to every player's send channel.
// Caller holds g.mu. Non-blocking on a full channel — we drop rather than
// stall the whole hub on one slow client. The next successful send carries
// the latest state anyway, so a dropped snapshot is not a lost update.
func (g *game) broadcast() {
	snap := g.buildSnapshot()
	for p := range g.players {
		select {
		case p.send <- snap:
		default:
		}
	}
}

// gameSnapshotMsg is the Bubble Tea message type that delivers a snapshot
// into a screen's Update. Defining `type X gameSnapshot` creates a new
// distinct type with the same layout — needed so the type switch can tell
// our messages apart from any other tea.Msg.
type gameSnapshotMsg gameSnapshot

func gameWaitForSnap(sub chan gameSnapshot) tea.Cmd {
	return func() tea.Msg {
		return gameSnapshotMsg(<-sub)
	}
}

type gameScreen struct {
	width    int
	height   int
	player   *gamePlayer
	snapshot gameSnapshot
}

func newGameScreen(s ssh.Session, ip string, width, height int) Screen {
	p := &gamePlayer{
		// Buffered so a brief stall in the receiver doesn't block the
		// broadcast goroutine. If the buffer fills we drop snapshots —
		// fine, since the next one carries the latest state.
		send: make(chan gameSnapshot, 32),
		ip:   ip,
		nick: getNick(ip),
	}
	if !theGame.join(p) {
		return newFullScreen(width, height)
	}
	// Leave the game if the SSH session ends. Normal exit (esc) also
	// calls leave; leave is idempotent so the double-call is safe.
	go func() {
		<-s.Context().Done()
		theGame.leave(p)
	}()
	return gameScreen{
		width:    width,
		height:   height,
		player:   p,
		snapshot: theGame.snapshot(),
	}
}

func (m gameScreen) Init() tea.Cmd {
	return gameWaitForSnap(m.player.send)
}

func (m gameScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case gameSnapshotMsg:
		// Adopt the new state and re-arm the receiver so the *next*
		// snapshot also reaches us.
		m.snapshot = gameSnapshot(msg)
		return m, gameWaitForSnap(m.player.send)
	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc", "q":
			theGame.leave(m.player)
			return m, func() tea.Msg { return ShowDirectoryMsg{} }
		case "up", "k", "w":
			theGame.move(m.player, 0, -1)
		case "down", "j", "s":
			theGame.move(m.player, 0, 1)
		case "left", "h", "a":
			theGame.move(m.player, -1, 0)
		case "right", "l", "d":
			theGame.move(m.player, 1, 0)
		}
	}
	return m, nil
}

func (m gameScreen) View() string {
	if m.width < gameMinWidth || m.height < gameMinHeight {
		msg := lipgloss.NewStyle().Foreground(colorAmberDim).Align(lipgloss.Center).Render(
			"this game needs at least 80×24\n" +
				fmt.Sprintf("your terminal is %d×%d\n\n", m.width, m.height) +
				"resize and try again · esc to go back",
		)
		if m.width > 0 {
			return lipgloss.PlaceHorizontal(m.width, lipgloss.Center,
				lipgloss.PlaceVertical(m.height, lipgloss.Center, msg))
		}
		return msg
	}

	// We read our own position from the snapshot, not from m.player.x/y,
	// because the player fields are mutated by the hub under its lock —
	// reading them outside the lock would be a data race. The snapshot is
	// a captured copy, so it's safe to read.
	me := m.snapshot[m.player]

	const statusH, helpH = 1, 1
	viewportH := m.height - statusH - helpH
	viewportTilesW := m.width / 2
	viewportTilesH := viewportH

	// Camera centred on the local player, clamped to the world. Far-edge
	// clamps come first so the >=0 clamp wins when the viewport is bigger
	// than the world (see game.go's earlier panic for the cautionary tale).
	camX := me.x - viewportTilesW/2
	camY := me.y - viewportTilesH/2
	if camX > worldWidth-viewportTilesW {
		camX = worldWidth - viewportTilesW
	}
	if camY > worldHeight-viewportTilesH {
		camY = worldHeight - viewportTilesH
	}
	if camX < 0 {
		camX = 0
	}
	if camY < 0 {
		camY = 0
	}

	grassStyle := lipgloss.NewStyle().Foreground(colorAmberDim)
	treeStyle := lipgloss.NewStyle().Foreground(colorCream)
	waterStyle := lipgloss.NewStyle().Foreground(colorAmberDim).Faint(true)
	selfStyle := lipgloss.NewStyle().Foreground(colorAmber).Bold(true)
	otherStyle := lipgloss.NewStyle().Foreground(colorCream).Bold(true)

	// Index other players by world coord so the per-tile loop below is an
	// O(1) lookup instead of a linear scan through the snapshot.
	others := make(map[[2]int]gamePlayerInfo, len(m.snapshot))
	for p, info := range m.snapshot {
		if p == m.player {
			continue
		}
		others[[2]int{info.x, info.y}] = info
	}

	rows := make([]string, viewportTilesH)
	for y := 0; y < viewportTilesH; y++ {
		var row strings.Builder
		for x := 0; x < viewportTilesW; x++ {
			wx, wy := camX+x, camY+y
			if wx < 0 || wx >= worldWidth || wy < 0 || wy >= worldHeight {
				row.WriteString("  ")
				continue
			}
			if wx == me.x && wy == me.y {
				row.WriteString(selfStyle.Render("@ "))
				continue
			}
			if _, isOther := others[[2]int{wx, wy}]; isOther {
				row.WriteString(otherStyle.Render("@ "))
				continue
			}
			switch world[wy][wx] {
			case tileTree:
				row.WriteString(treeStyle.Render("T "))
			case tileWater:
				row.WriteString(waterStyle.Render("~~"))
			default:
				row.WriteString(grassStyle.Render(". "))
			}
		}
		rows[y] = row.String()
	}

	statusLine := lipgloss.NewStyle().Foreground(colorAmber).Bold(true).Render("game test") +
		lipgloss.NewStyle().Foreground(colorAmberDim).Render(
			fmt.Sprintf("  %d players · pos (%d, %d)", len(m.snapshot), me.x, me.y),
		)
	help := lipgloss.NewStyle().Foreground(colorAmberDim).
		Render("arrows / wasd / hjkl to move · esc to leave")

	out := make([]string, 0, viewportTilesH+2)
	out = append(out, statusLine)
	out = append(out, rows...)
	out = append(out, help)
	return strings.Join(out, "\n")
}
