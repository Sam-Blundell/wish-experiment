# Hard edges

A running map of where this codebase is fragile, where AI-driven development
strained, and what a future from-scratch rewrite should watch out for.

`PLAN.md` is the project intent; `NOTES.md` is tool/deployment lessons learned.
This file is specifically the **rewrite minefield map** — the places that look
like they'll bite when Sam rewrites this himself.

## How to use this

A map of current facts, not a worklog: describe what's true *now*, not how it was
investigated. When an open question gets answered and turns out not to be a hard
edge, **delete** the entry rather than marking it "resolved" — just make sure any
fact still worth knowing lands where it belongs (a code comment, `NOTES.md`, etc.).

Two kinds of entry:

- **[read]** — inferred from reading the code: fragility or a design smell visible
  in the artifact. May flag something to *verify* rather than a confirmed bug.
- **[lived]** — hit firsthand while building: took several tries, the framework
  fought the approach, or behaviour was guessed at and couldn't be confirmed.

Each entry says what it is and what the rewrite should do about it.

---

## Rendering & layout

### World→screen coordinate transform is recomputed inline everywhere · [read]
Each world tile is 2 terminal cells wide, so the world→screen mapping is
`(wx - camX + worldOffsetTilesX) * 2`. That exact expression is hand-rolled in
several places in `game.go`'s `View` — the tile loop, tree canopies and
nameplates — each re-deriving the camera offset and its own
bounds clamping. Change the camera or centring logic and every copy has to move
in lockstep or they silently drift apart.
**Rewrite:** a single `worldToScreen(wx, wy)` helper (and its inverse), used
everywhere a coordinate crosses the boundary.

## Concurrency & shared state

### chat drops are lost lines; the same pattern in the game is harmless · [read]
Both `room.broadcast` (chat) and `game.broadcast` use
`select { case ch <- msg: default: }` to avoid blocking on a slow client. For
the **game** this is fine — each message is a *full snapshot*, so a dropped one
is just a skipped frame and the next carries current state (the code says as
much). For **chat**, messages are *deltas* (individual lines): a drop is a line
that client never sees and that is never resent, so under sustained load to a
stalled client its history silently diverges from everyone else's.
**Rewrite:** the snapshot reasoning doesn't transfer to a delta stream. Chat
needs backpressure, a per-client catch-up, or sequence numbers.

### chat join + history snapshot is a two-lock window → duplicate lines · [read]
`newChatScreen` calls `chatRoom.join(c)` (locks, adds `c` to the client set,
unlocks) and *then separately* `chatRoom.history()` (locks, copies messages,
unlocks). A message sent by someone else in the gap between those two locked
sections is both delivered to `c`'s channel (it's a client by then) *and*
present in the history snapshot — so the joiner renders it twice. NOTES.md
fixed the narrower join-*message* duplicate (add-after-broadcast) but not this
general window. Timing-dependent, likely never observed by hand.
**Rewrite:** take the history snapshot and the join under one lock, so a
client's initial state and its live stream can't overlap.

### `nicks` is a package global shared across chat and game · [read]
Identity (`nicks` map + `nicksMu`, `getNick`/`setNick`) lives in `chat.go`, but
the game reads and writes it too. `game.rename` deliberately calls `setNick`
(its own lock) *before* taking `g.mu`, with a comment accepting a brief window
where a new joiner sees the new nick before other players' snapshots refresh.
It's not a deadlock — the two locks are never held at once — but it's two
subsystems coupled through a mutable global with an acknowledged consistency
gap.
**Rewrite:** decide where identity actually lives (a session/user object?)
instead of a cross-module package global.

## World generation

### `worlds` is one big package-init IIFE of hardcoded coordinates · [read]
`var worlds = func() []*worldGrid { ... }()` builds all three worlds, wires the
doors, carves the paths, scatters clutter and places landmarks in a single
initializer full of magic constants (`houseX=80, houseY=18`,
`linkX=worldWidth/2`, seeds `42/7/11/23/31/99…`). Positions are absolute, so
changing `worldWidth`/`worldHeight` quietly breaks half of them, and any
out-of-range index panics during package init — meaning the *server fails to
start* with no graceful error.
**Rewrite:** data-driven world definitions (relative / validated positions),
generation behind a function that can return an error instead of panicking at
init.

## Persistence

### Notes are saved while holding the game lock · [lived]
`game.addNote` writes the whole notes file (`fileNoteStore.Save` rewrites it
atomically) *while still holding `g.mu`*, so dropping a note does blocking disk
I/O inside the hub lock — every other player's moves wait on that write. It's
deliberate: it keeps saves ordered and the on-disk set consistent with what's
broadcast, and at current volume (notes are rare, the file is tiny) the stall is
sub-millisecond. It stops being fine if writes get frequent — e.g. if "drop a
note" becomes "place a block" — when a disk hiccup would stall the whole world.
The read side takes the opposite shortcut: each session re-pulls the entire
notes list (`allNotes`, a lock + full copy) on *every* snapshot it receives,
rather than carrying notes in the snapshot — fine while notes are few.
**Rewrite:** move persistence off the hot path (a dedicated writer goroutine fed
an ordered channel, or debounced saves), and give notes a change-version so
clients only re-pull when the set actually changes.

## Dependencies

### Pinned pre-release Charm v2 stack; game uses the low-level `uv` API · [read]
`go.mod` pins the `charm.land/*` v2 line plus `ultraviolet` and `ssh` at commit
pseudo-versions (`v0.0.0-2026…`) — pre-1.0, fast-moving APIs. The game renders
by going *under* Lipgloss to Ultraviolet (`uv.Cell`, `uv.Style`,
`lipgloss.NewCanvas`), which is the least-documented surface in use here and the
most likely both to have been guessed at during the spike and to shift under a
future upgrade.
**Rewrite:** pin deliberately, and isolate the raw-cell rendering behind a thin
internal layer so an Ultraviolet API change touches one file, not all of `View`.

## Testing

### No tests, and several invariants are load-bearing · [read]
No `_test.go` files anywhere (expected for a spike). For the rewrite, the
invariants worth pinning down first — the spots where a confident-looking
rewrite could regress silently:
- `tile.walkable()` correctness;
- **every door's target tile is walkable** (`move()` checks this at runtime, but
  nothing tests it);
- the world↔screen coordinate transform;
- `placeSpawn` always finding a tile;
- the concurrency cases above (no duplicate on join, no lost chat line, nick
  consistency).
