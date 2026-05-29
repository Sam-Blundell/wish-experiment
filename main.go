// Every Go program lives in a package. The `main` package is special: it's
// the one Go looks at when building an executable, and it must contain a
// `func main()` which is the program's entry point.
package main

// `import` pulls in other packages. The standard library packages (context,
// fmt, log, etc.) ship with Go itself. The lower-case names below the blank
// line are third-party — listed in go.mod and fetched into the module cache.
//
// Note the `tea "charm.land/bubbletea/v2"` line: that's an *aliased import*.
// The package's real name is `bubbletea`, but we rename it to `tea` locally
// so we can write `tea.Model` instead of `bubbletea.Model`. This is a Go
// language feature, not a Charm convention, but the Bubble Tea docs use
// `tea` everywhere so it's the conventional alias.
import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/wish/v2"
	"charm.land/wish/v2/bubbletea"
	"github.com/charmbracelet/ssh"
)

// `const` declares compile-time constants. Grouping them in a parenthesised
// block is a Go style convention — same as the import block above. You could
// also write `const host = "0.0.0.0"` on its own line.
const (
	host = "0.0.0.0"
	port = 2222
)

func main() {
	// `:=` is short variable declaration: it both declares `s` and `err` and
	// assigns to them, inferring their types from the right-hand side. You
	// can only use `:=` inside a function; at package level you need `var`.
	//
	// Everything from here down is Charm-specific. `wish.NewServer` builds
	// an SSH server. The `wish.With...` calls are the functional-options
	// pattern — each one returns a function that mutates the server config.
	// Go doesn't have keyword arguments, so libraries often expose config
	// this way.
	s, err := wish.NewServer(
		wish.WithAddress(fmt.Sprintf("%s:%d", host, port)),
		wish.WithHostKeyPath(".ssh/id_ed25519"),
		// Middleware wraps every incoming SSH session. `bubbletea.Middleware`
		// is the bridge between Wish (SSH) and Bubble Tea (the TUI framework):
		// for each session it spins up a Bubble Tea program whose root model
		// is whatever the function returns.
		wish.WithMiddleware(
			bubbletea.Middleware(func(s ssh.Session) (tea.Model, []tea.ProgramOption) {
				// `net.SplitHostPort` returns three values (host, port, err).
				// We assign all three but use `_` (the blank identifier) to
				// discard the port and the error — a Go convention for "I
				// don't care about this return value".
				ip, _, _ := net.SplitHostPort(s.RemoteAddr().String())
				root := newRoot(s, ip)
				// A function in Go can return multiple values. Here we return
				// the root model and `nil` for the program options (no extras).
				return root, nil
			}),
		),
	)
	// Standard Go error handling: functions that can fail return an `error`
	// as their last return value, and you check it with `if err != nil`.
	// There are no exceptions in Go.
	if err != nil {
		log.Fatalf("could not start server: %s", err)
	}

	// A *channel* is Go's built-in mechanism for passing values between
	// goroutines. `make(chan os.Signal, 1)` creates a buffered channel that
	// can hold one signal before a sender blocks.
	done := make(chan os.Signal, 1)
	// `signal.Notify` tells the runtime to deliver these OS signals to our
	// channel instead of using the default behaviour (which would kill us).
	signal.Notify(done, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)

	log.Printf("Starting SSH server on %s:%d", host, port)
	// `go func() { ... }()` starts a *goroutine*: a lightweight thread
	// managed by the Go runtime. The function literal is defined and then
	// immediately invoked (the trailing `()`), but the `go` keyword means
	// it runs concurrently instead of blocking us here.
	go func() {
		if err := s.ListenAndServe(); err != nil {
			log.Fatalf("server error: %s", err)
		}
	}()

	// `<-done` receives a value from the channel. Because nothing has been
	// sent yet, this blocks the main goroutine until a signal arrives. That
	// keeps the program alive while the server goroutine does its work.
	<-done
	log.Println("Stopping SSH server")
	// `context.WithTimeout` returns a context that auto-cancels after 5s,
	// plus a `cancel` function we should call to release its resources.
	// `defer` schedules `cancel()` to run when the surrounding function
	// returns — a Go idiom for cleanup that mirrors try/finally.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		log.Fatalf("could not stop server: %s", err)
	}
}
