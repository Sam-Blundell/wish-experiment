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
// so the game uses its own colours.
var (
	colorGrass       = lipgloss.Color("22")  // grass — deep dim green (recedes)
	colorTree        = lipgloss.Color("130") // trees — rust brown (contrasts)
	colorWater       = lipgloss.Color("67")  // water — steel blue
	colorWall        = lipgloss.Color("240") // walls — dim grey stone
	colorFloor       = lipgloss.Color("137") // interior floor — warm wood
	colorPlayerSelf  = lipgloss.Color("15")  // your @ — white
	colorPlayerOther = lipgloss.Color("245") // other players' @ — grey
	colorNameplate   = lipgloss.Color("141") // nameplates — soft lavender (UI, not world)
)

type tile rune

const (
	tileGrass tile = '.'
	tileTree  tile = 'T'
	tileWater tile = '~'
	tileWall  tile = '#' // interior + exterior walls — not walkable
	tileFloor tile = ',' // interior floor — walkable
	tileDoor  tile = '/' // walking *into* one teleports between worlds
)

// walkable says whether the player can step onto this tile via normal
// movement. Door tiles are deliberately excluded: stepping into a door
// triggers a teleport in move() instead of standing on it.
func (t tile) walkable() bool {
	switch t {
	case tileTree, tileWater, tileWall, tileDoor:
		return false
	}
	return true
}

// worldGrid is one game cell — a grid of tiles plus its dimensions and the
// list of doors that lead out of it. The outdoor world is one of these;
// each interior (a house, a cave, eventually a dungeon room) is another.
//
// `floor` is the tile the world considers its natural walkable surface
// (grass outdoors, wood-floor in the house). Doors render as this tile so
// they read as gaps rather than special-looking glyphs.
type worldGrid struct {
	tiles  [][]tile
	width  int
	height int
	floor  tile
	doors  []door
}

// door pins a door tile to its target. Stepping into (x, y) on this world
// teleports the player to (target.x, target.y) in worlds[target.worldID].
type door struct {
	x, y   int
	target doorTarget
}

type doorTarget struct {
	worldID int
	x, y    int
}

// doorAt returns the target for the door at (x, y) if one is registered.
// Worlds typically have one or two doors, so a linear scan is fine.
func (w *worldGrid) doorAt(x, y int) (doorTarget, bool) {
	for _, d := range w.doors {
		if d.x == x && d.y == y {
			return d.target, true
		}
	}
	return doorTarget{}, false
}

// World IDs. Players spawn in worldOutdoor and can warp to worldHouse by
// walking into the house's door tile.
const (
	worldOutdoor = 0
	worldHouse   = 1
)

// worlds is the slice of all game cells. Generated once at startup —
// never mutated, so any goroutine can read from any world without
// locking. Doors are wired up after both worlds exist so each can
// reference the other.
var worlds = func() []*worldGrid {
	outdoor := generateOutdoor()
	house := generateHouse()

	// Outdoor house at (60, 30): top-left of its 5×3 footprint. The door
	// is at the south face. Stepping into that door drops the player one
	// row above the interior door (facing into the room); stepping into
	// the interior door drops them one row below the outdoor door so we
	// don't immediately re-trigger the teleport.
	// House sits to the upper-right of the world's centre — far enough
	// that you have to walk a bit from spawn to find it, but well clear
	// of the edges and both lakes.
	const (
		houseWide, houseTall = 5, 3
		houseX, houseY       = 80, 18
		outDoorX             = houseX + houseWide/2 // 82
		outDoorY             = houseY + houseTall - 1 // 20
		inDoorX, inDoorY     = 9, 11 // bottom-centre of the 18×12 interior
	)
	placeHouse(outdoor, houseX, houseY, houseWide, houseTall)
	outdoor.doors = []door{{
		x: outDoorX, y: outDoorY,
		target: doorTarget{worldID: worldHouse, x: inDoorX, y: inDoorY - 1},
	}}
	house.doors = []door{{
		x: inDoorX, y: inDoorY,
		target: doorTarget{worldID: worldOutdoor, x: outDoorX, y: outDoorY + 1},
	}}

	return []*worldGrid{outdoor, house}
}()

// generateOutdoor builds the open-air cell: grass with a tree border,
// random tree clutter, and two circular lakes.
func generateOutdoor() *worldGrid {
	r := rand.New(rand.NewSource(42))
	tiles := make([][]tile, worldHeight)
	for y := range tiles {
		tiles[y] = make([]tile, worldWidth)
		for x := range tiles[y] {
			tiles[y][x] = tileGrass
		}
	}
	for x := 0; x < worldWidth; x++ {
		tiles[0][x] = tileTree
		tiles[worldHeight-1][x] = tileTree
	}
	for y := 0; y < worldHeight; y++ {
		tiles[y][0] = tileTree
		tiles[y][worldWidth-1] = tileTree
	}
	for i := 0; i < 250; i++ {
		x := r.Intn(worldWidth-2) + 1
		y := r.Intn(worldHeight-2) + 1
		tiles[y][x] = tileTree
	}
	w := &worldGrid{tiles: tiles, width: worldWidth, height: worldHeight, floor: tileGrass}
	carveLake(w, worldWidth/3, worldHeight/3, 4, 4)
	carveLake(w, 3*worldWidth/4, 2*worldHeight/3, 5, 5)
	return w
}

// generateHouse builds the interior cell: an 18×12 stone-walled room with
// a wood floor. The bottom-centre wall tile is replaced with floor (the
// "door") so it reads as a gap when rendered; the teleport hook on that
// position is registered by the worlds initialiser.
func generateHouse() *worldGrid {
	const (
		hw = 18
		hh = 12
	)
	tiles := make([][]tile, hh)
	for y := range tiles {
		tiles[y] = make([]tile, hw)
		for x := range tiles[y] {
			if y == 0 || y == hh-1 || x == 0 || x == hw-1 {
				tiles[y][x] = tileWall
			} else {
				tiles[y][x] = tileFloor
			}
		}
	}
	// Door: just a missing wall section. The teleport behaviour is
	// attached to this position by the worlds initialiser.
	tiles[hh-1][hw/2] = tileFloor
	return &worldGrid{tiles: tiles, width: hw, height: hh, floor: tileFloor}
}

// placeHouse stamps an wide×tall building onto the outdoor world at
// (x, y). Walls everywhere except the bottom-centre tile, which becomes
// a door tile (its target is wired up by the worlds initialiser). The
// tile directly south of the door is forced to grass so the teleport-out
// landing spot is always walkable, even if random tree placement put
// something there.
func placeHouse(w *worldGrid, x, y, wide, tall int) {
	for dy := 0; dy < tall; dy++ {
		for dx := 0; dx < wide; dx++ {
			w.tiles[y+dy][x+dx] = tileWall
		}
	}
	doorX := x + wide/2
	doorY := y + tall - 1
	w.tiles[doorY][doorX] = tileDoor
	if doorY+1 < w.height {
		w.tiles[doorY+1][doorX] = tileGrass
	}
}

func carveLake(w *worldGrid, cx, cy, rx, ry int) {
	for y := 1; y < w.height-1; y++ {
		for x := 1; x < w.width-1; x++ {
			dx := float64(x - cx)
			dy := float64(y - cy)
			if dx*dx/float64(rx*rx)+dy*dy/float64(ry*ry) < 1 {
				w.tiles[y][x] = tileWater
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
	worldID        int // which world the player is currently in
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
	worldID        int
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

// placeSpawn finds an unoccupied walkable tile in the outdoor world near
// its centre and assigns it to p. Caller holds g.mu.
func (g *game) placeSpawn(p *gamePlayer) bool {
	p.worldID = worldOutdoor
	w := worlds[worldOutdoor]

	// Build a quick lookup of currently-occupied tiles in this world.
	// `[2]int` is a fixed-size array (not a slice), and arrays are
	// comparable in Go, which makes them valid map keys — useful for
	// "set of coordinates".
	occupied := make(map[[2]int]bool, len(g.players))
	for other := range g.players {
		if other.worldID == worldOutdoor {
			occupied[[2]int{other.x, other.y}] = true
		}
	}
	cx, cy := w.width/2, w.height/2
	for r := 0; r < w.width; r++ {
		for y := cy - r; y <= cy+r; y++ {
			for x := cx - r; x <= cx+r; x++ {
				if y < 0 || y >= w.height || x < 0 || x >= w.width {
					continue
				}
				if !w.tiles[y][x].walkable() {
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
// Stepping into a door tile teleports the player to the door's target.
func (g *game) move(p *gamePlayer, dx, dy int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.players[p]; !ok {
		return
	}
	w := worlds[p.worldID]
	nx, ny := p.x+dx, p.y+dy
	if nx < 0 || nx >= w.width || ny < 0 || ny >= w.height {
		return
	}

	// Door: walking into one warps the player to its target. Doors are
	// not "walkable" in the normal sense — we handle them first, before
	// the walkability check below.
	if target, ok := w.doorAt(nx, ny); ok {
		dest := worlds[target.worldID]
		if target.x < 0 || target.x >= dest.width ||
			target.y < 0 || target.y >= dest.height ||
			!dest.tiles[target.y][target.x].walkable() {
			return
		}
		for other := range g.players {
			if other != p && other.worldID == target.worldID &&
				other.x == target.x && other.y == target.y {
				return
			}
		}
		p.worldID = target.worldID
		p.x = target.x
		p.y = target.y
		g.broadcast()
		return
	}

	if !w.tiles[ny][nx].walkable() {
		return
	}
	for other := range g.players {
		if other != p && other.worldID == p.worldID &&
			other.x == nx && other.y == ny {
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
			worldID:        p.worldID,
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
	curWorld := worlds[me.worldID]

	const statusH, helpH = 1, 1
	viewportH := m.height - statusH - helpH
	viewportTilesW := m.width / 2
	viewportTilesH := viewportH

	// Camera centred on the local player, clamped to the current world.
	// Far-edge clamps come first so the >=0 clamp wins when the viewport
	// is bigger than the world (which happens easily for the small
	// interior cell).
	camX := me.x - viewportTilesW/2
	camY := me.y - viewportTilesH/2
	if camX > curWorld.width-viewportTilesW {
		camX = curWorld.width - viewportTilesW
	}
	if camY > curWorld.height-viewportTilesH {
		camY = curWorld.height - viewportTilesH
	}
	if camX < 0 {
		camX = 0
	}
	if camY < 0 {
		camY = 0
	}

	// When a world is smaller than the viewport, the camera clamps to 0
	// above — but that leaves the world rendered against the top-left
	// corner. Compute a per-axis offset in tile-units so we instead draw
	// it centred on the screen. For larger worlds these stay zero and
	// nothing changes from the previous behaviour.
	worldOffsetTilesX := 0
	worldOffsetTilesY := 0
	if curWorld.width < viewportTilesW {
		worldOffsetTilesX = (viewportTilesW - curWorld.width) / 2
	}
	if curWorld.height < viewportTilesH {
		worldOffsetTilesY = (viewportTilesH - curWorld.height) / 2
	}

	// Cell styles. These are ultraviolet styles (the lower-level type that
	// Lip Gloss v2's Canvas operates on) rather than lipgloss.Style — we're
	// going under Lip Gloss for direct cell access. `Attrs` is a bitfield
	// of `uv.AttrBold | uv.AttrFaint | ...`.
	treeStyle := uv.Style{Fg: colorTree}
	waterStyle := uv.Style{Fg: colorWater}
	grassStyle := uv.Style{Fg: colorGrass}
	wallStyle := uv.Style{Fg: colorWall}
	floorStyle := uv.Style{Fg: colorFloor}
	selfStyle := uv.Style{Fg: colorPlayerSelf, Attrs: uv.AttrBold}
	otherStyle := uv.Style{Fg: colorPlayerOther, Attrs: uv.AttrBold}

	// Index other players by world coord so the per-tile loop below is an
	// O(1) lookup instead of a linear scan through the snapshot. Only
	// include players in the same world as us — we can't see into other
	// cells.
	others := make(map[[2]int]gamePlayerInfo, len(m.snapshot))
	for p, info := range m.snapshot {
		if p == m.player || info.worldID != me.worldID {
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
			// World coord = camera position + screen offset, minus the
			// centring shift. When the world is bigger than the viewport
			// the offset is zero and this reduces to the original calc.
			wx := camX + tx - worldOffsetTilesX
			wy := camY + y - worldOffsetTilesY
			cx := tx * 2 // left cell of this 2-wide tile

			// Out-of-world (or in the blank "margin" around a small world):
			// leave the canvas's default blank cell in place.
			if wx < 0 || wx >= curWorld.width || wy < 0 || wy >= curWorld.height {
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
				switch curWorld.tiles[wy][wx] {
				case tileTree:
					left = &uv.Cell{Content: "T", Width: 1, Style: treeStyle}
					right = &uv.Cell{Content: " ", Width: 1}
				case tileWater:
					left = &uv.Cell{Content: "~", Width: 1, Style: waterStyle}
					right = &uv.Cell{Content: "~", Width: 1, Style: waterStyle}
				case tileWall:
					left = &uv.Cell{Content: "#", Width: 1, Style: wallStyle}
					right = &uv.Cell{Content: "#", Width: 1, Style: wallStyle}
				case tileFloor:
					left = &uv.Cell{Content: ",", Width: 1, Style: floorStyle}
					right = &uv.Cell{Content: " ", Width: 1}
				case tileDoor:
					// Render as the world's floor so the door reads as a
					// gap in the wall. The teleport behaviour is unaffected.
					if curWorld.floor == tileFloor {
						left = &uv.Cell{Content: ",", Width: 1, Style: floorStyle}
					} else {
						left = &uv.Cell{Content: ".", Width: 1, Style: grassStyle}
					}
					right = &uv.Cell{Content: " ", Width: 1}
				default:
					left = &uv.Cell{Content: ".", Width: 1, Style: grassStyle}
					right = &uv.Cell{Content: " ", Width: 1}
				}
			}
			canvas.SetCell(cx, y, left)
			canvas.SetCell(cx+1, y, right)
		}
	}

	cellName := "outside"
	if me.worldID == worldHouse {
		cellName = "house"
	}
	// Count only players in our cell — the snapshot includes everyone.
	visibleCount := 0
	for _, info := range m.snapshot {
		if info.worldID == me.worldID {
			visibleCount++
		}
	}
	statusLine := lipgloss.NewStyle().Foreground(colorAmber).Bold(true).Render("game test") +
		lipgloss.NewStyle().Foreground(colorAmberDim).Render(
			fmt.Sprintf("  [%s] %d here · pos (%d, %d)", cellName, visibleCount, me.x, me.y),
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
		// Skip players in other worlds — speech doesn't carry between cells.
		if info.worldID != me.worldID {
			continue
		}
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
	// Only show plates for players in the same world as us.
	nameStyle := lipgloss.NewStyle().Foreground(colorNameplate)
	for _, info := range m.snapshot {
		if info.worldID != me.worldID || info.name == "" || !onScreen(info.x, info.y) {
			continue
		}
		playerCol := (info.x - camX + worldOffsetTilesX) * 2
		playerRow := canvasOffsetY + (info.y - camY + worldOffsetTilesY)
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
		// Player's left cell on the canvas, then in compositor coords.
		// Apply the same centring offset used for the tile rendering.
		playerCol := (b.x - camX + worldOffsetTilesX) * 2
		playerRow := canvasOffsetY + (b.y - camY + worldOffsetTilesY)
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
