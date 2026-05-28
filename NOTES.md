# Things we learned

## Wish / Charmbracelet

- `lipgloss.NewStyle()` uses a global renderer that detects the server's terminal, not the SSH client's. Colours won't work over remote SSH unless you use `bubbletea.MakeRenderer(session)` to create a per-session renderer and call `renderer.NewStyle()` instead.
- Wish auto-generates a host key at the path you give `wish.WithHostKeyPath()`. If you delete it and it regenerates, SSH clients that connected before will reject the new key (REMOTE HOST IDENTIFICATION HAS CHANGED). On the VPS the key persists across restarts since it lives on disk.
- You can't overwrite the app binary via SCP while the systemd service is running it. The deploy workflow needs to stop the service first, copy the binary, then start it again.

## Deployment / VPS

- Vultr's cloud firewall is separate from UFW on the server. An empty Vultr firewall group denies everything by default.
- fail2ban bans by IP, not device. Devices on the same local network share a public IP, so failed SSH attempts from one device (e.g. Termius on a phone) can lock out another (e.g. a laptop).
- Cloudflare doesn't proxy SSH traffic (Spectrum exists but is Enterprise-only). SSH apps need DNS-only (grey cloud) records pointing to a server you control.

## Go / Architecture

- Bubble Tea uses the Elm architecture: Init (startup), Update (handle events, modify state), View (render state to string). Wish serves this loop over SSH instead of a local terminal.
- For shared state across SSH sessions (like a chat room), use a mutex-protected struct. Each session gets its own Bubble Tea model but they communicate through shared state via channels.
- When broadcasting a join message, add the client to the room after the broadcast — otherwise the joiner receives the message both via their channel and via history, causing a duplicate.
