# Design — the game we're building toward

This is the **design vision / north star**: where the game is headed and the
reasoning behind the big choices. It is **mostly unbuilt** — aspirational, not a
description of the current code. For what *exists and is fragile today* see
`HARD-EDGES.md`; for the original (now-superseded) framing see `PLAN.md`.

Captured from a long design session (2026-06-02). Rough first pass — expect to
reorganise. Things already built are tagged **[built]**.

## What it is

An SSH-served, terminal, multiplayer overworld — small, social, ambient. Today it's
a sandbox (walk, chat, leave notes) with no gameplay loop; the vision adds one
without losing the chill, social, exploratory feel.

**The premise that ties it together:** *people huddled on islands of stable reality,
venturing into an encroaching unreal to bring back what they need.* Reality holds
near civilisation and **frays as you wander out** — the far wild turns non-euclidean,
trackless, dangerous to return from (House of Leaves / the Zone / Area X).
Civilisation is safe, known and poor; the deep is rich, alien and perilous.

**Identity still open — the pivotal fork:** cozy-communal-tending (peaceful,
collective, async; Animal Crossing / Stardew) vs survival-progression (scarcity,
tools-as-power; more Minecraft). Most choices below bend to this; current lean is
cozy-ish but undecided. **Emerging resolution (from the dive/risk mechanics): maybe
both/and** — a *cozy, social hub* (civilisation) wrapped around a *tense, perilous
frontier* (the dives). Stardew's farm vs its mines; Sunless Sea's port vs its sea.

## The world model

- **A grid of chunks**, traversed **edge-to-edge, Zelda-style** — walk off the east
  edge and the east chunk loads. No map-click transitions for ordinary travel.
- **Chunks are bigger than the terminal** — you scroll within one (the dead-zone
  camera **[built]** already does this; the current 120×60 worlds already scroll).
- **Fast travel** exists but is *diegetic, not map-based* — a **transport merchant**
  in town, gated behind civilisation + a cost.
- **Three kinds of chunk = two axes** (persistence × mutability):
  - **Authored + fixed** — towns, landmarks, **roads**. Hand-made content, not
    save-state, unalterable (notes aside). Roads are fixed *deliberately* so the
    arteries between towns can't be vandalised or walled off.
  - **Built + persistent** — homesteads / plots. Save-state, player-owned, editable
    (plant, farm, build). Should be **finite / claimable** (a homestead near a town),
    not "claim any chunk forever," or storage grows unbounded.
  - **Wild + generated** — the wilderness. **Deterministic, not ephemeral:** a
    chunk's layout is `f(x,y)` (seed = hash of coords), generated identically every
    visit. See next section for why this beats per-player instances.

## Knowable near, trackless deep — and how it holds together

The wilderness rides **one dial: distance from civilisation**, governing **rarity +
strangeness + return-risk** together. Near = safe/known/poor; deep =
rich/alien/perilous. A procedural grid is effectively infinite, so *distance gates
rarity* the way the classic formula uses world size — we just don't hand-build the
vastness.

**Near wild = knowable (deterministic).** Stable, mappable, learnable ("the ore cave
is three chunks NE"). Co-presence is free: generalise the current **per-`worldID`**
presence/chat **[built]** to **per-chunk**, and players at the same coords see each
other automatically — *no party system, no instancing*. Untouched wilderness costs
**zero storage** (it's a function, not a save).

**The "instant re-farm" problem is solved by regrowth *timing*, not ephemerality.**
Pure determinism would restore trees the moment you re-enter (cheap). Fix: a thin
**harvest-delta** — store *only* "node N harvested at time T" for touched nodes. A
chunk = its seeded layout *minus* the deltas, so cut-leave-return-immediately shows
**stumps**, regrowing on a wall-clock timer, never on re-entry. Deltas are tiny, only
touched chunks have one, and the cache is **LRU-evicted** (evicted → reverts to the
full seeded layout = "fully regrown," which is correct). Standard Minecraft/Valheim
approach: deterministic worldgen + a saved diff.

**Deep wild = trackless (the strange).** Far out, reality frays: backtracking fails,
**thresholds stop being reciprocal** (go through a door, turn around, it's gone /
opens somewhere new — the diegetic face of "the chunk re-rolled"). This is the
trackless/roguelike feel, and where the rarest resources live.

**Two verbs, no genre clash.** You **walk** the knowable near-world (Zelda); you
**dive and extract** from the deep (roguelike). That dissolves the
seamless-walking-vs-trackless tension — different modes of one world.

**Getting home = extraction, not traversal.** You don't walk back from the deep; you
**extract** via a mechanism — which makes the deep a place you *raid* (dive, grab, get
out), a proven, juicy loop (Tarkov / Hades). Candidate mechanisms (likely combine
~two):
  - **Anchor you manage** — twine / lantern-oil / a tether that drains the deeper you
    push; running low *is* the "turn back" signal.
  - **Beacon** — a tower-light / tolling bell whose *direction* you can always sense
    even when the ground lies; navigate by it, not by memory.
  - **Recall ripcord** — one-use / cooldown snap back to the last stable threshold.
  - **Persistent thread** — markers that survive the churn and breadcrumb you home
    (the existing **notes [built]** could be this), until the wild starts moving them.

**Guiding principle: agency, not helplessness.** "Lost at random" is anti-fun. The
strangeness must be a *system you learn to read and work* — every get-home idea above
is really a **legibility** tool: readable state in an unreadable place. The day/night
tint + the divider line **[built]** are ready-made to *show* "you're in the strange
now" (the palette curdles, the clock stops making sense).

## Light, depth & the dive (the wilderness mechanic)

How the deep is actually traversed — the chosen realisation of the get-home
candidates above (the light *is* the anchor + beacon + recall, fused). The fixed
**flames at the heart of each town are the reality anchors**; you light your own
portable light (candle → torch → lantern; more sophisticated = higher **charge** /
longer burn) from one, and carry it in. (Acknowledged debt: Darkest Dungeon's torch.)

**Depth, not coordinates.** The deep wilderness has no stable (x,y) — it's a **depth
ladder**. Each cell has four edges: **three go deeper (+1), one goes out (−1)**, and
which is which is **re-rolled per cell** (the non-euclidean fraying — the way out is
never just "back the way you came"). "Out" means *decrement depth until you pop into
civilisation at the flame you lit from* — you don't retrace a path and won't pass the
same cells again. (So the world splits cleanly: stable, mappable (x,y) civilisation +
near vs the non-spatial wilderness ladder.)

**The light does four jobs with one number:**
1. **Compass** — while lit, shows the current cell's "out" edge.
2. **Timer** — every cell you enter burns **1 charge** (moving, not standing — no
   real-time pressure while you think).
3. **Death clock** — if it goes out in the wild you're blind: each move is 3/4 deeper,
   1/4 out, a biased walk that almost certainly drifts to the **lost-to-the-wilderness**
   depth (death). Going lightless is the *failure state*, not a strategy.
4. **Progression gate** — charge caps safe depth, depth gates rarity, so *better light
   → deeper → rarer loot → craft better light*. The whole gather→craft→explore loop
   runs on light.

**The number the player watches: `charge − depth`.** Walking out from depth D costs D
charge, so you're safe only while `charge ≥ depth`; that margin is the real danger
meter, and "one more cell?" as it shrinks is the push-your-luck tension. Surfacing the
margin (not raw charge) is what keeps it *agency*, not random death. *Worked example:*
enter at depth 1, candle 20; it says east is out, but you go deeper (north) → depth 2,
candle 19, now says north; you head out (north) → depth 1, candle 18, now says south;
out (south) → home, candle 17.

**Replenishment turns the cap into a gamble (the heart of the risk).** Let players
*refuel in the wild* so they can push **past** the `charge ≥ depth` cap, betting
they'll find more light before the dark takes them — knowable arithmetic becomes
expected-value-under-uncertainty. Keep it *informed, not blind*: "**can I reach that
glow and get back**," not "push deep and hope." Sources, by the risk each creates:
- **Visible deeper lights** — you can *see* a glow / cache a few cells in; reaching it
  refuels, but it's past your margin. (Lore spice: some are *false lights* — the
  wilderness baiting you deeper, anglerfish-style.)
- **Burn your haul for charge** — sacrifice loot to buy your way out. Agonising,
  self-balancing, ties fuel to the economy (wood/oil *is* fuel).
- **Wild flames** — rare, semi-stable lesser anchors you light to refuel + stage from;
  could be *communal* (pushes the stable frontier outward) but **unstable** (gutters
  unless tended → async upkeep), so it never trivialises depth.
- **Fuel as a gatherable** — oil / wax / fatwood stocked *before* a run (the cozy prep
  phase).

Hygiene: keep **light *tier* meaningful even with refuelling** — bigger = more buffer
/ slower burn / survives deep cells that gutter weak ones — or refuelling cannibalises
the tech tree. Tier = resilience, refuel = extension. The loop: **prep → dive →
extract-or-lose-your-haul → upgrade** — a tense extraction loop with a cozy front
porch.

**Parties land here, on-theme.** Co-presence is free in the stable overworld (shared
coords), but the ladder is per-expedition, so meeting *in the dark* means **descending
together** — exactly Darkest Dungeon's party. So "parties" are a *dive* feature, only
where they're thematic, and never needed for the overworld.

**Open within this mechanic:**
- What **death** costs — drop your haul and wake at a flame (cozy) vs harsher
  (survival)? Ties to the identity fork.
- Keep **basic resources (wood/stone) in safe civilisation / near plots**, so the
  early game isn't gated behind risky dives — the ladder is for the *rarer* stuff.
- Is the compass **perfect** (pure budget push-your-luck) or **imperfect** (flicker /
  rough heading — navigation skill on top of the budget)?

## The core loop: gather → craft → unlock → explore

Classic resource progression (wood → stone → metal → special tools), reframed for a
small, shared, persistent world via three swaps:
- **Space → Time.** Renewability + rarity gated by timers/conditions (regrowth,
  night/seasonal spawns), riding the tick **[built]** + day/night **[built]**.
  Doubles as the async hook (worth coming *back*, not just *out*).
- **Depth → Cells.** A mine is a **stack of cave cells** you descend; "deep" = how
  many cells down. No 2D vertical digging. Ruins, deeper forest, an island — each a
  cell, via the door system **[built]**.
- **Survival → communal tending** (cozy lean): the shared place *improves from
  collective effort* — chop wood to build a bench others sit on, light the square.

Tool tiers **gate cells** as well as nodes: a cave needs a light (the shelved
`/torch`); a collapsed passage needs a metal pick. So progression *is* exploration:
gather → craft → **unlock** → explore → gather rarer.

**Crafting is terminal-native = verbs** (`chop`, `mine`, `craft axe`, `build bench`),
not Minecraft's spatial place-block (awkward on a char grid). Text recipes, glyph
resources — leans into the BBS/MUD heritage and is a real differentiator.

Raw nodes already exist as tiles **[built]**: trees, rocks / standing stones, reeds,
mushrooms, water.

## Liveness & stickiness

Low simultaneous population → two disproportionately valuable levers:
- **Ambient life** (feel alive even alone): **living water [built]**; fireflies at
  night, wind through grass/canopy, chimney smoke — all ride the tick, cheap because
  the renderer ships only changed cells.
- **Async presence** (community across time): a guestbook / "who was here"; planting
  that persists and grows; notes as the world's memory; a message board (the
  long-deferred `PLAN.md` item).
- **Expressiveness together:** emotes (`/wave`, `/sit`), reusing the speech-cue
  mechanism **[built]**.

## What this leans on (architecture / rewrite)

Rewrite-scale, and it points hard at directions already in `HARD-EDGES.md`:
- **Data-driven entity catalogue** — wild trees/ore are *generated stateful
  entities*; tools/resources are *items*. The spine that makes resource state (and
  the map-builder) possible.
- **Per-chunk presence** — generalise the per-`worldID` snapshot/chat broadcast.
- **Mutable shared world state** — harvest-deltas / built plots break today's
  load-bearing "world is static, read without locking" simplification → needs locking
  + a mutable overlay; pushes on the world-gen IIFE.
- **Persistent inventory** — extends the note-store persistence.
- **Retained / dirty-region rendering** — the current build is full + double-buffered
  every frame (alloc-bound); an entity renderer that tracks per-cell dirty state is
  the fix (see HARD-EDGES).
- The **tick [built]** is the ambient-sim heartbeat for all of it (regrowth, spawns,
  strangeness).

**Find the fun before the architecture:** the full system is big, but the loop can be
prototyped cheaply on the current world — one resource (wood, regrowing on the tick),
one verb (`chop`), one craft, a throwaway inventory — to feel whether it's satisfying
*here* before committing.

## Open decisions

- **Identity:** cozy-communal vs survival-progression (drives everything).
- **Wilderness reach:** near-knowable + deep-trackless synthesis (current lean) vs
  whole-wild-trackless.
- **Build-plots:** designated/claimable-near-town (bounded) vs claim-anywhere.
- Get-home mechanism mix; how the boundary into "the strange" announces itself;
  z-levels (deferred).
