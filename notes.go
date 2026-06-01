package main

import (
	"encoding/json"
	"log"
	"os"
	"time"
)

// ----------------------------------------------------------------------------
// Notes: messages players drop on the ground
//
// A player can leave a short note at their feet; everyone else in the same world
// sees a marker on that tile and can stand on it and read it (in the spirit of
// the messages players leave for each other in games like Dark Souls).
//
// Two firsts for this codebase:
//   - Notes are *mutable, shared* world state. Until now the only thing that
//     changed at runtime was player positions; the map itself was read-only.
//     Notes live on the game hub (theGame) under its mutex, like players.
//   - Notes are the first thing we *persist*. The live set lives in memory (the
//     source of truth at runtime); a NoteStore just loads it at startup and
//     saves it when it changes. See game.addNote / game.allNotes.
// ----------------------------------------------------------------------------

// note is a single dropped message, pinned to a world coordinate.
//
// Its fields are *exported* (capitalised) on purpose: encoding/json only
// marshals exported fields, and notes are persisted to disk as JSON. Most of the
// game's internal types use unexported fields, but anything that crosses the
// serialisation boundary has to be visible to the json package. The `json:"..."`
// tags pick the on-disk key names (so the file reads `"x"` rather than `"X"`).
type note struct {
	WorldID int    `json:"world"`
	X       int    `json:"x"`
	Y       int    `json:"y"`
	Text    string `json:"text"`
	Author  string `json:"author"`
	// AuthorIP is who may remove the note. We key removal off IP, not the
	// display name, because nicks can change — IP is the app's stable notion of
	// identity (it's also what nicks are keyed by). Notes written before this
	// field existed load with an empty AuthorIP and so can't be removed.
	AuthorIP string    `json:"authorIP"`
	Created  time.Time `json:"created"`
}

// NoteStore is the persistence boundary for notes. The game owns the live set in
// memory; a NoteStore only has to read it at startup and write it back when it
// changes.
//
// It's an interface — rather than hard-coded file access — so the backend is
// swappable. Today it's a JSON file (fileNoteStore). When the world grows enough
// to want real queries (accounts, an economy, a mutable map), a sqliteNoteStore
// can implement these same two methods and nothing in the game changes. That's
// the "cheap now, no rewrite later" move we discussed; see PLAN.md.
type NoteStore interface {
	Load() ([]note, error)   // read all persisted notes (empty if none yet)
	Save(notes []note) error // persist the whole set, atomically
}

// fileNoteStore persists notes as a JSON array in a single file. It's stateless
// beyond the path: Save always writes the whole set, so there's no in-memory
// copy here that could drift from the game's. The game holds the authoritative
// list and hands it to Save; this type just gets it onto disk.
//
// Why "save the whole set" instead of "append one note"? A JSON array can't be
// appended in place — you rewrite the file either way. At this volume (notes are
// dropped rarely, a few hundred at most) rewriting a small file is nothing. A
// future sqliteNoteStore would instead do a single INSERT, which is exactly the
// kind of change the interface lets us make without touching the game.
//
// Save takes no lock of its own: the game serialises every call under its mutex
// (see game.addNote), so there's only ever one Save running at a time.
type fileNoteStore struct {
	path string
}

func newFileNoteStore(path string) *fileNoteStore {
	return &fileNoteStore{path: path}
}

// Load reads and decodes the notes file. A missing file is *not* an error — it
// just means nothing's been dropped yet, so we return an empty slice.
// os.IsNotExist distinguishes "file isn't there" from a real I/O error we do
// want to surface.
func (s *fileNoteStore) Load() ([]note, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var notes []note
	if err := json.Unmarshal(data, &notes); err != nil {
		return nil, err
	}
	return notes, nil
}

// Save writes the whole set as indented JSON. It writes to a temp file and then
// renames it over the real path: os.Rename is atomic on a single filesystem, so
// a reader (or a crash, or a restart mid-write) never sees a half-written file —
// you get either the complete old file or the complete new one.
func (s *fileNoteStore) Save(notes []note) error {
	data, err := json.MarshalIndent(notes, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// init wires a file-backed store into the shared game and loads whatever notes
// were left in previous sessions. A package init function runs once, after the
// package-level vars (theGame, worlds) are constructed and before main starts —
// so theGame already exists and no sessions are connected yet, which is why it's
// safe to set these fields without taking the lock.
//
// A failed load is logged but not fatal: better to start with no notes than to
// refuse to boot.
func init() {
	store := newFileNoteStore("notes.json")
	notes, err := store.Load()
	if err != nil {
		log.Printf("notes: could not load notes.json: %v", err)
	}
	theGame.store = store
	theGame.notes = notes
}
