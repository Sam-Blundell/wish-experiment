package main

import (
	"fmt"
	"image/color"
	"log"
	"math"
	"math/rand"
	"strings"
	"sync"
	"time"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
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
	gameMinWidth     = 80
	gameMinHeight    = 24
	worldWidth       = 120
	worldHeight      = 60
	gameMaxPlayers   = 20
	cueDuration      = 4 * time.Second        // how long a speaker's nameplate blinks after they talk
	cueBlinkInterval = 500 * time.Millisecond // the !↔name toggle period during that blink
	tickInterval     = 100 * time.Millisecond // world heartbeat: rate the hub coalesces state changes into one broadcast
	moveRepeatDelay  = 200 * time.Millisecond // enhanced terminals: pause after a press before the tick auto-glides a held key
	dayCycle         = 20 * time.Minute       // full length of one accelerated day→night→day cycle; feel knob: shorter = more cycling, longer = slower world clock
	dayStages        = 8                      // discrete darkness levels around the cycle; the world re-renders only on a stage flip
	maxNightDim      = 0.62                   // deepest darken fraction at the dead of night (0 = none, 1 = black)
	maxWarmth        = 0.35                   // strongest warm (sunset) cast, at the middle of a dawn/dusk ramp
	warmGreenCut     = 0.18                   // warm pulls green down only a little (× warmth) — keep low or warm/brown tiles turn red
	warmBlueCut      = 0.80                   // …and blue down more (× warmth) — this is what warms the scene without reddening it
	lightRadius      = 6.5                    // tiles a light source reaches; spill falls off (smoothstep) to 0 here
	lightWarmth      = 0.40                   // warm cast a light source adds at its centre
	maxLift          = 0.65                   // most of the night's darkness a light can lift (1 = full daylight at the centre; <1 keeps it a glow)
	glowStrength     = 0.85                   // how far a light source brightens toward its glow colour at deepest night
	sayCharCap       = 200
	nickCharCap      = 16
	noteCharCap      = 140
	maxGameChat      = 200 // per-world chat backlog kept in memory
	chatLogLines     = 3   // chat lines shown in the docked (unexpanded) pane
	composeModalW    = 40  // input width inside the rename / note modal
	maxViewTilesW    = 56  // cap on the visible map width in tiles; larger terminals get a centred, framed column
	maxViewTilesH    = 30  // cap on the visible map height in tiles
)

// inputMode enumerates the screen's keyboard modes. `iota` gives each
// constant a successive integer value starting at 0; defining a named
// type makes them distinct from plain ints so we can't accidentally pass
// an arbitrary number where a mode is expected.
type inputMode int

const (
	inputModeMove    inputMode = iota // arrow keys drive the player
	inputModeSpeak                    // typing a chat message
	inputModeRename                   // typing a new nickname
	inputModeRead                     // reading a signpost or note modal
	inputModeNote                     // typing a note to drop on the ground
	inputModeHelp                     // the controls modal is open
	inputModeBigChat                  // the full-screen chat view is open
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
	colorPanelBg     = lipgloss.Color("235") // modal / panel background — dark grey

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
	colorWindowGlow = lipgloss.Color("#fff2c4") // window glow target as night falls (brighter, warmer)
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

	colorNote = lipgloss.Color("#e8d8a0") // dropped-note marker — parchment
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

// gameHelpText builds the controls list shown in the help modal (raised with
// `?` or by typing `/help`). Built rather than a const so the key/description
// columns stay aligned.
func gameHelpText() string {
	keys := [][2]string{
		{"move", "arrows / wasd / hjkl"},
		{"t", "say a line"},
		{"c", "open full chat"},
		{"/", "start a command"},
		{"i", "read a sign or note"},
		{"x", "remove your note (reading)"},
		{"?", "show this help"},
		{"esc", "leave the game"},
	}
	cmds := [][2]string{
		{"/nick", "set your nickname"},
		{"/note", "leave a note"},
		{"/help", "show this help"},
	}
	var b strings.Builder
	b.WriteString("Controls\n\n")
	for _, r := range keys {
		fmt.Fprintf(&b, "%-9s %s\n", r[0], r[1])
	}
	b.WriteString("\nIn chat:\n")
	for _, r := range cmds {
		fmt.Fprintf(&b, "%-9s %s\n", r[0], r[1])
	}
	return strings.TrimRight(b.String(), "\n")
}

// composeModalLayer builds the centred panel for editing a name or note. The
// input is passed already-rendered (the shared textarea); we just frame it with
// a title and a hint. Same panel look as signModalLayer.
func composeModalLayer(title, inputView, hint string, width, height int) *lipgloss.Layer {
	panelBg := colorPanelBg
	t := lipgloss.NewStyle().Foreground(colorAmber).Bold(true).Background(panelBg).Render(title)
	h := lipgloss.NewStyle().Foreground(colorAmberDim).Background(panelBg).Render(hint)
	box := lipgloss.NewStyle().
		Border(lipgloss.DoubleBorder()).
		BorderForeground(colorAmber).
		BorderBackground(panelBg).
		Background(panelBg).
		Padding(1, 2).
		Render(t + "\n\n" + inputView + "\n\n" + h)
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

// signModalLayer builds the centred panel shown while reading a sign or note.
// removable adds an "x to remove" hint to the footer (used when the reader owns
// the note they're looking at).
func signModalLayer(text string, width, height int, removable bool) *lipgloss.Layer {
	boxW := 40
	if max := width - 8; boxW > max {
		boxW = max
	}
	if boxW < 10 {
		boxW = 10
	}
	panelBg := colorPanelBg
	body := lipgloss.NewStyle().Foreground(colorCream).Background(panelBg).Width(boxW).Render(text)
	footerText := "esc to close"
	if removable {
		footerText = "esc to close · x to remove"
	}
	footer := lipgloss.NewStyle().Foreground(colorAmberDim).Background(panelBg).Render(footerText)
	box := lipgloss.NewStyle().
		Border(lipgloss.DoubleBorder()).
		BorderForeground(colorAmber).
		BorderBackground(panelBg).
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
// `messageExpires` is set when the player speaks; until it passes, the renderer
// flashes a "just spoke" cue on their nameplate. The message text itself goes to
// the world's chat log, not onto the player. Lives under the game's mutex.
type gamePlayer struct {
	send           chan gameSnapshot
	ip             string
	nick           string
	worldID        int // which world the player is currently in
	x, y           int
	messageExpires time.Time
	// Movement intent for terminals that report key release (the enhanced path):
	// the direction currently held, advanced one tile per tick until released.
	// heldSince gates a short delay before the tick starts auto-gliding, so a
	// quick tap moves exactly one tile. Zero direction = not moving. Guarded by mu.
	heldDX, heldDY int
	heldSince      time.Time
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
	messageExpires time.Time // non-zero & future ⇒ flash the "just spoke" cue
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
	mu           sync.Mutex
	players      map[*gamePlayer]struct{}
	notes        []note                 // notes dropped on the ground; guarded by mu, persisted via store
	store        NoteStore              // persistence backend for notes; wired up in notes.go's init
	chat         map[int][]gameChatLine // chat backlog per world (keyed by worldID); guarded by mu
	dirty        bool                   // state changed since the last tick; the tick loop broadcasts and clears it
	lastDayStage int                    // last day/night stage broadcast; a change re-tints the world (see tickOnce)
}

var theGame = &game{
	players: make(map[*gamePlayer]struct{}),
	chat:    make(map[int][]gameChatLine),
}

// gameChatLine is one line in a world's chat log. A blank `from` marks a system
// line (arrivals, departures) rather than something a player said.
type gameChatLine struct {
	from string
	text string
}

// appendChat adds a line to a world's chat backlog, trimming to maxGameChat.
// Caller holds g.mu.
func (g *game) appendChat(worldID int, line gameChatLine) {
	msgs := append(g.chat[worldID], line)
	if len(msgs) > maxGameChat {
		msgs = msgs[len(msgs)-maxGameChat:]
	}
	g.chat[worldID] = msgs
}

// chatFor returns a private copy of a world's chat backlog, safe to read without
// the lock. Pulled by each screen when a snapshot arrives, like allNotes.
func (g *game) chatFor(worldID int) []gameChatLine {
	g.mu.Lock()
	defer g.mu.Unlock()
	src := g.chat[worldID]
	out := make([]gameChatLine, len(src))
	copy(out, src)
	return out
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
	g.appendChat(p.worldID, gameChatLine{text: p.displayName() + " arrived"})
	g.markDirty()
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
	g.appendChat(p.worldID, gameChatLine{text: p.displayName() + " left"})
	g.markDirty()
}

// move applies a keypress-driven step: the player pressed a direction. It takes
// the lock and delegates to moveLocked. (The tick's held-movement calls
// moveLocked directly, since it already holds the lock.)
func (g *game) move(p *gamePlayer, dx, dy int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.moveLocked(p, dx, dy)
}

// moveLocked validates the requested step and applies it if legal. Players
// can't walk through tiles that aren't walkable or onto another player.
// Stepping into a door tile teleports the player to the door's target. Marks
// the world dirty on a successful step. Caller holds g.mu.
func (g *game) moveLocked(p *gamePlayer, dx, dy int) {
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
		g.markDirty()
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
	g.markDirty()
}

// setIntent records the direction a player is holding (enhanced terminals only)
// and stamps the press time. The tick then advances them one tile per beat,
// after moveRepeatDelay, until the key is released. Called once per physical
// press — key repeats are ignored, since the tick drives continuation.
func (g *game) setIntent(p *gamePlayer, dx, dy int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.players[p]; !ok {
		return
	}
	p.heldDX, p.heldDY = dx, dy
	p.heldSince = time.Now()
}

// clearIntent stops held movement, but only if the released direction is the one
// being held. Releasing a key already superseded by a newer press is a no-op —
// last press wins, and we keep gliding in the newer direction.
func (g *game) clearIntent(p *gamePlayer, dx, dy int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if p.heldDX == dx && p.heldDY == dy {
		p.heldDX, p.heldDY = 0, 0
	}
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
	g.markDirty()
}

// say posts a chat line to the speaker's current world and blinks a "just
// spoke" cue on their nameplate for cueDuration. The text lives in the world's
// chat log (everyone in that cell sees it); the cue is just the visual ping.
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
	g.appendChat(p.worldID, gameChatLine{from: p.displayName(), text: msg})
	p.messageExpires = time.Now().Add(cueDuration)
	expires := p.messageExpires
	g.markDirty()
	g.mu.Unlock()

	// Mark dirty once the cue lapses so the tick clears the flash even if the
	// area is idle (nothing else would trigger a redraw). Skip if a newer say()
	// has since pushed the expiry forward.
	go func() {
		time.Sleep(cueDuration + 10*time.Millisecond)
		g.mu.Lock()
		defer g.mu.Unlock()
		if _, ok := g.players[p]; !ok {
			return
		}
		if p.messageExpires.Equal(expires) {
			g.markDirty()
		}
	}()
}

// addNote drops a note on the ground at the player's current tile. Anyone in the
// same world then sees a marker there and can stand on it to read it. Notes are
// this codebase's first piece of mutable, shared *world* state (players were the
// only thing that changed before) and the first thing we persist — see notes.go.
func (g *game) addNote(p *gamePlayer, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.players[p]; !ok {
		return
	}
	// At most one note per tile: if this spot's already taken, leave the
	// existing note be. Simplest rule, and it stops one player papering a tile.
	for _, n := range g.notes {
		if n.WorldID == p.worldID && n.X == p.x && n.Y == p.y {
			return
		}
	}
	g.notes = append(g.notes, note{
		WorldID: p.worldID, X: p.x, Y: p.y,
		Text: text, Author: p.displayName(), AuthorIP: p.ip, Created: time.Now(),
	})
	// Persist while we still hold the lock. Doing it here — rather than in a
	// background goroutine — keeps saves ordered and means the file on disk
	// always matches what we've broadcast. The write is a tiny file and notes
	// are dropped rarely, so the I/O cost under the lock is negligible here;
	// see HARD-EDGES.md for when that stops being true.
	if g.store != nil {
		if err := g.store.Save(g.notes); err != nil {
			log.Printf("notes: could not save: %v", err)
		}
	}
	g.markDirty()
}

// removeNote deletes the note on the player's current tile — but only if they
// placed it (matched by IP; see note.AuthorIP). It's a no-op otherwise, so the
// caller can fire it unconditionally and let ownership decide.
func (g *game) removeNote(p *gamePlayer) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.players[p]; !ok {
		return
	}
	for i, n := range g.notes {
		if n.WorldID == p.worldID && n.X == p.x && n.Y == p.y {
			if n.AuthorIP != p.ip {
				return // someone else's note — leave it be
			}
			// Delete index i by shifting the tail down one. Mutating the
			// backing array in place is safe here because allNotes hands out
			// copies — no reader is holding this slice.
			g.notes = append(g.notes[:i], g.notes[i+1:]...)
			if g.store != nil {
				if err := g.store.Save(g.notes); err != nil {
					log.Printf("notes: could not save: %v", err)
				}
			}
			g.markDirty()
			return
		}
	}
}

// allNotes returns a private copy of every note, safe to read without the lock.
// Notes change rarely, so screens pull them this way when a snapshot arrives
// rather than carrying them inside every per-move snapshot (which would re-copy
// the note text on every single step). The copy lets the caller read freely
// while the hub keeps mutating g.notes.
func (g *game) allNotes() []note {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]note, len(g.notes))
	copy(out, g.notes)
	return out
}

// markDirty records that shared state changed. Rather than broadcasting a fresh
// snapshot on every single change (a move, a chat line, a note), callers just
// flag the world dirty and the tick loop coalesces a burst of changes into one
// broadcast on its next beat. This is what stops one player's movement from
// forcing every other player to re-render on every step. Caller holds g.mu.
func (g *game) markDirty() {
	g.dirty = true
}

// tick is the world's heartbeat: one goroutine, started once at boot (see main),
// that wakes every tickInterval and — only if something changed since the last
// beat — broadcasts a single snapshot to everyone. Coalescing here caps the
// broadcast (and therefore re-render) rate no matter how many players act or how
// fast, and it's the loop ambient behaviour (day/night, critters) will hang off
// later. Idle beats are nearly free: with nothing dirty we just take the lock,
// see it's clean, and return.
func (g *game) tick() {
	t := time.NewTicker(tickInterval)
	defer t.Stop()
	for range t.C {
		g.tickOnce()
	}
}

// tickOnce is a single beat, split out from the loop so it can be driven
// deterministically in tests. If anything changed since the last beat, send one
// snapshot to everyone and clear the flag.
func (g *game) tickOnce() {
	g.mu.Lock()
	defer g.mu.Unlock()
	// Advance held-movement intents (enhanced terminals): one tile per beat in
	// the direction each player holds, once past the initial moveRepeatDelay. A
	// successful step marks dirty, so the broadcast below carries it. This is what
	// makes a held key a steady, uniform glide — its speed is the tick rate, not
	// the terminal's key-repeat rate.
	now := time.Now()
	for p := range g.players {
		if (p.heldDX != 0 || p.heldDY != 0) && now.Sub(p.heldSince) >= moveRepeatDelay {
			g.moveLocked(p, p.heldDX, p.heldDY)
		}
		if p.messageExpires.After(now) {
			g.dirty = true // keep a "just spoke" nameplate blinking while its cue is live
		}
	}
	if st := dayStage(); st != g.lastDayStage {
		g.lastDayStage = st
		g.dirty = true // a day/night stage flip re-tints the world, even in an idle area
	}
	if g.dirty {
		g.broadcast()
		g.dirty = false
	}
}

// broadcast snapshots state and pushes it to every player's send channel. Called
// only by the tick loop now — state changes flag dirty via markDirty rather than
// broadcasting directly. Caller holds g.mu. Non-blocking on a full channel — we
// drop rather than stall the whole hub on one slow client. The next successful
// send carries the latest state anyway, so a dropped snapshot is not a lost update.
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
	notes       []note         // this world's notes, refreshed when a snapshot arrives
	chat        []gameChatLine // this world's chat backlog, refreshed when a snapshot arrives
	chatVP      viewport.Model // big-chat scroll view; size/content synced in syncChatVP, scroll persists here
	mode        inputMode      // current keyboard mode (move / speak / rename / read / note)
	input       textarea.Model // wrapping compose input for speak, rename and note modes
	readingText string         // text shown in the modal (sign or note)
	enhanced    bool           // terminal reports key release/repeat → smooth held-movement (else per-press)
	lastMove    time.Time      // fallback path only: time of the last per-press move, to rate-cap movement
	camX, camY  int            // dead-zone camera: world coord of the viewport's top-left, eased as the player moves
}

func newGameScreen(s ssh.Session, ip string, width, height int, enhanced bool) Screen {
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

	// Shared compose widget for the typing modes. It soft-wraps and grows from
	// 1 to 3 rows as the message gets longer; Enter sends (intercepted in
	// Update, so the textarea never inserts a newline — messages stay a single
	// logical line). Prompt, width and char limit are set per-mode on entry.
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.DynamicHeight = true
	ta.MinHeight = 1
	ta.MaxHeight = 3
	styles := ta.Styles()
	styles.Focused.Prompt = lipgloss.NewStyle().Foreground(colorAmber).Bold(true)
	styles.Focused.Text = lipgloss.NewStyle().Foreground(colorCream)
	styles.Focused.CursorLine = lipgloss.NewStyle() // no cursor-line highlight
	styles.Cursor.Color = colorAmber
	ta.SetStyles(styles)

	// Scrollable viewport for the big-chat log. New() enables mouse-wheel; the
	// panel background keeps its gaps grey to match the modal.
	vp := viewport.New()
	vp.Style = lipgloss.NewStyle().Background(colorPanelBg)
	vp.MouseWheelDelta = 3

	gs := gameScreen{
		width:    width,
		height:   height,
		player:   p,
		snapshot: theGame.snapshot(),
		notes:    theGame.allNotes(),
		chat:     theGame.chatFor(worldOutdoor),
		input:    ta,
		chatVP:   vp,
		enhanced: enhanced,
	}
	gs.centerCamera() // start centred on the spawn tile
	return gs
}

func (m gameScreen) title() string { return "game" }

func (m gameScreen) Init() tea.Cmd {
	return gameWaitForSnap(m.player.send)
}

// moveDir maps a movement key (arrow, hjkl, or wasd) to a unit step. ok is false
// for any other key. Shared by the press handler (which direction to step / hold)
// and the release handler (which held direction to clear).
func moveDir(s string) (dx, dy int, ok bool) {
	switch s {
	case "up", "k", "w":
		return 0, -1, true
	case "down", "j", "s":
		return 0, 1, true
	case "left", "h", "a":
		return -1, 0, true
	case "right", "l", "d":
		return 1, 0, true
	}
	return 0, 0, false
}

// viewportTiles is the size of the map view in world-tiles, at rest (one divider
// row sits below it). The camera is sized against this fixed value rather than
// the mode-dependent height, so it doesn't lurch when the compose bar grows.
func (m gameScreen) viewportTiles() (w, h int) {
	w = m.width / 2
	if w > maxViewTilesW {
		w = maxViewTilesW
	}
	h = m.height - chatLogLines - 1
	if h > maxViewTilesH {
		h = maxViewTilesH
	}
	return w, h
}

// colW is the column's width in terminal cells (each tile is two cells). The
// docked compose bar and chat pane size to this so they match the capped map.
func (m gameScreen) colW() int {
	w, _ := m.viewportTiles()
	return w * 2
}

// dayPhase is the single source of truth for "what time is it" — a value in
// [0,1) around the clock (0 = the cycle's darkest point). THIS is the swap seam:
// accelerated for now (wall-clock modulo dayCycle). To switch to real wall-clock
// day/night later, change only this body to (seconds since local midnight)/86400
// — everything downstream is unchanged.
func dayPhase() float64 {
	return float64(time.Now().UnixNano()%int64(dayCycle)) / float64(dayCycle)
}

// dayDim is the night-darkness curve over the cycle: a trapezoid that sits at
// full day or full night for most of it, with short dawn/dusk ramps between —
// so the world spends the majority of the cycle settled, not mid-transition.
// 0 = full day, 1 = deepest night; phase 0 is midnight.
func dayDim(phase float64) float64 {
	const dayFrac, nightFrac = 0.40, 0.40      // plateaus — 80% of the cycle
	const ramp = (1 - dayFrac - nightFrac) / 2 // dawn / dusk, 0.10 each
	switch {
	case phase < nightFrac/2: // just after midnight — still full night
		return 1
	case phase < nightFrac/2+ramp: // dawn: night → day
		return 1 - (phase-nightFrac/2)/ramp
	case phase < nightFrac/2+ramp+dayFrac: // full day
		return 0
	case phase < nightFrac/2+ramp+dayFrac+ramp: // dusk: day → night
		return (phase - (nightFrac/2 + ramp + dayFrac)) / ramp
	default: // before midnight — full night again
		return 1
	}
}

// dayStage quantises the darkness curve to dayStages discrete levels (0 = day,
// dayStages-1 = deepest night), so the world re-renders only when the level
// flips — never during the long settled plateaus.
func dayStage() int {
	return int(dayDim(dayPhase())*float64(dayStages-1) + 0.5)
}

// dayTint returns the night darkening and the dawn/dusk warmth for the current
// stage: dim ramps 0→maxNightDim toward midnight; warmth peaks mid-transition
// (dawn/dusk) and falls to 0 at both full day and full night — so the ramps read
// like a warm sunrise/sunset rather than a flat dimming.
func dayTint() (dim, warmth float64) {
	frac := float64(dayStage()) / float64(dayStages-1) // 0 = day … 1 = deepest night
	dim = frac * maxNightDim
	warmth = (1 - math.Abs(2*frac-1)) * maxWarmth
	return
}

// timeOfDay is a short label for the divider — a plain-language clue for why the
// colours are shifting. day/night for the plateaus, dawn/dusk for the ramps
// (told apart by which side of noon we're on). Changes only on a stage flip, so
// it costs no extra renders.
func timeOfDay() string {
	switch st := dayStage(); {
	case st == 0:
		return "day"
	case st == dayStages-1:
		return "night"
	case dayPhase() < 0.5: // morning side, brightening
		return "dawn"
	default: // evening side, darkening
		return "dusk"
	}
}

// mix blends colour a toward b by f in [0,1] — a single linear step (lipgloss's
// Blend1D is gradient-oriented, so we lerp the channels ourselves). RGBA() gives
// 16-bit channels; /257 brings them back to 8-bit.
func mix(a, b color.Color, f float64) color.Color {
	ar, ag, ab, _ := a.RGBA()
	br, bg, bb, _ := b.RGBA()
	ch := func(x, y uint32) uint8 { return uint8((float64(x)*(1-f) + float64(y)*f) / 257) }
	return lipgloss.RGBColor{R: ch(ar, br), G: ch(ag, bg), B: ch(ab, bb)}
}

// warmMul warms a colour by pulling its cool channels down (green a little, blue
// more) in proportion to warmth — a "warm light" tint. Crucially it *scales*
// each channel rather than blending toward one orange, so the contrast between
// tiles (what keeps the path readable) is preserved. The old blend-toward-orange
// collapsed that and washed detail out at dawn/dusk.
func warmMul(c color.Color, warmth float64) color.Color {
	r, g, b, _ := c.RGBA()
	gs := math.Max(0, 1-warmth*warmGreenCut)
	bs := math.Max(0, 1-warmth*warmBlueCut)
	return lipgloss.RGBColor{
		R: uint8(r >> 8),
		G: uint8(uint32(float64(g)*gs) >> 8),
		B: uint8(uint32(float64(b)*bs) >> 8),
	}
}

// tintNight applies the night look to a terrain style: darken toward black by
// dim, then a warm dawn/dusk cast by warmth. Unset colours are left alone. Only
// the world (terrain + canopy) is tinted, so UI chrome and players stay bright.
func tintNight(s uv.Style, dim, warmth float64) uv.Style {
	f := func(c color.Color) color.Color {
		c = lipgloss.Darken(c, dim)
		if warmth > 0 {
			c = warmMul(c, warmth)
		}
		return c
	}
	if s.Fg != nil {
		s.Fg = f(s.Fg)
	}
	if s.Bg != nil {
		s.Bg = f(s.Bg)
	}
	return s
}

// glowNight makes a light source brighten as night falls instead of dimming with
// the world: blend toward colorWindowGlow by dim, so a window reads as lit
// against the dark. (dim is 0 by day, so this is a no-op then.)
func glowNight(s uv.Style, dim float64) uv.Style {
	g := dim * glowStrength
	if s.Fg != nil {
		s.Fg = mix(s.Fg, colorWindowGlow, g)
	}
	if s.Bg != nil {
		s.Bg = mix(s.Bg, colorWindowGlow, g)
	}
	return s
}

// isStructure reports whether a tile is part of a building's solid body, so
// light pools on the ground around it rather than washing over its roof and
// walls. Windows are handled separately (they glow); doors and floors are
// openings/ground, so they're not exempt.
func isStructure(t tile) bool {
	switch t {
	case tileRoof, tileRidge, tileTimber, tileWall:
		return true
	}
	return false
}

// clampCam eases one axis of the camera with the dead-zone rule: keep the player
// inside a central band of the viewport, scrolling only when they push past it,
// and clamp to the world edges. If the world fits the viewport (max <= 0) the
// camera pins to 0 and View centres it — that's the fixed-camera room, for free.
func clampCam(cam, pos, vp, world int) int {
	max := world - vp
	if max <= 0 {
		return 0
	}
	margin := vp / 4 // dead-zone = the central half; smaller = a tighter follow
	if s := pos - cam; s < margin {
		cam = pos - margin
	} else if s > vp-1-margin {
		cam = pos - (vp - 1 - margin)
	}
	if cam < 0 {
		cam = 0
	}
	if cam > max {
		cam = max
	}
	return cam
}

// centerCam puts the player in the middle of the viewport (clamped). Used when
// there's no sensible previous camera to ease from — spawning, or stepping
// through a door into a different world.
func centerCam(pos, vp, world int) int {
	max := world - vp
	if max <= 0 {
		return 0
	}
	cam := pos - vp/2
	if cam < 0 {
		cam = 0
	}
	if cam > max {
		cam = max
	}
	return cam
}

// updateCamera eases the camera toward the local player (dead-zone). Called when
// our position might have changed (a snapshot) or the viewport resized.
func (m *gameScreen) updateCamera() {
	me := m.snapshot[m.player]
	w := worlds[me.worldID]
	vpW, vpH := m.viewportTiles()
	m.camX = clampCam(m.camX, me.x, vpW, w.width)
	m.camY = clampCam(m.camY, me.y, vpH, w.height)
}

// centerCamera snaps the camera to centre the local player. Used at spawn and on
// a world change, where easing from the old camera would be meaningless.
func (m *gameScreen) centerCamera() {
	me := m.snapshot[m.player]
	w := worlds[me.worldID]
	vpW, vpH := m.viewportTiles()
	m.camX = centerCam(me.x, vpW, w.width)
	m.camY = centerCam(me.y, vpH, w.height)
}

func (m gameScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		switch m.mode {
		case inputModeSpeak:
			m.input.SetWidth(composeInputWidth(m.colW())) // keep the chat bar's wrap-width in sync
		case inputModeBigChat:
			m.input.SetWidth(bigChatContentWidth(m.width))
			m.syncChatVPStick()
		}
		m.updateCamera() // re-clamp to the new viewport (a now-smaller world re-centres)
	case gameSnapshotMsg:
		// Adopt the new state and re-arm the receiver so the *next* snapshot
		// also reaches us. Notes aren't in the snapshot (they change rarely and
		// we don't want to copy note text on every move), so we re-pull them
		// from the hub whenever anything changes — a placement always triggers
		// a broadcast, so this refresh sees it.
		prevWorld, hadPrev := 0, false
		if info, ok := m.snapshot[m.player]; ok {
			prevWorld, hadPrev = info.worldID, true
		}
		m.snapshot = gameSnapshot(msg)
		m.notes = theGame.allNotes()
		me := msg[m.player]
		m.chat = theGame.chatFor(me.worldID)
		// Ease the camera toward us, but snap-centre on a world change (a door) —
		// easing from the old world's camera would be meaningless.
		if hadPrev && prevWorld == me.worldID {
			m.updateCamera()
		} else {
			m.centerCamera()
		}
		if m.mode == inputModeBigChat {
			m.syncChatVPStick()
		}
		return m, gameWaitForSnap(m.player.send)
	case tea.MouseWheelMsg:
		// Trackpad / mouse-wheel scrolls the big-chat log.
		if m.mode == inputModeBigChat {
			m.syncChatVP()
			m.chatVP, _ = m.chatVP.Update(msg)
		}
		return m, nil
	case tea.KeyboardEnhancementsMsg:
		// Late arrival (usually it lands before the game is entered and reaches us
		// via newGameScreen). Update our copy so held-movement turns on.
		m.enhanced = msg.SupportsEventTypes()
		return m, nil
	case tea.KeyReleaseMsg:
		// Only enhanced terminals send these. In movement mode, releasing the held
		// direction ends the glide. We swallow releases in every mode so enabling
		// the protocol can't leak release events into the text input.
		if m.mode == inputModeMove && m.enhanced {
			if dx, dy, ok := moveDir(msg.String()); ok {
				theGame.clearIntent(m.player, dx, dy)
			}
		}
		return m, nil
	case tea.KeyPressMsg:
		// The controls modal swallows the next key, then closes.
		if m.mode == inputModeHelp {
			m.mode = inputModeMove
			return m, nil
		}
		// Reading a signpost or note: the modal swallows input until dismissed.
		// `x` removes the note (a no-op unless it's yours, so pressing it while
		// reading a sign just closes the modal).
		if m.mode == inputModeRead {
			switch msg.String() {
			case "esc", "i", "enter", "q", " ":
				m.mode = inputModeMove
				m.readingText = ""
			case "x":
				theGame.removeNote(m.player)
				m.mode = inputModeMove
				m.readingText = ""
			}
			return m, nil
		}
		// Non-move-mode key handling takes precedence: while composing,
		// almost every key goes into the input rather than the movement
		// system. Esc cancels, enter commits the appropriate action.
		if m.mode != inputModeMove {
			// In the big chat, PgUp/PgDn page the log viewport (intercepted
			// before the input, which gets the rest of the keys).
			if m.mode == inputModeBigChat {
				switch msg.String() {
				case "pgup", "pgdown":
					m.syncChatVP()
					m.chatVP, _ = m.chatVP.Update(msg)
					return m, nil
				}
			}
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
				case inputModeSpeak, inputModeBigChat:
					// The chat bar (inline or full-screen) doubles as the command
					// line. A leading slash runs a command: `/nick` and `/note`
					// with no argument open their modal, with an argument they act.
					cmd := strings.TrimSpace(text)
					switch {
					case cmd == "/help":
						m.mode = inputModeHelp
					case cmd == "/nick":
						m.mode = inputModeRename
						m.input.SetPromptFunc(0, firstLinePrompt(""))
						m.setInputInPanel(true)
						m.input.CharLimit = nickCharCap
						m.input.SetWidth(composeModalW)
						m.input.SetValue(getNick(m.player.ip))
						m.input.CursorEnd()
						return m, m.input.Focus()
					case strings.HasPrefix(cmd, "/nick "):
						theGame.rename(m.player, strings.TrimPrefix(cmd, "/nick "))
					case cmd == "/note":
						m.mode = inputModeNote
						m.input.SetPromptFunc(0, firstLinePrompt(""))
						m.setInputInPanel(true)
						m.input.CharLimit = noteCharCap
						m.input.SetWidth(composeModalW)
						return m, m.input.Focus()
					case strings.HasPrefix(cmd, "/note "):
						theGame.addNote(m.player, strings.TrimPrefix(cmd, "/note "))
					default:
						theGame.say(m.player, text)
					}
				case inputModeRename:
					theGame.rename(m.player, text)
				case inputModeNote:
					theGame.addNote(m.player, text)
				}
				if mode == inputModeBigChat && m.mode == inputModeMove {
					m.mode = inputModeBigChat
					m.syncChatVP() // the chat grew with our message
					m.chatVP.GotoBottom()
					return m, m.input.Focus()
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
			m.input.SetPromptFunc(5, firstLinePrompt("say> "))
			m.setInputInPanel(false)
			m.input.CharLimit = sayCharCap
			m.input.SetWidth(composeInputWidth(m.colW()))
			return m, m.input.Focus()
		case "/":
			// Same as `t`, but pre-fill a slash so a command is one keypress away.
			m.mode = inputModeSpeak
			m.input.SetPromptFunc(5, firstLinePrompt("say> "))
			m.setInputInPanel(false)
			m.input.CharLimit = sayCharCap
			m.input.SetWidth(composeInputWidth(m.colW()))
			m.input.SetValue("/")
			m.input.CursorEnd()
			return m, m.input.Focus()
		case "c":
			// Open the big chat (modal over the map), at the bottom of the log.
			m.mode = inputModeBigChat
			m.input.SetPromptFunc(2, firstLinePrompt("> "))
			m.setInputInPanel(true)
			m.input.CharLimit = sayCharCap
			m.input.SetWidth(bigChatContentWidth(m.width))
			m.syncChatVP()
			m.chatVP.GotoBottom()
			return m, m.input.Focus()
		case "i":
			// Read what we're on or near. Read position from the snapshot (not
			// m.player) to avoid racing the hub, exactly as View does. A note
			// underfoot takes priority over a nearby signpost.
			me := m.snapshot[m.player]
			if n, ok := noteUnder(m.notes, me); ok {
				m.mode = inputModeRead
				m.readingText = n.Text + "\n\n— " + n.Author
			} else if sg, ok := nearbySign(me); ok {
				m.mode = inputModeRead
				m.readingText = sg.text
			}
			return m, nil
		case "?":
			m.mode = inputModeHelp
			return m, nil
		default:
			// Movement. On enhanced terminals the first press takes one immediate
			// step and arms a held intent for the tick to glide from (repeats are
			// ignored — the tick drives continuation); release stops it. Otherwise
			// we just step on every press (and key-repeat), as before.
			if dx, dy, ok := moveDir(msg.String()); ok {
				if m.enhanced {
					if !msg.Key().IsRepeat {
						theGame.setIntent(m.player, dx, dy)
						theGame.move(m.player, dx, dy)
					}
				} else if now := time.Now(); now.Sub(m.lastMove) >= tickInterval {
					// Fallback (no key release): step on each press, but rate-capped
					// to the tick cadence so a fast key-repeat can't outpace a slow
					// one. The enhanced path doesn't need this — the tick paces it.
					theGame.move(m.player, dx, dy)
					m.lastMove = now
				}
			}
		}
	}
	// Forward any unhandled message (e.g. cursor blink ticks while a
	// non-move mode is active) to the textarea so its internal state
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

	// The strip between map and chat is one divider row normally, but while
	// composing it's the input, which soft-wraps and grows up to 3 rows. The map
	// gives up those rows; the chat pane stays fixed. The full chat (`c`) is a
	// modal over a full-height map, so it reserves no chat rows at all.
	bigChat := m.mode == inputModeBigChat
	composing := m.mode == inputModeSpeak // only chat uses the inline bar; rename/note are modals
	midH := 1
	if composing {
		midH = m.input.Height()
	}
	// The map view is capped (viewportTiles) so it can't sprawl across a big
	// terminal; the whole map+divider+chat "column" is then centred in the
	// terminal further down, with the void margins painted by a background layer.
	// colW/colH are the column's fixed (resting) size.
	viewportTilesW, restingMapTilesH := m.viewportTiles()
	colW := viewportTilesW * 2
	colH := restingMapTilesH + 1 + chatLogLines // map + divider + chat, constant

	// Rows of map actually drawn this frame. The column height stays fixed:
	// composing borrows rows from the map for the taller input; the big-chat
	// modal uses the whole column as its backdrop (chat is the modal).
	viewportTilesH := restingMapTilesH - (midH - 1)
	if bigChat {
		viewportTilesH = colH
	}

	// The camera is dead-zone state, eased as the player moves in Update (see
	// updateCamera) rather than recomputed here — that's what keeps it still
	// while the player roams the middle of the screen, so most steps redraw just
	// two tiles instead of the whole map. View only reads it.
	camX := m.camX
	camY := m.camY

	// A world smaller than the (capped) viewport is centred within the column by
	// a per-axis tile offset; larger worlds leave these zero. This is separate
	// from the column-in-terminal centring computed below.
	worldOffsetTilesX := 0
	worldOffsetTilesY := 0
	if curWorld.width < viewportTilesW {
		worldOffsetTilesX = (viewportTilesW - curWorld.width) / 2
	}
	if curWorld.height < viewportTilesH {
		worldOffsetTilesY = (viewportTilesH - curWorld.height) / 2
	}

	// worldToScreen / screenToWorld are the single source of truth for the
	// world↔canvas transform — it used to be hand-rolled in the tile loop, the
	// tree canopy and the nameplates, each re-deriving it. Each world tile is two
	// cells wide; worldOffset shifts a world smaller than the viewport so it
	// renders centred. Coordinates are canvas-relative (the base layer sits at
	// the canvas origin).
	worldToScreen := func(wx, wy int) (col, row int) {
		return (wx - camX + worldOffsetTilesX) * 2, wy - camY + worldOffsetTilesY
	}
	screenToWorld := func(tx, ty int) (wx, wy int) {
		return camX + tx - worldOffsetTilesX, camY + ty - worldOffsetTilesY
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

	// Index this world's notes by coordinate too, so the tile loop can test for
	// a note marker with an O(1) lookup. m.notes is the copy pulled from the hub
	// when the last snapshot arrived.
	notesByPos := make(map[[2]int]note)
	for _, n := range m.notes {
		if n.WorldID == me.worldID {
			notesByPos[[2]int{n.X, n.Y}] = n
		}
	}

	// Build the viewport on a Lip Gloss Canvas: a 2D buffer of styled
	// cells. We set each tile's two cells directly rather than building
	// a string with embedded ANSI per glyph — Bubble Tea's renderer can
	// then diff the buffer against the previous frame and only emit the
	// cells that actually changed.
	// Day/night: the night darkening + dawn/dusk warmth for this frame (both 0 by
	// day). Computed once and applied to terrain + canopy below; UI chrome and
	// players stay bright, and light sources (windows) glow instead. The stage is
	// discrete, so these only change — and trigger a re-render (see tickOnce) —
	// every few minutes. At dim == 0 we skip tinting, so daytime renders exactly
	// as before.
	dim, warmth := dayTint()

	// Static light spill: at night, gather the light-source tiles near the
	// viewport (just windows for now) so each cell can be lit by the nearest one
	// — less dim and warmer the closer it is (lightAt returns 0..1). It's cheap,
	// and the sources are static, so it adds nothing to movement within the
	// dead-zone: the lit pattern is fixed on screen until the camera scrolls.
	var sources [][2]int
	if dim > 0 {
		r := int(math.Ceil(lightRadius)) + 1
		for sy := camY - r; sy < camY+viewportTilesH+r; sy++ {
			if sy < 0 || sy >= curWorld.height {
				continue
			}
			for sx := camX - r; sx < camX+viewportTilesW+r; sx++ {
				if sx >= 0 && sx < curWorld.width && curWorld.tiles[sy][sx] == tileWindow {
					sources = append(sources, [2]int{sx, sy})
				}
			}
		}
	}
	lightAt := func(wx, wy int) float64 {
		best := 0.0
		for _, s := range sources {
			if t := 1 - math.Hypot(float64(wx-s[0]), float64(wy-s[1]))/lightRadius; t > best {
				best = t
			}
		}
		if best <= 0 {
			return 0
		}
		return best * best * (3 - 2*best) // smoothstep: soft glow that fades to nothing at the edge
	}

	canvas := lipgloss.NewCanvas(colW, viewportTilesH)
	for y := 0; y < viewportTilesH; y++ {
		for tx := 0; tx < viewportTilesW; tx++ {
			// Screen tile → world tile. When the world is bigger than the
			// viewport the centring offset is zero (see screenToWorld).
			wx, wy := screenToWorld(tx, y)
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
			if dim > 0 {
				if curWorld.tiles[wy][wx] == tileWindow {
					fill = glowNight(fill, dim) // a lit window brightens as night falls
				} else {
					l := lightAt(wx, wy)
					if isStructure(curWorld.tiles[wy][wx]) {
						l = 0 // light pools on the ground, not over the building itself
					}
					fill = tintNight(fill, dim*(1-l*maxLift), math.Max(warmth, l*lightWarmth))
				}
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
			case notePosContains(notesByPos, wx, wy):
				// A dropped note: a parchment marker sitting on top of the
				// terrain (it keeps the tile's background, the way players do).
				// The player cases above win, so standing on a note hides the
				// marker — the help line tells you it's there to read.
				left = &uv.Cell{Content: "▤", Width: 1, Style: uv.Style{Fg: colorNote, Bg: fill.Bg, Attrs: uv.AttrBold}}
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
	leafCell := func(seed uint32, l float64) *uv.Cell {
		glyph, fg := "▒", colorCanopy
		switch seed % 4 {
		case 0:
			glyph, fg = "░", colorCanopyLight
		case 1:
			glyph, fg = "▓", colorCanopyDark
		}
		st := uv.Style{Fg: fg, Bg: colorCanopyBg}
		if dim > 0 {
			st = tintNight(st, dim*(1-l*maxLift), math.Max(warmth, l*lightWarmth)) // lit by nearby sources too
		}
		return &uv.Cell{Content: glyph, Width: 1, Style: st}
	}
	drawCanopyCell := func(wx, wy int) {
		if wx < 0 || wx >= curWorld.width || wy < 0 || wy >= curWorld.height {
			return
		}
		if wx == me.x && wy == me.y {
			return // keep our own avatar visible through the leaves
		}
		cellX, cellY := worldToScreen(wx, wy)
		if cellX < 0 || cellX+1 >= colW || cellY < 0 || cellY >= viewportTilesH {
			return
		}
		// Seed each half-cell from the world coord so the dapple is stable and
		// the tile's two halves differ.
		h := grassHash(wx, wy)
		l := lightAt(wx, wy)
		canvas.SetCell(cellX, cellY, leafCell(h, l))
		canvas.SetCell(cellX+1, cellY, leafCell(h*2654435761+1, l))
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
	// The strip between map and chat: the wrapping input while composing,
	// otherwise a double rule labelled with the current cell and headcount (the
	// only persistent status now the top line is gone) plus a read hint when
	// standing on a sign or note.
	var midBlock string
	if composing {
		midBlock = m.input.View()
	} else {
		label := fmt.Sprintf("══ %s · %d here · %s ", cellName, visibleCount, timeOfDay())
		if _, ok := noteUnder(m.notes, me); ok {
			label += "· i read note "
		} else if _, ok := nearbySign(me); ok {
			label += "· i read sign "
		}
		fill := colW - lipgloss.Width(label)
		if fill < 0 {
			fill = 0
		}
		midBlock = lipgloss.NewStyle().Foreground(colorAmberDim).Render(label + strings.Repeat("═", fill))
	}

	// Docked chat pane: the last few messages for this world, newest at the
	// bottom, blank-padded to a fixed height.
	chatBlock := renderChatLog(m.chat, colW, chatLogLines)

	mapView := canvas.Render()
	base := strings.Join([]string{mapView, midBlock, chatBlock}, "\n")
	if bigChat {
		base = mapView // full-height map; the chat is a modal layered over it
	}

	// Centre the column in the terminal. The margins are filled by a void
	// background layer below, so they can't flash on a resize.
	marginX := (m.width - colW) / 2
	marginY := (m.height - colH) / 2
	if marginX < 0 {
		marginX = 0
	}
	if marginY < 0 {
		marginY = 0
	}

	// Compositor: the base text on the bottom, player nameplates layered over
	// it. Speech is in the chat pane now (no floating bubbles), so nameplates
	// and the read modal are the only overlays left.
	// Compose onto a void background sized to the whole terminal, with the column
	// placed at its centred offset. The background paints the margins rather than
	// leaving them to terminal background-erase, so a resize can't flash them.
	voidBG := lipgloss.NewStyle().Width(m.width).Height(m.height).Background(colorVoid).Render("")
	layers := []*lipgloss.Layer{lipgloss.NewLayer(voidBG)}
	// When the column floats inside margins on a big terminal, frame it so its
	// edges — especially the chat's bottom — don't blend into the void. The frame
	// sits in the margin ring (the column content stays put), so it only appears
	// when there's room for it: at least one cell of margin on every side.
	if marginX >= 1 && marginY >= 1 {
		boxed := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorAmberDim).Render(base)
		layers = append(layers, lipgloss.NewLayer(boxed).X(marginX-1).Y(marginY-1))
	} else {
		layers = append(layers, lipgloss.NewLayer(base).X(marginX).Y(marginY))
	}

	// onScreen reports whether a world tile is inside the current viewport.
	onScreen := func(x, y int) bool {
		return x >= camX && x < camX+viewportTilesW &&
			y >= camY && y < camY+viewportTilesH
	}

	// Nameplates: one row above each on-screen player's @, centred on it. Soft
	// lavender normally; while a player's "just spoke" cue is live (messageExpires
	// still in the future) the plate flashes amber with a leading "!" so you can
	// see who's talking and glance at the chat pane for what they said.
	now := time.Now()
	nameStyle := lipgloss.NewStyle().Foreground(colorNameplate)
	cueStyle := lipgloss.NewStyle().Foreground(colorAmber).Bold(true)
	for _, info := range m.snapshot {
		if info.worldID != me.worldID || info.name == "" || !onScreen(info.x, info.y) {
			continue
		}
		plateText, style := info.name, nameStyle
		if info.messageExpires.After(now) {
			// "just spoke": blink the whole plate between "!" and the name in
			// amber, toggling every cueBlinkInterval. messageExpires = spoke +
			// cueDuration, so the time elapsed since speaking is cueDuration minus
			// what's left.
			style = cueStyle
			elapsed := cueDuration - info.messageExpires.Sub(now)
			if (elapsed/cueBlinkInterval)%2 == 0 {
				plateText = "!"
			}
		}
		// worldToScreen is canvas-relative; add the column's margin to land on
		// the centred column in the terminal.
		playerCol, playerRow := worldToScreen(info.x, info.y)
		playerCol += marginX
		playerRow += marginY
		// Prefer above the player, flip below if it'd land in the top margin.
		nameRow := playerRow - 1
		if nameRow < marginY {
			nameRow = playerRow + 1
		}
		plate := style.Render(plateText)
		plateW := lipgloss.Width(plate)
		plateX := playerCol - (plateW-1)/2
		if plateX < marginX {
			plateX = marginX
		}
		if plateX+plateW > marginX+colW {
			plateX = marginX + colW - plateW
		}
		layers = append(layers, lipgloss.NewLayer(plate).X(plateX).Y(nameRow))
	}

	if m.mode == inputModeRead {
		// Offer removal in the footer only when we're reading our own note
		// (matched by IP). The IP is immutable so it's safe to read directly,
		// unlike position which we take from the snapshot.
		removable := false
		if n, ok := noteUnder(m.notes, me); ok && n.AuthorIP == m.player.ip {
			removable = true
		}
		layers = append(layers, signModalLayer(m.readingText, m.width, m.height, removable))
	}
	if m.mode == inputModeHelp {
		layers = append(layers, signModalLayer(gameHelpText(), m.width, m.height, false))
	}
	if m.mode == inputModeRename {
		layers = append(layers, composeModalLayer("New nickname", m.input.View(), "enter to set · esc to cancel", m.width, m.height))
	}
	if m.mode == inputModeNote {
		layers = append(layers, composeModalLayer("New note", m.input.View(), "enter to drop · esc to cancel", m.width, m.height))
	}
	if bigChat {
		m.syncChatVP() // size the viewport to the current panel and load the log
		layers = append(layers, bigChatModalLayer(m.chatVP.View(), cellName, m.input.View(), m.width, m.height))
	}
	return lipgloss.NewCompositor(layers...).Render()
}

// bigChatModalLayer builds the large centred chat panel (the `c` view): a
// double-bordered grey panel over the live map, with a header, the wrapped log
// filling it, and the input at the bottom. esc closes it. Sized to leave a
// margin of map around the edges, and its height never exceeds the terminal, so
// the composite stays exactly `height` rows.
func bigChatModalLayer(logView, cellName, inputView string, width, height int) *lipgloss.Layer {
	panelBg := colorPanelBg
	contentW := bigChatContentWidth(width)

	pad := lipgloss.NewStyle().Width(contentW).Background(panelBg)
	header := lipgloss.NewStyle().Foreground(colorAmber).Bold(true).Background(panelBg).Width(contentW).
		Render("chat · " + cellName + " · scroll: wheel / pgup-dn · esc to close")
	body := header + "\n" + logView + "\n" + pad.Render(inputView)

	box := lipgloss.NewStyle().
		Border(lipgloss.DoubleBorder()).
		BorderForeground(colorAmber).
		BorderBackground(panelBg).
		Background(panelBg).
		Padding(0, 1).
		Render(body)
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

// bigChatContentWidth is the inner text width of the big-chat panel — the panel
// leaves roughly a 4-column map margin each side, minus its border and padding.
func bigChatContentWidth(termWidth int) int {
	w := termWidth - 12
	if w < 20 {
		w = 20
	}
	return w
}

// bigChatLogHeight is the number of log rows in the big-chat panel for a given
// terminal height and input height (the panel leaves a 2-row map margin, a
// border, and one header row). Shared by the renderer and the scroll clamp so
// they can't disagree.
func bigChatLogHeight(height, inputH int) int {
	innerH := height - 6
	if innerH < 3 {
		innerH = 3
	}
	logH := innerH - 1 - inputH // 1 row for the header
	if logH < 1 {
		logH = 1
	}
	return logH
}

// wrapChatLines renders every chat message wrapped to width, returning all the
// visual lines (newest last), each carrying the panel background. It wraps long
// messages rather than truncating like the docked renderChatLog.
func wrapChatLines(chat []gameChatLine, width int) []string {
	senderStyle := lipgloss.NewStyle().Foreground(colorAmber).Bold(true).Background(colorPanelBg)
	textStyle := lipgloss.NewStyle().Foreground(colorCream).Background(colorPanelBg)
	systemStyle := lipgloss.NewStyle().Foreground(colorAmberDim).Italic(true).Background(colorPanelBg)
	wrap := lipgloss.NewStyle().Width(width).Background(colorPanelBg)

	var rows []string
	for _, line := range chat {
		rendered := senderStyle.Render(line.from+": ") + textStyle.Render(line.text)
		if line.from == "" {
			rendered = systemStyle.Render("* " + line.text)
		}
		rows = append(rows, strings.Split(wrap.Render(rendered), "\n")...)
	}
	return rows
}

// syncChatVP sizes the big-chat viewport to the current panel and loads it with
// the world's chat, bottom-aligned (few messages sit just above the input). The
// scroll position is preserved (viewport setters don't reset the offset); the
// caller calls GotoBottom when it wants to stick to the newest line.
func (m *gameScreen) syncChatVP() {
	contentW := bigChatContentWidth(m.width)
	logH := bigChatLogHeight(m.height, m.input.Height())
	m.chatVP.SetWidth(contentW)
	m.chatVP.SetHeight(logH)
	lines := wrapChatLines(m.chat, contentW)
	if pad := logH - len(lines); pad > 0 {
		lines = append(make([]string, pad), lines...) // top-pad so the log sits at the bottom
	}
	m.chatVP.SetContentLines(lines)
}

// syncChatVPStick re-syncs the viewport but keeps it pinned to the bottom if it
// already was — so new messages auto-scroll into view, while a reader who has
// scrolled up stays put.
func (m *gameScreen) syncChatVPStick() {
	m.chatVP.SetHeight(bigChatLogHeight(m.height, m.input.Height())) // current height for an accurate AtBottom
	wasBottom := m.chatVP.AtBottom()
	m.syncChatVP()
	if wasBottom {
		m.chatVP.GotoBottom()
	}
}

// renderChatLog formats the last `lines` chat messages into exactly `lines`
// rows — newest at the bottom, blank-padded at the top so the pane is a fixed
// height — each clipped to `width` columns.
func renderChatLog(chat []gameChatLine, width, lines int) string {
	senderStyle := lipgloss.NewStyle().Foreground(colorAmber).Bold(true)
	textStyle := lipgloss.NewStyle().Foreground(colorCream)
	systemStyle := lipgloss.NewStyle().Foreground(colorAmberDim).Italic(true)

	start := len(chat) - lines
	if start < 0 {
		start = 0
	}
	recent := chat[start:]

	rows := make([]string, 0, lines)
	for i := 0; i < lines-len(recent); i++ {
		rows = append(rows, "") // top-pad so the log sits at the bottom of its region
	}
	for _, line := range recent {
		if line.from == "" {
			rows = append(rows, systemStyle.Render(truncateText("* "+line.text, width)))
			continue
		}
		prefix := line.from + ": "
		budget := width - lipgloss.Width(prefix)
		if budget < 1 {
			budget = 1
		}
		rows = append(rows, senderStyle.Render(prefix)+textStyle.Render(truncateText(line.text, budget)))
	}
	return strings.Join(rows, "\n")
}

// truncateText clips s to at most `max` display columns, adding an ellipsis when
// it overflows. Width is approximated by rune count for the cut, which is exact
// for the latin text chat is overwhelmingly made of.
func truncateText(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= max {
		return s
	}
	if max == 1 {
		return "…"
	}
	r := []rune(s)
	if len(r) > max-1 {
		r = r[:max-1]
	}
	return string(r) + "…"
}

// setInputInPanel gives the compose input the panel's grey background (when it's
// inside a modal / the big-chat panel) or a transparent one (the docked bar,
// which sits over the map). Without the grey, the cream text renders on the
// terminal's default black and punches dark holes in the grey panels.
func (m *gameScreen) setInputInPanel(inPanel bool) {
	s := m.input.Styles()
	text := lipgloss.NewStyle().Foreground(colorCream)
	prompt := lipgloss.NewStyle().Foreground(colorAmber).Bold(true)
	cursorLine := lipgloss.NewStyle()
	if inPanel {
		text = text.Background(colorPanelBg)
		prompt = prompt.Background(colorPanelBg)
		cursorLine = cursorLine.Background(colorPanelBg)
	}
	s.Focused.Text = text
	s.Focused.Prompt = prompt
	s.Focused.CursorLine = cursorLine
	m.input.SetStyles(s)
}

// composeInputWidth sizes the bottom-line text input to roughly fill the
// terminal width (leaving room for the prompt), with a floor for narrow ones.
func composeInputWidth(termWidth int) int {
	w := termWidth - 8
	if w < 20 {
		w = 20
	}
	return w
}

// firstLinePrompt builds a textarea prompt that shows `label` only on the first
// visual row; wrapped continuation rows get blank padding (the textarea aligns
// it to the prompt width) instead of repeating the label on every line.
func firstLinePrompt(label string) func(textarea.PromptInfo) string {
	return func(info textarea.PromptInfo) string {
		if info.LineNumber == 0 {
			return label
		}
		return ""
	}
}

// othersContains keeps the type-switch in View readable — Go doesn't have
// a one-liner for "is this key in this map?" without the value.
func othersContains(m map[[2]int]gamePlayerInfo, x, y int) bool {
	_, ok := m[[2]int{x, y}]
	return ok
}

// notePosContains is the same one-liner for the note-marker lookup in View.
func notePosContains(m map[[2]int]note, x, y int) bool {
	_, ok := m[[2]int{x, y}]
	return ok
}

// noteUnder returns the note on the exact tile the player is standing on, if
// any — you read a note by standing on its marker and pressing i. It takes a
// position snapshot (gamePlayerInfo) rather than the live player, so it doesn't
// race the hub, exactly like nearbySign.
func noteUnder(notes []note, me gamePlayerInfo) (note, bool) {
	for _, n := range notes {
		if n.WorldID == me.worldID && n.X == me.x && n.Y == me.y {
			return n, true
		}
	}
	return note{}, false
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
