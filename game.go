package main

import (
	"fmt"
	"image/color"
	"math/rand"
	"strings"
	"sync"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/ssh"
	uv "github.com/charmbracelet/ultraviolet"
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
	bubbleDuration = 4 * time.Second
	bubbleCharCap  = 60
	nickCharCap    = 16
)

// inputMode enumerates the screen's keyboard modes. `iota` gives each
// constant a successive integer value starting at 0; defining a named
// type makes them distinct from plain ints so we can't accidentally pass
// an arbitrary number where a mode is expected.
type inputMode int

const (
	inputModeMove   inputMode = iota // arrow keys drive the player
	inputModeSpeak                   // typing into a speech bubble
	inputModeRename                  // typing a new nickname
)

// Game-only palette. The amber-monochrome theme from theme.go works for
// text screens but flattens out an environment with grass / trees / water,
// so the game uses its own colours. The 256-colour indices below correspond
// to roughly: muted green, dark green, steel blue, bright white, and a
// medium grey.
var (
	colorGrass       = lipgloss.Color("22")  // grass — deep dim green (recedes)
	colorTree        = lipgloss.Color("130") // trees — rust brown (contrasts)
	colorWater       = lipgloss.Color("67")  // water — steel blue
	colorPlayerSelf  = lipgloss.Color("15")  // your @ — white
	colorPlayerOther = lipgloss.Color("245") // other players' @ — grey
	colorNameplate   = lipgloss.Color("141") // nameplates — soft lavender (UI, not world)
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
//
// `message` and `messageExpires` track the currently-floating speech bubble.
// They live under the game's mutex with the rest of the player fields.
type gamePlayer struct {
	send           chan gameSnapshot
	ip             string
	nick           string
	x, y           int
	message        string
	messageExpires time.Time
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
	name           string
	x, y           int
	message        string
	messageExpires time.Time
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
		snap[p] = gamePlayerInfo{
			name:           p.displayName(),
			x:              p.x,
			y:              p.y,
			message:        p.message,
			messageExpires: p.messageExpires,
		}
	}
	return snap
}

// rename updates a player's nick (persists across reconnects via the
// shared nicks map from chat.go) and broadcasts so everyone's nameplates
// refresh. Empty/whitespace-only names are ignored.
func (g *game) rename(p *gamePlayer, newNick string) {
	newNick = strings.TrimSpace(newNick)
	if newNick == "" {
		return
	}
	// setNick uses its own mutex; call it before taking g.mu so we don't
	// hold two locks at once. Slight inconsistency window (new joiners
	// might see the new nick before existing players' snapshots refresh)
	// is acceptable for the use case.
	setNick(p.ip, newNick)

	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.players[p]; !ok {
		return
	}
	p.nick = newNick
	g.broadcast()
}

// say sets the speaker's floating-bubble message, broadcasts the new state,
// and schedules a background goroutine to clear the bubble after
// bubbleDuration. The clearer only fires if no newer message has been
// said in the meantime (checked via messageExpires equality).
func (g *game) say(p *gamePlayer, msg string) {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return
	}
	g.mu.Lock()
	if _, ok := g.players[p]; !ok {
		g.mu.Unlock()
		return
	}
	p.message = msg
	p.messageExpires = time.Now().Add(bubbleDuration)
	expires := p.messageExpires
	g.broadcast()
	g.mu.Unlock()

	go func() {
		time.Sleep(bubbleDuration + 10*time.Millisecond)
		g.mu.Lock()
		defer g.mu.Unlock()
		if _, ok := g.players[p]; !ok {
			return
		}
		// Only clear if the expiry we scheduled is still the active one.
		// A newer say() would have replaced it with a later time.
		if p.messageExpires.Equal(expires) {
			p.message = ""
			g.broadcast()
		}
	}()
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
	mode     inputMode       // current keyboard mode (move / speak / rename)
	input    textinput.Model // shared input widget for speak and rename modes
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

	// Shared input widget used for speech bubbles and renames. Width and
	// CharLimit are reconfigured per-mode when the player enters one. Not
	// focused until the player presses 't' (speak) or 'n' (rename).
	ti := textinput.New()
	ti.Prompt = ""
	styles := ti.Styles()
	styles.Focused.Text = lipgloss.NewStyle().Foreground(colorCream)
	styles.Cursor.Color = colorAmber
	ti.SetStyles(styles)

	return gameScreen{
		width:    width,
		height:   height,
		player:   p,
		snapshot: theGame.snapshot(),
		input:    ti,
	}
}

func (m gameScreen) title() string { return "game" }

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
		// Non-move-mode key handling takes precedence: while composing,
		// almost every key goes into the input rather than the movement
		// system. Esc cancels, enter commits the appropriate action.
		if m.mode != inputModeMove {
			switch msg.String() {
			case "esc":
				m.mode = inputModeMove
				m.input.Reset()
				m.input.Blur()
				return m, nil
			case "enter":
				text := m.input.Value()
				mode := m.mode
				m.mode = inputModeMove
				m.input.Reset()
				m.input.Blur()
				switch mode {
				case inputModeSpeak:
					theGame.say(m.player, text)
				case inputModeRename:
					theGame.rename(m.player, text)
				}
				return m, nil
			}
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
		// Movement mode.
		switch msg.String() {
		case "esc", "q":
			theGame.leave(m.player)
			return m, func() tea.Msg { return ShowDirectoryMsg{} }
		case "t":
			m.mode = inputModeSpeak
			m.input.CharLimit = bubbleCharCap
			m.input.SetWidth(bubbleCharCap)
			return m, m.input.Focus()
		case "n":
			m.mode = inputModeRename
			m.input.CharLimit = nickCharCap
			m.input.SetWidth(nickCharCap)
			// Pre-fill with the current nick (if any) so the user can edit
			// rather than retype. getNick reads under its own mutex, safe
			// to call here.
			m.input.SetValue(getNick(m.player.ip))
			m.input.CursorEnd()
			return m, m.input.Focus()
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
	// Forward any unhandled message (e.g. cursor blink ticks while a
	// non-move mode is active) to the textinput so its internal state
	// keeps ticking.
	if m.mode != inputModeMove {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
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

	// Cell styles. These are ultraviolet styles (the lower-level type that
	// Lip Gloss v2's Canvas operates on) rather than lipgloss.Style — we're
	// going under Lip Gloss for direct cell access. `Attrs` is a bitfield
	// of `uv.AttrBold | uv.AttrFaint | ...`.
	treeStyle := uv.Style{Fg: colorTree}
	waterStyle := uv.Style{Fg: colorWater}
	grassStyle := uv.Style{Fg: colorGrass}
	selfStyle := uv.Style{Fg: colorPlayerSelf, Attrs: uv.AttrBold}
	otherStyle := uv.Style{Fg: colorPlayerOther, Attrs: uv.AttrBold}

	// Index other players by world coord so the per-tile loop below is an
	// O(1) lookup instead of a linear scan through the snapshot.
	others := make(map[[2]int]gamePlayerInfo, len(m.snapshot))
	for p, info := range m.snapshot {
		if p == m.player {
			continue
		}
		others[[2]int{info.x, info.y}] = info
	}

	// Build the viewport on a Lip Gloss Canvas: a 2D buffer of styled
	// cells. We set each tile's two cells directly rather than building
	// a string with embedded ANSI per glyph — Bubble Tea's renderer can
	// then diff the buffer against the previous frame and only emit the
	// cells that actually changed.
	canvas := lipgloss.NewCanvas(m.width, viewportH)
	for y := 0; y < viewportTilesH; y++ {
		for tx := 0; tx < viewportTilesW; tx++ {
			wx, wy := camX+tx, camY+y
			cx := tx * 2 // left cell of this 2-wide tile

			// Out-of-world: leave the canvas's default blank cell in place.
			if wx < 0 || wx >= worldWidth || wy < 0 || wy >= worldHeight {
				continue
			}

			var left, right *uv.Cell
			switch {
			case wx == me.x && wy == me.y:
				left = &uv.Cell{Content: "@", Width: 1, Style: selfStyle}
				right = &uv.Cell{Content: " ", Width: 1}
			case othersContains(others, wx, wy):
				left = &uv.Cell{Content: "@", Width: 1, Style: otherStyle}
				right = &uv.Cell{Content: " ", Width: 1}
			default:
				switch world[wy][wx] {
				case tileTree:
					left = &uv.Cell{Content: "T", Width: 1, Style: treeStyle}
					right = &uv.Cell{Content: " ", Width: 1}
				case tileWater:
					left = &uv.Cell{Content: "~", Width: 1, Style: waterStyle}
					right = &uv.Cell{Content: "~", Width: 1, Style: waterStyle}
				default:
					left = &uv.Cell{Content: ".", Width: 1, Style: grassStyle}
					right = &uv.Cell{Content: " ", Width: 1}
				}
			}
			canvas.SetCell(cx, y, left)
			canvas.SetCell(cx+1, y, right)
		}
	}

	statusLine := lipgloss.NewStyle().Foreground(colorAmber).Bold(true).Render("game test") +
		lipgloss.NewStyle().Foreground(colorAmberDim).Render(
			fmt.Sprintf("  %d players · pos (%d, %d)", len(m.snapshot), me.x, me.y),
		)
	var helpText string
	switch m.mode {
	case inputModeSpeak:
		helpText = "enter to send · esc to cancel"
	case inputModeRename:
		helpText = "type a name · enter to set · esc to cancel"
	default:
		helpText = "arrows / wasd / hjkl to move · t to say · n to rename · esc to leave"
	}
	help := lipgloss.NewStyle().Foreground(colorAmberDim).Render(helpText)

	base := strings.Join([]string{statusLine, canvas.Render(), help}, "\n")

	// Collect every bubble we need to overlay: live messages from any
	// player whose expiry is still in the future, plus the local player's
	// in-progress textinput if they're composing or renaming.
	//
	// `border` controls the bubble's border colour — amber for speech,
	// lavender for renames — which gives the user a visual cue about
	// which mode they're in (it also matches the nameplate colour for
	// the rename case, since renaming literally edits a nameplate).
	type bubbleSpec struct {
		x, y   int
		text   string
		border color.Color
	}
	var bubbles []bubbleSpec
	now := time.Now()
	for p, info := range m.snapshot {
		// Skip our own message if we're typing — the live input bubble
		// below replaces it. Also skip empty/expired messages.
		if p == m.player && m.mode != inputModeMove {
			continue
		}
		if info.message == "" || !info.messageExpires.After(now) {
			continue
		}
		bubbles = append(bubbles, bubbleSpec{
			x: info.x, y: info.y, text: info.message, border: colorAmber,
		})
	}
	if m.mode != inputModeMove {
		border := colorAmber
		if m.mode == inputModeRename {
			border = colorNameplate
		}
		bubbles = append(bubbles, bubbleSpec{
			x: me.x, y: me.y, text: m.input.View(), border: border,
		})
	}

	// Build a Compositor: the rendered base text becomes the bottom layer.
	// On top we add a persistent nameplate per player, then any active
	// speech bubbles (which will sit visually on top of nameplates when
	// both occupy the same cells — compositor z-order is insertion order).
	//
	// Layout per bubble: 1 top border + 1 content line + 1 bottom border +
	// 1 tail row = 4 rows total. Text never wraps because we don't set a
	// Width on the bubble's style, so this stays constant.
	layers := []*lipgloss.Layer{lipgloss.NewLayer(base)}
	const (
		canvasOffsetY = 1 // base rows: [0] status, [1..] canvas
		bubbleH       = 4
	)

	// onScreen reports whether a world tile is inside the current viewport.
	onScreen := func(x, y int) bool {
		return x >= camX && x < camX+viewportTilesW &&
			y >= camY && y < camY+viewportTilesH
	}

	// Nameplates: one row above each on-screen player's @, centred on it.
	// Uses a soft lavender so they read as UI rather than part of the world.
	nameStyle := lipgloss.NewStyle().Foreground(colorNameplate)
	for _, info := range m.snapshot {
		if info.name == "" || !onScreen(info.x, info.y) {
			continue
		}
		playerCol := (info.x - camX) * 2
		playerRow := canvasOffsetY + (info.y - camY)
		// Prefer above the player, flip below if at the top of the canvas.
		nameRow := playerRow - 1
		if nameRow < canvasOffsetY {
			nameRow = playerRow + 1
		}
		plate := nameStyle.Render(info.name)
		plateW := lipgloss.Width(plate)
		plateX := playerCol - (plateW-1)/2
		if plateX < 0 {
			plateX = 0
		}
		if plateX+plateW > m.width {
			plateX = m.width - plateW
		}
		layers = append(layers, lipgloss.NewLayer(plate).X(plateX).Y(nameRow))
	}

	for _, b := range bubbles {
		if !onScreen(b.x, b.y) {
			continue
		}
		// Player's left cell on the canvas, then in compositor coords:
		playerCol := (b.x - camX) * 2
		playerRow := canvasOffsetY + (b.y - camY)
		// If the bubble fits above the player without spilling off the
		// top of the screen, point down at them. Otherwise flip below.
		roomAbove := playerRow >= bubbleH
		bubble, tailCol := renderBubble(b.text, roomAbove, b.border)
		var bubbleY int
		if roomAbove {
			bubbleY = playerRow - bubbleH
		} else {
			bubbleY = playerRow + 1
		}
		// Anchor the bubble so the tail sits in the same column as the
		// player's "@" glyph.
		bubbleX := playerCol - tailCol
		// Clip horizontally so we never produce out-of-bounds coordinates.
		if bubbleX < 0 {
			bubbleX = 0
		}
		if bw := lipgloss.Width(bubble); bubbleX+bw > m.width {
			bubbleX = m.width - bw
			if bubbleX < 0 {
				bubbleX = 0
			}
		}
		layers = append(layers, lipgloss.NewLayer(bubble).X(bubbleX).Y(bubbleY))
	}
	return lipgloss.NewCompositor(layers...).Render()
}

// renderBubble draws a single speech-style bubble around `text`. tailDown
// flips between bubble-above-speaker (tail points down) and below (tail
// points up). border selects the border + tail colour (amber for speech,
// lavender for renames). The second return is the column index of the
// tail glyph, used by the caller to align it over the speaker.
func renderBubble(text string, tailDown bool, border color.Color) (string, int) {
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(border).
		Padding(0, 1).
		Foreground(colorCream).
		Render(text)
	boxW := lipgloss.Width(box)
	tailCol := boxW / 2
	tail := lipgloss.NewStyle().Foreground(border)
	if tailDown {
		return box + "\n" + strings.Repeat(" ", tailCol) + tail.Render("v"), tailCol
	}
	return strings.Repeat(" ", tailCol) + tail.Render("^") + "\n" + box, tailCol
}

// othersContains keeps the type-switch in View readable — Go doesn't have
// a one-liner for "is this key in this map?" without the value.
func othersContains(m map[[2]int]gamePlayerInfo, x, y int) bool {
	_, ok := m[[2]int{x, y}]
	return ok
}
