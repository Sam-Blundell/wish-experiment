package main

import (
	"fmt"
	"image/color"
	"math"
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
// game ("gametest"): a small multiplayer overworld — a forest of sprite trees
// and ponds, a timber cabin, and a wildflower meadow reached by a south path,
// dotted with interactive landmarks (a readable signpost, well and standing
// stones). Rendering fills every tile with a background colour and textures it
// deterministically per coordinate; see the View method and the tile palette.
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
	inputModeRead                    // reading a signpost modal
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

	// Background fills. mk2 paints a background colour on every tile so the
	// world reads as solid blocks instead of glyphs floating on the
	// terminal's default (black) background. The *2 colours above are the
	// detail-glyph foregrounds drawn over these fills.
	colorGrassBg     = lipgloss.Color("#004800") // grass fill — dark green block
	colorWaterBg     = lipgloss.Color("24")      // water fill — deep blue
	colorWaterRipple = lipgloss.Color("74")      // ripple glyph drawn over water
	colorShallowBg   = lipgloss.Color("31")      // lake shallows — lighter cyan-blue
	colorShallowRip  = lipgloss.Color("117")     // ripple glyph over shallows
	colorSandBg      = lipgloss.Color("180")     // beach — warm tan
	colorSandSpeck   = lipgloss.Color("137")     // sand speckle glyph (darker tan)
	colorMudBg       = lipgloss.Color("#43321f") // forest-pond bank — dark wet earth
	colorMudSpeck    = lipgloss.Color("#5a4630") // mud speckle glyph (lighter earth)
	colorDirtBg      = lipgloss.Color("#574028") // dirt path — dark trodden earth
	colorDirtSpeck   = lipgloss.Color("#6e533a") // dirt speckle glyph (lighter pebbles)
	colorWallTimber  = lipgloss.Color("#8a6038") // interior timber wall — brown glyph on black

	// Grass texture: a single dark base green with foreground tuft glyphs
	// scattered over it and rare flowers, picked deterministically per tile
	// (see grassCell). No background mottling — that read as noise.
	colorGrassTuft    = lipgloss.Color("65")  // tuft glyph — muted sage
	colorFlowerYellow = lipgloss.Color("229") // flower — pale yellow
	colorFlowerSalmon = lipgloss.Color("217") // flower — salmon

	// Trees are drawn as multi-cell sprites: a narrow brown trunk (the only
	// blocking tile) with a leafy canopy overlaid above it. See drawTrees.
	colorTrunk       = lipgloss.Color("#6b4423") // trunk — bark brown
	colorCanopy      = lipgloss.Color("#3a9d3a") // foliage — mid green stipple
	colorCanopyLight = lipgloss.Color("#57b357") // foliage — sunlit highlight
	colorCanopyDark  = lipgloss.Color("#226b22") // foliage — shadowed leaves
	colorCanopyBg    = lipgloss.Color("#0e4f14") // foliage fill behind the stipple

	// The cabin: a shingled roof with a sunlit ridge, timber walls, lit
	// windows and a wooden door. See placeHouse and the tile switch in View.
	colorRoofBg     = lipgloss.Color("#7d4a35") // roof shingles — warm brown
	colorRoofDark   = lipgloss.Color("#5e3526") // shingle stipple — shadow
	colorRidge      = lipgloss.Color("#9c6650") // roof ridge — sunlit top edge
	colorTimberBg   = lipgloss.Color("#7b5a3a") // log wall
	colorTimberLine = lipgloss.Color("#5a4028") // log courses (grooves)
	colorWindowBg   = lipgloss.Color("#e7c24f") // lit window — warm glow
	colorDoorBg     = lipgloss.Color("#3f2a16") // door — dark wood
	colorDoorPlank  = lipgloss.Color("#7a5230") // door plank seam (lighter)

	// Clutter colours (see scatterClutter and the tile switch in View).
	colorRockBg    = lipgloss.Color("#595959") // boulder — grey stone
	colorRockSpeck = lipgloss.Color("#808080") // boulder highlight
	colorBushBg    = lipgloss.Color("#2f6f2f") // shrub — mid green mound
	colorBush      = lipgloss.Color("#4f9f4f") // shrub foliage glyph
	colorStumpBg   = lipgloss.Color("#5a3f28") // stump — brown
	colorStump     = lipgloss.Color("#8a6038") // stump rings (lighter)
	colorLogBg     = lipgloss.Color("#54402a") // fallen log — brown
	colorLog       = lipgloss.Color("#3a2a1a") // log grain (darker)
	colorShroom    = lipgloss.Color("#c0392b") // mushroom cap — red
	colorReed      = lipgloss.Color("#9caf6a") // reeds — yellow-green

	// The meadow: a brighter, sunnier clearing reached via the south path.
	colorMeadowBg     = lipgloss.Color("#2a7d2a") // sunlit meadow grass
	colorMeadowTuft   = lipgloss.Color("#8fcf6a") // tall meadow-grass tufts
	colorFlowerWhite  = lipgloss.Color("#f2f2f2") // daisy
	colorFlowerPurple = lipgloss.Color("#b388e0") // bellflower
	colorFlowerPink   = lipgloss.Color("#f29bd0") // clover

	// Signpost (an interactive landmark — see sign and the read modal).
	colorSignPost     = lipgloss.Color("#6b4423") // post — bark brown
	colorSignBoardBg  = lipgloss.Color("#b08a4a") // board — pale wood
	colorSignBoardInk = lipgloss.Color("#3a2a16") // lettering on the board

	// Landmarks: well, standing stones, jetty.
	colorWellRimBg   = lipgloss.Color("#6e6e6e") // well rim — grey stone
	colorWellRim     = lipgloss.Color("#9a9a9a") // well rim highlight
	colorWellWaterBg = lipgloss.Color("#0c1a26") // well shaft — near-black water
	colorWellWater   = lipgloss.Color("#1d3a52") // faint glint on the shaft
	colorStoneBg     = lipgloss.Color("#6a6a76") // standing stone — cool grey
	colorStone       = lipgloss.Color("#9a9aa6") // standing stone highlight
	colorJettyBg     = lipgloss.Color("#7a5a38") // jetty planks — wood
	colorJetty       = lipgloss.Color("#5a4028") // plank seams
)

type tile rune

const (
	tileGrass   tile = '.'
	tileMeadow  tile = 'M' // sunlit meadow grass (second outdoor cell) — walkable
	tileTree    tile = 'T'
	tileWater   tile = '~'
	tileShallow tile = '≈' // lake shallows — lighter water, not walkable
	tileSand    tile = ':' // sandy beach — walkable (for coastal / large lakes)
	tileMud     tile = 'm' // boggy waterline of a forest pond — walkable
	tileDirt    tile = 'p' // dirt path — walkable

	tileWall   tile = '#' // interior walls — not walkable
	tileFloor  tile = ',' // interior floor — walkable
	tileDoor   tile = '/' // walking *into* one teleports between worlds
	tileRoof   tile = 'R' // cabin roof shingles — not walkable
	tileRidge  tile = '^' // cabin roof ridge (sunlit top) — not walkable
	tileTimber tile = '=' // cabin timber wall — not walkable
	tileWindow tile = 'O' // cabin lit window — not walkable

	// Clutter. Boulders/bushes/stumps/logs block movement; mushrooms and
	// reeds are decorative and walkable. All scattered by scatterClutter.
	tileRock   tile = '*' // boulder — not walkable
	tileBush   tile = '&' // shrub — not walkable
	tileStump  tile = 'u' // tree stump — not walkable
	tileLog    tile = '_' // fallen log — not walkable
	tileShroom tile = '!' // mushrooms — walkable
	tileReed   tile = '|' // pond-side reeds — walkable

	tileSignPost  tile = 'I' // signpost post — not walkable
	tileSignBoard tile = 'B' // signpost board (readable) — not walkable

	tileWellRim   tile = 'Q' // well rim (stone) — not walkable
	tileWellWater tile = 'q' // well shaft (dark water) — not walkable
	tileStone     tile = 'S' // standing stone (menhir) — not walkable
	tileJetty     tile = 'J' // jetty planks over water — walkable
)

// walkable says whether the player can step onto this tile via normal
// movement. Door tiles are deliberately excluded: stepping into a door
// triggers a teleport in move() instead of standing on it.
func (t tile) walkable() bool {
	switch t {
	case tileTree, tileWater, tileShallow, tileWall, tileDoor,
		tileRoof, tileRidge, tileTimber, tileWindow,
		tileRock, tileBush, tileStump, tileLog,
		tileSignPost, tileSignBoard,
		tileWellRim, tileWellWater, tileStone:
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
	signs  []sign
}

// sign is a readable signpost: standing within one tile of (x, y) and pressing
// `i` opens a modal showing `text`. Purely a client-side read — nothing about
// it is broadcast or mutated, so it lives on the (read-only) world like doors.
type sign struct {
	x, y int
	text string
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
	worldMeadow  = 2
)

// worlds is the slice of all game cells. Generated once at startup —
// never mutated, so any goroutine can read from any world without
// locking. Doors are wired up after both worlds exist so each can
// reference the other.
var worlds = func() []*worldGrid {
	outdoor := generateOutdoor()
	house := generateHouse()
	meadow := generateMeadow()

	// Outdoor cabin at (80, 18): top-left of its 7×4 footprint. The door
	// is at the south face. Stepping into that door drops the player one
	// row above the interior door (facing into the room); stepping into
	// the interior door drops them one row below the outdoor door so we
	// don't immediately re-trigger the teleport.
	// House sits to the upper-right of the world's centre — far enough
	// that you have to walk a bit from spawn to find it, but well clear
	// of the edges and both lakes.
	const (
		houseWide, houseTall = 7, 4
		houseX, houseY       = 80, 18
		outDoorX             = houseX + houseWide/2   // 82
		outDoorY             = houseY + houseTall - 1 // 20
		inDoorX, inDoorY     = 9, 11                  // bottom-centre of the 18×12 interior
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

	// Wear a dirt trail from the central spawn area up to the cabin door's
	// landing tile. Carved last, after the cabin and lakes are in place, so it
	// routes to the final door position and paves around the water. Its own
	// seed keeps the trail's wander deterministic.
	carvePath(outdoor, worldWidth/2, worldHeight/2, outDoorX, outDoorY+1, rand.New(rand.NewSource(7)))
	// Continue the trail south, all the way to the border, so the player is
	// dropped into the middle of a path and it leads on to the meadow.
	const linkX = worldWidth / 2
	carvePath(outdoor, worldWidth/2, worldHeight/2, linkX, worldHeight-2, rand.New(rand.NewSource(11)))

	// Sprinkle decorative clutter, only onto open grass, so it keeps off the
	// paths, water and cabin.
	scatterClutter(outdoor, rand.New(rand.NewSource(23)))

	// South link: an opening in the forest's south border and the meadow's
	// north border where the path crosses, registered as reciprocal doors so
	// walking off one edge arrives at the other. The opening tiles render as
	// path (dirt), so the trail simply continues rather than showing a door.
	// All three opening tiles target the path's centre column on the far side,
	// which is carved dirt (so the landing is always walkable).
	for dx := -1; dx <= 1; dx++ {
		fx := linkX + dx
		outdoor.tiles[worldHeight-1][fx] = tileDirt
		meadow.tiles[0][fx] = tileDirt
		outdoor.doors = append(outdoor.doors, door{
			x: fx, y: worldHeight - 1,
			target: doorTarget{worldID: worldMeadow, x: linkX, y: 2},
		})
		meadow.doors = append(meadow.doors, door{
			x: fx, y: 0,
			target: doorTarget{worldID: worldOutdoor, x: linkX, y: worldHeight - 3},
		})
	}
	// Path from the meadow's north opening down into the clearing.
	carvePath(meadow, linkX, 1, linkX, worldHeight/2, rand.New(rand.NewSource(31)))

	// A signpost at the forest junction, just east of where the paths meet.
	placeSign(outdoor, linkX+4, worldHeight/2+2,
		"— FOREST CROSSING —\n\nNorth: the cabin\nSouth: the meadow\n\nMind the boggy ponds.")
	// A jetty out over the north-west pond.
	placeJetty(outdoor, worldWidth/3, worldHeight/3+1, worldHeight/3+5)

	// Meadow landmarks: a well and a ring of standing stones out in the open.
	placeWell(meadow, 50, worldHeight/2+2)
	placeStones(meadow, 76, worldHeight/2+4)

	return []*worldGrid{outdoor, house, meadow}
}()

// generateOutdoor builds the open-air cell: grass with a tree border,
// trees clustered into groves, and two circular lakes.
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
	// Trees grow in groves, but the trunks within a grove are kept spaced
	// apart: a wood reads as separate trees with gaps between them, not one
	// solid clump. Each grove scatters trunks within a (generous) radius,
	// rejecting any that land too close to an existing trunk so their canopies
	// stay distinct, and thins toward the rim for a ragged edge.
	const (
		groveCount = 8
		trunkSpace = 2 // reject trunks within this many tiles of another (≥3 apart)
	)
	for g := 0; g < groveCount; g++ {
		cx := r.Intn(worldWidth-2) + 1
		cy := r.Intn(worldHeight-2) + 1
		radius := 6 + r.Intn(7) // 6..12 tiles — large enough to feel like a wood
		r2 := radius * radius
		// Oversample so the grove fills up to the spacing limit (a full stand
		// of well-separated trees) rather than staying sparse.
		for a := 0; a < 4*r2; a++ {
			dx := r.Intn(2*radius+1) - radius
			dy := r.Intn(2*radius+1) - radius
			d2 := dx*dx + dy*dy
			if d2 > r2 {
				continue
			}
			// Keep the core full; thin only the outer rim so the edge is
			// ragged rather than a hard circle.
			if frac := float64(d2) / float64(r2); frac > 0.7 && r.Float64() < (frac-0.7)/0.3 {
				continue
			}
			x, y := cx+dx, cy+dy
			if x < 1 || x >= worldWidth-1 || y < 1 || y >= worldHeight-1 {
				continue
			}
			if hasTreeNear(tiles, x, y, trunkSpace) {
				continue
			}
			tiles[y][x] = tileTree
		}
	}
	// A few lone trees across the open ground so clearings aren't bare, spaced
	// well clear of the groves so they read as solitary.
	for i := 0; i < 60; i++ {
		x := r.Intn(worldWidth-2) + 1
		y := r.Intn(worldHeight-2) + 1
		if hasTreeNear(tiles, x, y, 3) {
			continue
		}
		tiles[y][x] = tileTree
	}
	w := &worldGrid{tiles: tiles, width: worldWidth, height: worldHeight, floor: tileGrass}
	carveLake(w, worldWidth/3, worldHeight/3, 4, 4, tileMud)
	carveLake(w, 3*worldWidth/4, 2*worldHeight/3, 5, 5, tileMud)
	return w
}

// generateMeadow builds the second outdoor cell — a bright, open clearing
// reached via the south path. Same tree border as the forest (it's a clearing
// *within* the woods), but the interior is sunlit meadow grass (textured with
// abundant wildflowers in the renderer) dotted with a few well-spaced lone
// trees, so it reads as airy and open — the opposite of the dense forest. The
// well and standing stones get placed here in the landmark pass.
func generateMeadow() *worldGrid {
	r := rand.New(rand.NewSource(99))
	tiles := make([][]tile, worldHeight)
	for y := range tiles {
		tiles[y] = make([]tile, worldWidth)
		for x := range tiles[y] {
			tiles[y][x] = tileMeadow
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
	// Lone trees, not groves: spaced well apart so the meadow stays open.
	for i := 0; i < 40; i++ {
		x := r.Intn(worldWidth-2) + 1
		y := r.Intn(worldHeight-2) + 1
		if !hasTreeNear(tiles, x, y, 5) {
			tiles[y][x] = tileTree
		}
	}
	return &worldGrid{tiles: tiles, width: worldWidth, height: worldHeight, floor: tileMeadow}
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

// placeHouse stamps a wide×tall cabin onto the outdoor world at (x, y), laid
// out in rows from the oblique top-down view: a sunlit roof ridge along the
// top, roof shingles below it, then the front timber wall on the bottom two
// rows — lit windows flanking the centre, and a door at the bottom-centre
// (its teleport target is wired up by the worlds initialiser). Every tile is
// non-walkable except the door. The tile directly south of the door is forced
// to grass so the teleport-out landing spot is always walkable.
func placeHouse(w *worldGrid, x, y, wide, tall int) {
	for dy := 0; dy < tall; dy++ {
		for dx := 0; dx < wide; dx++ {
			var t tile
			switch {
			case dy == 0:
				t = tileRidge // sunlit ridge along the very top
			case dy < tall-2:
				t = tileRoof // roof slope
			default:
				t = tileTimber // front wall (bottom two rows)
			}
			w.tiles[y+dy][x+dx] = t
		}
	}
	// Windows on the upper wall row, set in from each end.
	if wide >= 5 {
		windowY := y + tall - 2
		w.tiles[windowY][x+1] = tileWindow
		w.tiles[windowY][x+wide-2] = tileWindow
	}
	doorX := x + wide/2
	doorY := y + tall - 1
	w.tiles[doorY][doorX] = tileDoor
	if doorY+1 < w.height {
		w.tiles[doorY+1][doorX] = tileGrass
	}
}

// placeSign stands a readable signpost at (x, y): a board on a post, both
// blocking. The tile directly south is forced to grass so there's always a
// walkable spot to approach and read it from. The text is stored on the world.
func placeSign(w *worldGrid, x, y int, text string) {
	w.tiles[y][x] = tileSignPost
	if y-1 >= 0 {
		w.tiles[y-1][x] = tileSignBoard
	}
	if y+1 < w.height {
		w.tiles[y+1][x] = tileGrass
	}
	w.signs = append(w.signs, sign{x: x, y: y, text: text})
}

// placeWell stamps a stone well centred at (cx, cy): a 3×3 stone rim around a
// dark water shaft, all blocking. Registers a readable plaque on its south rim
// so you can examine it like a signpost.
func placeWell(w *worldGrid, cx, cy int) {
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			w.tiles[cy+dy][cx+dx] = tileWellRim
		}
	}
	w.tiles[cy][cx] = tileWellWater
	w.signs = append(w.signs, sign{x: cx, y: cy + 1,
		text: "An old stone well.\nThe water far below is\nblack and still."})
}

// placeStones sets a rough ring of standing stones around (cx, cy), leaving the
// centre walkable. A readable plaque sits at the centre, so stepping into the
// ring and pressing `i` examines them.
func placeStones(w *worldGrid, cx, cy int) {
	offsets := [][2]int{{-2, -2}, {0, -3}, {2, -2}, {-3, 0}, {3, 0}, {-2, 2}, {0, 3}, {2, 2}}
	for _, o := range offsets {
		x, y := cx+o[0], cy+o[1]
		if x >= 1 && x < w.width-1 && y >= 1 && y < w.height-1 {
			w.tiles[y][x] = tileStone
		}
	}
	w.signs = append(w.signs, sign{x: cx, y: cy,
		text: "Ancient standing stones,\nworn smooth by ages.\nThey hum, faintly."})
}

// placeJetty lays a run of jetty planks down column x from y0 to y1 — a walkable
// boardwalk out over the water (the planks replace whatever water/shore tiles
// were there, so you can walk out onto the pond).
func placeJetty(w *worldGrid, x, y0, y1 int) {
	for y := y0; y <= y1; y++ {
		if x >= 0 && x < w.width && y >= 0 && y < w.height {
			w.tiles[y][x] = tileJetty
		}
	}
}

// nearbySign returns the sign within one tile of the given player position (if
// any), looked up on the player's current world. Reads only a snapshot value,
// so it's safe to call from Update without touching the hub's mutable state.
func nearbySign(me gamePlayerInfo) (sign, bool) {
	w := worlds[me.worldID]
	for _, sg := range w.signs {
		dx, dy := sg.x-me.x, sg.y-me.y
		if dx < 0 {
			dx = -dx
		}
		if dy < 0 {
			dy = -dy
		}
		if dx <= 1 && dy <= 1 {
			return sg, true
		}
	}
	return sign{}, false
}

// signModalLayer builds the centred panel shown while reading a sign.
func signModalLayer(text string, width, height int) *lipgloss.Layer {
	boxW := 40
	if max := width - 8; boxW > max {
		boxW = max
	}
	if boxW < 10 {
		boxW = 10
	}
	panelBg := lipgloss.Color("235")
	body := lipgloss.NewStyle().Foreground(colorCream).Background(panelBg).Width(boxW).Render(text)
	footer := lipgloss.NewStyle().Foreground(colorAmberDim).Background(panelBg).Render("esc to close")
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorAmber).
		Background(panelBg).
		Padding(1, 2).
		Render(body + "\n\n" + footer)
	x := (width - lipgloss.Width(box)) / 2
	y := (height - lipgloss.Height(box)) / 2
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	return lipgloss.NewLayer(box).X(x).Y(y)
}

// carvePath wears a dirt trail from (x0, y0) to (x1, y1). Rather than a jittery
// tile-by-tile walk, it follows the straight line between the endpoints with a
// single gentle sideways meander (a sine sway, tapered to zero at both ends so
// the trail still begins and finishes exactly on its endpoints). It paints with
// a 2×2 brush so the trail is two tiles wide throughout, and only paves grass
// and trees — water, the pond banks and the cabin are left intact, so a trunk in
// the way is trodden out (which also clears its canopy, drawn from trunk tiles).
func carvePath(w *worldGrid, x0, y0, x1, y1 int, r *rand.Rand) {
	pave := func(px, py int) {
		if px < 1 || px >= w.width-1 || py < 1 || py >= w.height-1 {
			return
		}
		if t := w.tiles[py][px]; t == tileGrass || t == tileTree || t == tileMeadow {
			w.tiles[py][px] = tileDirt
		}
	}
	brush := func(cx, cy int) { // 2×2 so the trail is two tiles wide
		pave(cx, cy)
		pave(cx+1, cy)
		pave(cx, cy+1)
		pave(cx+1, cy+1)
	}
	fdx, fdy := float64(x1-x0), float64(y1-y0)
	dist := math.Hypot(fdx, fdy)
	if dist == 0 {
		brush(x0, y0)
		return
	}
	// Perpendicular unit vector (the meander sways along this).
	perpX, perpY := -fdy/dist, fdx/dist
	amp := 2 + r.Float64()*3    // 2..5 tiles of sideways sway
	waves := 0.75 + r.Float64() // ~one gentle hump over the length
	phase := r.Float64() * 2 * math.Pi
	steps := int(dist * 2) // oversample so the 2×2 brush stays continuous
	for i := 0; i <= steps; i++ {
		t := float64(i) / float64(steps)
		off := amp * math.Sin(phase+waves*2*math.Pi*t) * math.Sin(math.Pi*t)
		bx := float64(x0) + t*fdx + off*perpX
		by := float64(y0) + t*fdy + off*perpY
		brush(int(math.Round(bx)), int(math.Round(by)))
	}
}

// carveLake stamps a lake as concentric elliptical bands so it has a
// shoreline: deep water in the middle, a ring of lighter shallows at the
// water's edge, then a narrow `margin` band where it meets the land (mud for a
// forest pond, sand for a coastal shore or large lake). `e` is the normalised
// ellipse distance (e < 1 is inside the water ellipse); the bands are slices of
// e. The margin only replaces grass, so trees and other lakes already in place
// are left alone.
func carveLake(w *worldGrid, cx, cy, rx, ry int, margin tile) {
	for y := 1; y < w.height-1; y++ {
		for x := 1; x < w.width-1; x++ {
			dx := float64(x - cx)
			dy := float64(y - cy)
			e := dx*dx/float64(rx*rx) + dy*dy/float64(ry*ry)
			switch {
			case e < 0.7:
				w.tiles[y][x] = tileWater
			case e < 1:
				w.tiles[y][x] = tileShallow
			case e < 1.3:
				if w.tiles[y][x] == tileGrass {
					w.tiles[y][x] = margin
				}
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
	width       int
	height      int
	player      *gamePlayer
	snapshot    gameSnapshot
	mode        inputMode       // current keyboard mode (move / speak / rename / read)
	input       textinput.Model // shared input widget for speak and rename modes
	readingText string          // text of the sign being read (when mode == read)
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

func (m gameScreen) title() string { return "gametest" }

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
		// Reading a signpost: the modal swallows input until dismissed.
		if m.mode == inputModeRead {
			switch msg.String() {
			case "esc", "i", "enter", "q", " ":
				m.mode = inputModeMove
				m.readingText = ""
			}
			return m, nil
		}
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
		case "i":
			// Read a signpost if we're standing within a tile of one. Read
			// position from the snapshot (not m.player) to avoid racing the
			// hub, exactly as View does.
			if sg, ok := nearbySign(m.snapshot[m.player]); ok {
				m.mode = inputModeRead
				m.readingText = sg.text
			}
			return m, nil
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
	// One fill style per tile type: a detail-glyph foreground over a
	// background colour. Both of a tile's cells are painted with the fill so
	// the world is solid rather than sparse. Players overlay these with their
	// own glyph but keep the tile's background (computed per-cell below).
	waterFill := uv.Style{Fg: colorWaterRipple, Bg: colorWaterBg}
	shallowFill := uv.Style{Fg: colorShallowRip, Bg: colorShallowBg}
	sandFill := uv.Style{Fg: colorSandSpeck, Bg: colorSandBg}
	mudFill := uv.Style{Fg: colorMudSpeck, Bg: colorMudBg}
	dirtFill := uv.Style{Fg: colorDirtSpeck, Bg: colorDirtBg}
	rockFill := uv.Style{Fg: colorRockSpeck, Bg: colorRockBg}
	bushFill := uv.Style{Fg: colorBush, Bg: colorBushBg}
	stumpFill := uv.Style{Fg: colorStump, Bg: colorStumpBg}
	logFill := uv.Style{Fg: colorLog, Bg: colorLogBg}
	shroomFill := uv.Style{Fg: colorShroom, Bg: colorGrassBg}
	reedFill := uv.Style{Fg: colorReed, Bg: colorGrassBg}
	signPostFill := uv.Style{Fg: colorSignPost, Bg: colorGrassBg}
	signBoardFill := uv.Style{Fg: colorSignBoardInk, Bg: colorSignBoardBg}
	wellRimFill := uv.Style{Fg: colorWellRim, Bg: colorWellRimBg}
	wellWaterFill := uv.Style{Fg: colorWellWater, Bg: colorWellWaterBg}
	stoneFill := uv.Style{Fg: colorStone, Bg: colorStoneBg}
	jettyFill := uv.Style{Fg: colorJetty, Bg: colorJettyBg}
	grassFill := uv.Style{Fg: colorGrass, Bg: colorGrassBg}
	// Interior tiles (house cells) deliberately keep no background fill —
	// glyph on the terminal's default black reads better indoors. Walls are
	// brown to suggest timber; the floor is the warm wood glyph as before.
	wallFill := uv.Style{Fg: colorWallTimber}
	floorFill := uv.Style{Fg: colorFloor}
	trunkFill := uv.Style{Fg: colorTrunk, Bg: colorGrassBg}
	roofFill := uv.Style{Fg: colorRoofDark, Bg: colorRoofBg}
	ridgeFill := uv.Style{Bg: colorRidge}
	timberFill := uv.Style{Fg: colorTimberLine, Bg: colorTimberBg}
	windowFill := uv.Style{Bg: colorWindowBg}
	doorFill := uv.Style{Fg: colorDoorPlank, Bg: colorDoorBg}

	// grassCell textures a grass tile: one dark base green, with foreground
	// tuft glyphs scattered over it and the occasional flower. It's a pure
	// function of the world coordinate, so the texture is identical every
	// frame — no flicker, and nothing to store on the world itself.
	tuftStyle := uv.Style{Fg: colorGrassTuft, Bg: colorGrassBg}
	flowerA := uv.Style{Fg: colorFlowerYellow, Bg: colorGrassBg}
	flowerB := uv.Style{Fg: colorFlowerSalmon, Bg: colorGrassBg}
	grassCell := func(x, y int) (uv.Style, string) {
		h := grassHash(x, y)
		switch {
		case h%101 == 0:
			return flowerA, "*"
		case h%97 == 0:
			return flowerB, "*"
		}
		// Roughly 3 in 8 tiles get a tuft glyph; the rest are bare base green.
		switch h % 8 {
		case 0:
			return tuftStyle, "'"
		case 1:
			return tuftStyle, ","
		case 2:
			return tuftStyle, "\""
		default:
			return grassFill, " "
		}
	}

	// meadowCell textures the bright meadow grass: more tall-grass tufts and
	// far more wildflowers than the forest floor, in several colours. Same
	// deterministic-per-tile approach as grassCell.
	meadowFill := uv.Style{Fg: colorGrass, Bg: colorMeadowBg}
	meadowTuft := uv.Style{Fg: colorMeadowTuft, Bg: colorMeadowBg}
	flowerY := uv.Style{Fg: colorFlowerYellow, Bg: colorMeadowBg}
	flowerW := uv.Style{Fg: colorFlowerWhite, Bg: colorMeadowBg}
	flowerP := uv.Style{Fg: colorFlowerPurple, Bg: colorMeadowBg}
	flowerPk := uv.Style{Fg: colorFlowerPink, Bg: colorMeadowBg}
	meadowCell := func(x, y int) (uv.Style, string) {
		h := grassHash(x, y)
		switch {
		case h%17 == 0:
			return flowerY, "*"
		case h%41 == 0:
			return flowerW, "*"
		case h%53 == 0:
			return flowerP, "*"
		case h%61 == 0:
			return flowerPk, "*"
		}
		switch h % 5 {
		case 0, 1:
			return meadowTuft, "\""
		case 2:
			return meadowTuft, "'"
		default:
			return meadowFill, " "
		}
	}

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

			// Resolve the tile under this cell to a fill style plus its two
			// glyphs (most tiles only put a glyph on the left, leaving the
			// right an empty cell painted with the same background).
			fill := grassFill
			leftRune, rightRune := " ", " "
			switch curWorld.tiles[wy][wx] {
			case tileTree:
				// Trunk: a centred bar (two half-blocks meeting in the middle
				// of the tile) so grass shows at the sides. Large trees get a
				// chunkier full-width trunk. The leafy canopy above it is drawn
				// later by drawTrees.
				if treeVariant(wx, wy) == 2 {
					fill, leftRune, rightRune = trunkFill, "█", "█"
				} else {
					fill, leftRune, rightRune = trunkFill, "▐", "▌"
				}
			case tileWater:
				fill, leftRune = waterFill, "~"
			case tileShallow:
				fill, leftRune = shallowFill, "~"
			case tileSand:
				// Mostly bare tan with the occasional darker speck so the
				// beach isn't a flat block.
				fill = sandFill
				if grassHash(wx, wy)%5 == 0 {
					leftRune = "."
				}
			case tileMud:
				// Dark wet earth with a sparse lighter speck for texture.
				fill = mudFill
				if grassHash(wx, wy)%4 == 0 {
					leftRune = "."
				}
			case tileDirt:
				// Dry packed earth with the occasional pebble.
				fill = dirtFill
				if grassHash(wx, wy)%5 == 0 {
					leftRune = "."
				}
			case tileWall:
				fill, leftRune, rightRune = wallFill, "#", "#"
			case tileFloor:
				fill, leftRune = floorFill, ","
			case tileRoof:
				// Roof shingles: a uniform dark stipple over warm brown.
				fill, leftRune, rightRune = roofFill, "▒", "▒"
			case tileRidge:
				fill = ridgeFill // sunlit ridge — a clean lighter top edge
			case tileTimber:
				// Log wall: horizontal courses across both cells.
				fill, leftRune, rightRune = timberFill, "═", "═"
			case tileWindow:
				fill = windowFill // a warm glowing pane
			case tileDoor:
				// The cabin's front door: dark wood with a lighter central
				// plank seam. Walking into it triggers the teleport.
				fill, leftRune, rightRune = doorFill, "▐", "▌"
			case tileRock:
				fill, leftRune, rightRune = rockFill, "▒", "▒"
			case tileBush:
				fill, leftRune, rightRune = bushFill, "♣", "♣"
			case tileStump:
				fill, leftRune = stumpFill, "○" // rings on a brown stump
			case tileLog:
				fill, leftRune, rightRune = logFill, "═", "═"
			case tileShroom:
				fill, leftRune = shroomFill, "•" // a red cap on the grass
			case tileReed:
				fill, leftRune, rightRune = reedFill, "║", "║"
			case tileSignPost:
				fill, leftRune, rightRune = signPostFill, "▐", "▌"
			case tileSignBoard:
				fill, leftRune, rightRune = signBoardFill, "≡", "≡"
			case tileWellRim:
				fill, leftRune, rightRune = wellRimFill, "▒", "▒"
			case tileWellWater:
				fill, leftRune, rightRune = wellWaterFill, "▓", "▓"
			case tileStone:
				fill, leftRune, rightRune = stoneFill, "▓", "▓"
			case tileJetty:
				fill, leftRune, rightRune = jettyFill, "═", "═"
			case tileMeadow:
				fill, leftRune = meadowCell(wx, wy)
			default:
				fill, leftRune = grassCell(wx, wy)
			}

			// Players overlay the tile with their own glyph but keep the
			// tile's background, so there's no black gap around them.
			var left, right *uv.Cell
			switch {
			case wx == me.x && wy == me.y:
				left = &uv.Cell{Content: "@", Width: 1, Style: uv.Style{Fg: colorPlayerSelf, Bg: fill.Bg, Attrs: uv.AttrBold}}
				right = &uv.Cell{Content: " ", Width: 1, Style: uv.Style{Bg: fill.Bg}}
			case othersContains(others, wx, wy):
				left = &uv.Cell{Content: "@", Width: 1, Style: uv.Style{Fg: colorPlayerOther, Bg: fill.Bg, Attrs: uv.AttrBold}}
				right = &uv.Cell{Content: " ", Width: 1, Style: uv.Style{Bg: fill.Bg}}
			default:
				left = &uv.Cell{Content: leftRune, Width: 1, Style: fill}
				right = &uv.Cell{Content: rightRune, Width: 1, Style: fill}
			}
			canvas.SetCell(cx, y, left)
			canvas.SetCell(cx+1, y, right)
		}
	}

	// drawTrees overlays leafy canopies above tree trunks. Trees are ordinary
	// world tiles — only the trunk blocks movement — and the canopy is painted
	// on top of the base layer here, so players can walk *behind* the leaves.
	// A canopy cell is skipped when it lands on the local player, so you never
	// lose sight of your own @.
	// leafCell renders one foliage half-cell: a stipple glyph (light ░ / mid
	// ▒ / dark ▓) over the canopy fill, picked from a seed so the leaves look
	// dappled rather than a flat block.
	leafCell := func(seed uint32) *uv.Cell {
		glyph, fg := "▒", colorCanopy
		switch seed % 4 {
		case 0:
			glyph, fg = "░", colorCanopyLight
		case 1:
			glyph, fg = "▓", colorCanopyDark
		}
		return &uv.Cell{Content: glyph, Width: 1, Style: uv.Style{Fg: fg, Bg: colorCanopyBg}}
	}
	drawCanopyCell := func(wx, wy int) {
		if wx < 0 || wx >= curWorld.width || wy < 0 || wy >= curWorld.height {
			return
		}
		if wx == me.x && wy == me.y {
			return // keep our own avatar visible through the leaves
		}
		cellX := (wx - camX + worldOffsetTilesX) * 2
		cellY := wy - camY + worldOffsetTilesY
		if cellX < 0 || cellX+1 >= m.width || cellY < 0 || cellY >= viewportTilesH {
			return
		}
		// Seed each half-cell from the world coord so the dapple is stable and
		// the tile's two halves differ.
		h := grassHash(wx, wy)
		canvas.SetCell(cellX, cellY, leafCell(h))
		canvas.SetCell(cellX+1, cellY, leafCell(h*2654435761+1))
	}
	// Scan trunks across the viewport plus a margin (canopies reach up to
	// treeCanopyReach rows above their trunk and two tiles to either side).
	// Trees are sparse, so the scan is cheap.
	for wy := camY; wy < camY+viewportTilesH+treeCanopyReach && wy < curWorld.height; wy++ {
		if wy < 0 {
			continue
		}
		for wx := camX - 2; wx < camX+viewportTilesW+2 && wx < curWorld.width; wx++ {
			if wx < 0 || curWorld.tiles[wy][wx] != tileTree {
				continue
			}
			for _, off := range treeCanopies[treeVariant(wx, wy)] {
				drawCanopyCell(wx+off[0], wy+off[1])
			}
		}
	}

	cellName := "outside"
	switch me.worldID {
	case worldHouse:
		cellName = "house"
	case worldMeadow:
		cellName = "meadow"
	}
	// Count only players in our cell — the snapshot includes everyone.
	visibleCount := 0
	for _, info := range m.snapshot {
		if info.worldID == me.worldID {
			visibleCount++
		}
	}
	statusLine := lipgloss.NewStyle().Foreground(colorAmber).Bold(true).Render("gametest") +
		lipgloss.NewStyle().Foreground(colorAmberDim).Render(
			fmt.Sprintf("  [%s] %d here · pos (%d, %d)", cellName, visibleCount, me.x, me.y),
		)
	var helpText string
	switch m.mode {
	case inputModeSpeak:
		helpText = "enter to send · esc to cancel"
	case inputModeRename:
		helpText = "type a name · enter to set · esc to cancel"
	case inputModeRead:
		helpText = "esc to close"
	default:
		helpText = "arrows / wasd / hjkl to move · t to say · n to rename · esc to leave"
		if _, ok := nearbySign(me); ok {
			helpText = "i to read sign · " + helpText
		}
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
	if m.mode == inputModeRead {
		layers = append(layers, signModalLayer(m.readingText, m.width, m.height))
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

// grassHash maps a world coordinate to a stable pseudo-random value, used by
// grassCell to pick a grass shade / tuft. It's a plain integer hash (mix the
// two coords with large odd constants, then avalanche the bits) so the same
// tile always yields the same value — the grass texture is baked into the
// coordinate space rather than rolled per frame.
func grassHash(x, y int) uint32 {
	h := uint32(x)*73856093 ^ uint32(y)*19349663
	h ^= h >> 13
	h *= 2654435761
	h ^= h >> 16
	return h
}

// hasTileNear reports whether tile `want` sits within Chebyshev distance d of
// (x, y).
func hasTileNear(tiles [][]tile, x, y, d int, want tile) bool {
	for yy := y - d; yy <= y+d; yy++ {
		if yy < 0 || yy >= len(tiles) {
			continue
		}
		for xx := x - d; xx <= x+d; xx++ {
			if xx < 0 || xx >= len(tiles[yy]) {
				continue
			}
			if tiles[yy][xx] == want {
				return true
			}
		}
	}
	return false
}

// hasTreeNear reports whether any tree trunk sits within Chebyshev distance d
// of (x, y). Grove placement uses it to keep trunks spaced apart so their
// canopies stay distinct instead of fusing into one blob.
func hasTreeNear(tiles [][]tile, x, y, d int) bool {
	return hasTileNear(tiles, x, y, d, tileTree)
}

// scatterClutter sprinkles small decorative features across the open grass:
// boulders, bushes, stumps and fallen logs (which block movement), plus
// mushrooms and pond-side reeds (which don't). Everything is placed only on
// grass, so it stays clear of the paths, water, pond banks and the cabin. Kept
// deliberately sparse — a little reads as life, too much reads as noise.
func scatterClutter(w *worldGrid, r *rand.Rand) {
	isGrass := func(x, y int) bool {
		return x >= 1 && x < w.width-1 && y >= 1 && y < w.height-1 && w.tiles[y][x] == tileGrass
	}
	put := func(x, y int, t tile) {
		if isGrass(x, y) {
			w.tiles[y][x] = t
		}
	}
	// Boulders: scattered and spaced, occasionally in a pair.
	for i := 0; i < 30; i++ {
		x, y := r.Intn(w.width-2)+1, r.Intn(w.height-2)+1
		if !isGrass(x, y) || hasTileNear(w.tiles, x, y, 2, tileRock) {
			continue
		}
		put(x, y, tileRock)
		if r.Intn(3) == 0 {
			put(x+1, y, tileRock)
		}
	}
	// Bushes: mostly at the edges of groves, a few out in the open.
	for placed, tries := 0, 0; placed < 50 && tries < 600; tries++ {
		x, y := r.Intn(w.width-2)+1, r.Intn(w.height-2)+1
		if !isGrass(x, y) {
			continue
		}
		if !hasTreeNear(w.tiles, x, y, 3) && r.Intn(4) != 0 {
			continue
		}
		put(x, y, tileBush)
		placed++
	}
	// Stumps and short fallen logs near the groves.
	for i := 0; i < 12; i++ {
		x, y := r.Intn(w.width-2)+1, r.Intn(w.height-2)+1
		if isGrass(x, y) && hasTreeNear(w.tiles, x, y, 3) {
			put(x, y, tileStump)
		}
	}
	for i := 0; i < 7; i++ {
		x, y := r.Intn(w.width-2)+1, r.Intn(w.height-2)+1
		if !hasTreeNear(w.tiles, x, y, 3) {
			continue
		}
		for k := 0; k < 2+r.Intn(2); k++ { // a 2–3 tile log lying east-west
			put(x+k, y, tileLog)
		}
	}
	// Mushrooms in the shade near trees (non-blocking).
	for placed, tries := 0, 0; placed < 28 && tries < 500; tries++ {
		x, y := r.Intn(w.width-2)+1, r.Intn(w.height-2)+1
		if isGrass(x, y) && hasTileNear(w.tiles, x, y, 2, tileTree) {
			put(x, y, tileShroom)
			placed++
		}
	}
	// Reeds on the grass right at the pond margins (non-blocking).
	for y := 1; y < w.height-1; y++ {
		for x := 1; x < w.width-1; x++ {
			if w.tiles[y][x] == tileGrass && hasTileNear(w.tiles, x, y, 1, tileMud) && r.Intn(2) == 0 {
				w.tiles[y][x] = tileReed
			}
		}
	}
}

// treeCanopies lists, per size variant, the canopy tile offsets relative to
// the trunk tile (negative dy is above the trunk). The trunk at (0,0) is the
// only blocking tile; every offset here is a decorative overlay drawn on top
// of the base layer. treeCanopyReach is how far up the tallest canopy goes,
// used to decide how far below the viewport to scan for trunks.
var treeCanopies = [3][][2]int{
	// small — a single head above the trunk
	{{0, -1}},
	// medium — a 3-wide cap with a crown
	{{0, -1}, {-1, -1}, {1, -1}, {0, -2}},
	// large — a 5-wide body tapering to a crown
	{
		{-2, -1}, {-1, -1}, {0, -1}, {1, -1}, {2, -1},
		{-2, -2}, {-1, -2}, {0, -2}, {1, -2}, {2, -2},
		{-1, -3}, {0, -3}, {1, -3},
		{0, -4},
	},
}

const treeCanopyReach = 4

// treeVariant picks a tree's size variant from its trunk coordinate so it's
// stable frame to frame. Roughly 40% small, 40% medium, 20% large. The y
// offset decorrelates it from the grass-texture hashing at the same tile.
func treeVariant(x, y int) int {
	switch grassHash(x, y+777) % 5 {
	case 0, 1:
		return 0
	case 2, 3:
		return 1
	default:
		return 2
	}
}
