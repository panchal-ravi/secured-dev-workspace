# Secured Workspace helper (`secured-ws://`)

The portal backend runs on a remote server, so it cannot open a terminal or write
`~/.ssh/config` on your machine. This small **local** helper does that, triggered
by `secured-ws://` deep links the portal's **Authenticate** and **Open** buttons emit —
the same pattern as `vscode://` links.

The helper is deliberately narrow. It understands exactly two actions and rebuilds
every command/SSH block itself from validated parameters; a link can never make it
run an arbitrary command or write arbitrary SSH config.

- `secured-ws://authenticate?addr=&auth_method_id=&terminal=`
  → opens your terminal running `boundary authenticate oidc …` (manual SSO login;
    the token caches in your OS keyring for `boundary connect`). Optional now —
    `connect` signs in on demand; kept as a pre-authenticate / troubleshooting path.
- `secured-ws://connect?host=&user=&target_id=&addr=&ide=&auth_method_id=`
  → signs you in to Boundary if no valid token is cached (runs `boundary
    authenticate oidc`, which reuses the live IBM Verify SSO session from your
    portal login and completes silently — no re-login), then upserts the
    workspace's managed `Host` block in `~/.ssh/config` and opens your IDE.
- `secured-ws://disconnect?host=`
  → removes the workspace's managed `Host` block from `~/.ssh/config` (fired when the
    workspace is destroyed); no-ops if the block was never written.

Parameter validation (rejected otherwise): `addr` must be `https://…`,
`auth_method_id` `am*_*` (required on `connect` too), `target_id` `tssh_*`,
`host`/`user` alphanumeric-ish, `terminal` ∈ {terminal,iterm},
`ide` ∈ {vscode,vscode-insiders,cursor,windsurf,bob}. `bob` (IBM Bob, a VS Code fork)
maps to the `ibm-bob://` scheme; it needs the open-remote-ssh extension from Open VSX
(no built-in Remote-SSH).

## macOS (built & shipped)

`SecuredWS.app` is an AppleScript applet whose `on open location` handler runs the
bundled `secured-ws.sh`. Source: [`macos/`](macos/).

**Build** (operator, on macOS — needs `osacompile`, run *after* the frontend build
since `npm run build` empties `backend/web`, though the helper now lives in the
sibling `backend/helper-dist/` and is unaffected):

```sh
portal/helper/macos/build.sh      # → portal/backend/helper-dist/SecuredWS-macos.zip
```

**Install** (developer): in the portal's "Connect to your workspaces" panel click
**Download & install it**, then:

```sh
unzip ~/Downloads/SecuredWS-macos.zip -d ~/Applications
xattr -dr com.apple.quarantine ~/Applications/SecuredWS.app   # clear the download flag
open ~/Applications/SecuredWS.app                             # once, to register the scheme
```

The app is **ad-hoc signed** (no Apple Developer ID — this is a PoC). macOS quarantines
anything downloaded from a browser; on Apple Silicon a quarantined app without a
Developer-ID signature is reported as *"'SecuredWS' is damaged and can't be opened"*.
The `xattr -dr com.apple.quarantine` line above removes the quarantine flag and resolves
it. If you skipped that step and already hit the error, run the two commands on the
installed copy:

```sh
codesign --force --deep --sign - ~/Applications/SecuredWS.app   # re-seal if needed
xattr -dr com.apple.quarantine ~/Applications/SecuredWS.app
```

After that, **Open** in the portal works with one click — it signs you in to Boundary
automatically (reusing your portal login) and launches the IDE. Requires the
`boundary` CLI on your `PATH` (already needed for `boundary connect`; the helper also
looks in `/usr/local/bin` and `/opt/homebrew/bin`).

> **Log into the portal in a normal browser window — not incognito/private.** The
> silent Boundary sign-in works by reusing the IBM Verify session your portal login
> created. `boundary authenticate oidc` opens your OS **default browser in a normal
> window**; an incognito/private session's cookies are isolated from it, so if you
> logged into the portal in incognito, Boundary can't see that Verify session and you
> get a full re-login. Use a normal window (the same browser context Boundary opens).

**Self-test** the logic without launching anything: `bash macos/selftest.sh`.

## Windows (not built — reference)

Ship a small `secured-ws-helper.exe` that, run as `secured-ws-helper.exe --register`,
writes:

```
HKCU\Software\Classes\secured-ws\(Default)            = "URL:Secured Workspace"
HKCU\Software\Classes\secured-ws\URL Protocol         = ""
HKCU\Software\Classes\secured-ws\shell\open\command\(Default) = "\"C:\path\secured-ws-helper.exe\" \"%1\""
```

On invocation it parses the same URL and launches the chosen terminal:
`wt.exe cmd /k "<cmd>"`, `cmd /c start "" powershell -NoExit -Command "<cmd>"`, or
`cmd /c start "" cmd /k "<cmd>"`; for `connect` it upserts `%USERPROFILE%\.ssh\config`
and runs `start <ide>://…`; for `disconnect` it strips the managed block.

## Linux (not built — reference)

Install a `.desktop` file and register it:

```desktop
# ~/.local/share/applications/secured-ws.desktop
[Desktop Entry]
Type=Application
Name=Secured Workspace
Exec=/path/secured-ws-helper %u
MimeType=x-scheme-handler/secured-ws;
NoDisplay=true
```

```sh
xdg-mime default secured-ws.desktop x-scheme-handler/secured-ws
update-desktop-database ~/.local/share/applications
```

The handler opens the user's terminal (`x-terminal-emulator -e …`), writes
`~/.ssh/config` the same way, and strips the managed block on `disconnect`.

## Limitations

- The macOS app is unsigned (PoC) → first-open Gatekeeper prompt.
