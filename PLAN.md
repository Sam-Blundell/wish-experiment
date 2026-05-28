# SSH BBS

An SSH community platform. Users connect with `ssh bbs.example.com`, get a terminal UI, and can browse a directory of services — message board, chatrooms, games, file exchange. They can log in with a persistent account or stay anonymous.

## Architecture

One Bubbletea program per SSH session. A thin root model holds a `Screen` interface and forwards all messages to whoever is active. Sub-apps (message board, chat, etc.) each implement `Screen`. Navigation works by swapping which sub-app the root points at.

The SSH/wish layer is separate from the TUI. The TUI runs standalone in a terminal during development. Wish wraps it for serving over SSH — the only difference is where the renderer comes from.

Persistence via SQLite. User accounts, message board posts, etc.

## Step 1: Standalone message board TUI

Build a message board as a regular Bubbletea app that runs locally in the terminal. No SSH, no accounts, no multi-user, no root model. Just the message board UI and interaction.

This becomes the first sub-app when we later build the shell around it. Adapting it is a small refactor: rename the interface, swap `tea.Quit` for a back message, accept user/renderer as parameters.
