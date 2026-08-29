# godeploy

Terminal-first deployment/synchronization tool for local projects and SFTP/FTP/FTPS targets.

## Current starter features

- YAML accounts/projects configuration
- SFTP key, ssh-agent, and password authentication
- FTP and explicit/implicit FTPS password authentication
- Scoped local/remote recursive scanning
- Multiple local-subfolder to remote-directory mappings per project
- `.gitignore` plus configured glob exclusions
- Size, modification-time, SHA-256, and normalized-text diff modes
- Live progress while scanning, comparing, and deploying files
- Side-by-side remote/local text diff preview with language-aware syntax highlighting
- Bubble Tea + Lip Gloss TUI
- Bubblezone row hit-testing
- Upload/download/delete/skip action selection
- Explicit deletion confirmation before execution
- Remote source browser: browse the server outside the sync flow and
  download any file or folder, including files the sync would ignore
  (mapped paths go to the project, unmapped paths pick a local folder)
- Local source browser: browse local files and upload anything, ignoring
  `.gitignore`; project `exclusions` stay visible (dimmed) and toggleable
- Interactive ssh shell: drop into the project's server with the account
  credentials (`t` in the folder browser or `-shell`)
- Concurrent transfer worker pool
- Temporary-file atomic replacement when remote rename is available
- Local timestamp preservation and SFTP remote timestamp preservation

## Requirements

- Go 1.23+

## Setup

```bash
mkdir -p ~/.config/godeploy
cp config.example.yaml ~/.config/godeploy/config.yaml
go mod tidy
go run ./cmd/godeploy
```

Running without `-p` opens an interactive project picker. Select a project to
synchronize, press `T` to shell into its account immediately, or switch to the
Accounts list with `A`/`Tab` and shell into any SFTP account—including accounts
that are not linked to a project. Next, a **local folder browser** opens at the selected project's `local_root`: descend into subfolders and
mark folders or individual files as sync scopes (or pick the root for a
full sync) — only then does godeploy connect to the remote and scan. To
skip both steps:

```bash
go run ./cmd/godeploy -p api-backend
```

Scoped synchronization:

```bash
go run ./cmd/godeploy -p api-backend -path src/controllers
go run ./cmd/godeploy -p api-backend -path public/index.php  # single file
```

### Per-folder remote mappings

A project can route unrelated local subfolders to different remote directories:

```yaml
projects:
  - id: "site"
    account_id: "prod-web"
    local_root: "/Users/dev/code/site"
    path_mappings:
      - local_path: "public"
        remote_path: "/var/www/site"
      - local_path: "deploy/config"
        remote_path: "/etc/site"
```

The folder browser works as before and still supports marking multiple folders.
Selecting the project root includes every mapping. Selecting `public/assets`
syncs it to `/var/www/site/assets`; selecting `deploy/config` syncs it to
`/etc/site`. Mappings must be non-overlapping and `local_path` is always
relative to `local_root`.

Existing projects that only have `remote_root` remain valid; it behaves like a
single mapping from the project root. In `godeploy -manage`, select **Path
mappings** and press `Enter` to open the dedicated row editor. Local and remote
columns both support `Tab` completion; local results remain relative to the
project's Local root.

The compare phase opens `-compare` parallel connections (default 4) and
diffs files across them — each connection is used by exactly one worker,
so it works for SFTP and FTP alike. Tune with
`-compare 8` on fast links; credentials are only prompted once per run.

## Config manager

Manage accounts and projects interactively instead of hand-editing the YAML:

```bash
go run ./cmd/godeploy -manage
```

The manager starts with an empty config when the file does not exist yet, so
it can also be used to create one from scratch. Accounts and projects are
grouped in two tabbed lists. **Edits are written to disk immediately**: saving
a form (`Enter`) or confirming a delete persists `config.yaml` (and
`secrets.yaml`) right away, so quitting never loses work. `s` re-writes
manually, and if a write ever fails, `q` offers `y` save-and-quit / `n`
quit-without-saving / `esc` stay.

| Key | Action |
|---|---|
| `Tab`, `1`, `2` | Switch Accounts/Projects section |
| `Enter` | Edit selected account/project |
| `T` | On the Accounts tab, open an SSH shell for the selected SFTP account; return to Manage when the shell exits |
| `N` | New account/project |
| `D` | Delete (confirmation; warns when an account is still referenced) |
| `S` | Write config to disk (forms already auto-save) |
| `Q` | Quit (`y` saves before quitting when there are unsaved changes) |

Inside an editor:

| Key | Action |
|---|---|
| `↑`/`↓`, `Tab`, `Shift+Tab` | Move between fields |
| `Tab` on a path field | Autocomplete: local dirs, `~/.ssh` key files, env var names, and remote directories listed live over the connection |
| `←`/`→`, `Space` | Cycle select fields and on/off toggles |
| `Enter` | Open the option list (selects with many options) or save the form |
| `Esc` | Cancel |
| `Ctrl+T` | Test the connection with the current values |

Inside the Path mappings editor:

| Key | Action |
|---|---|
| `↑`/`↓` | Select a mapping row |
| `Ctrl+←`/`Ctrl+→`, `Shift+Tab` | Select the local or remote column |
| `Tab` | Autocomplete local folders under Local root or remote folders over the live connection |
| `↑`/`↓`, `PageUp`/`PageDown`, `Home`/`End` | Browse a completion list while it is open |
| `Ctrl+N` / `Ctrl+D` | Add / remove a mapping |
| `Ctrl+↑` / `Ctrl+↓` | Reorder a mapping |
| `Enter` / `Esc` | Apply / cancel mapping changes |

Connection testing: `Ctrl+T` uses the typed secret first, then the stored
one, then `password_env`. Secrets are never written to `config.yaml` — the
"Password" / "Key passphrase" field stores them in `secrets.yaml` next to
the config file (created with `0600` permissions). Typing a new value
replaces the stored secret; `Ctrl+X` on that field clears it; leaving it
empty keeps the stored value. `password_env` remains an alternative source.

Build:

```bash
go build -o godeploy ./cmd/godeploy
```

Windows:

```powershell
New-Item -ItemType Directory -Force "$HOME\.config\godeploy"
Copy-Item .\config.example.yaml "$HOME\.config\godeploy\config.yaml"
go mod tidy
.\build.ps1            # production build (trimpath + stripped symbols)
.\build.ps1 -Debug     # same as `make build` (go build -o godeploy.exe .\cmd\godeploy)
.\godeploy.exe -p api-backend
```

## TUI keys

Project picker (`godeploy` without `-p`):

| Key | Action |
|---|---|
| `↑`/`↓`, `j`/`k` | Move within the visible Projects or Accounts list |
| `Enter` | Projects: start sync. Accounts: open an SSH shell. |
| `T` | Projects: shell into the highlighted project's account. Accounts: shell into the highlighted account. |
| `A`, `Tab` | Show all accounts, including accounts with no linked projects |
| `P`, `Tab` | Return to the Projects list |
| `1`–`9` | Jump to item N in the visible list |
| `q`, `Esc` | Quit |

Folder browser (after selecting a project, unless `-path` is given):

| Key | Action |
|---|---|
| `↑`/`↓`, `j`/`k` | Move between entries |
| `Enter` on a folder | Descend into it |
| `Enter` on a file | Mark/unmark the file for deployment |
| `Space` | Mark/unmark the file or folder for deployment (multiple allowed; marking a folder includes its whole subtree) |
| `Enter` on `(select this folder)` | Deploy the marked files/folders (or the current folder if none marked) |
| `s` | Deploy the marked files/folders (or the current folder) |
| `←`, `Backspace`, `..` | Go up one level |
| `→` | Descend into the highlighted folder |
| `/` | Search folders and files anywhere under the project. The recursive index loads in the background, abbreviated segments work (`k/p/a` matches `kiosks/public_html/actions`), and ordered filters can narrow by path, type, extension, or contents. Arrows pick, `Enter` opens, `Alt+M` marks, `Alt+E` edits, and `Esc` returns to the list. `Tab` still autocompletes a plain path bash-style. |
| `r` | Browse the **remote source** instead of syncing — download any file or folder from the server, including files the sync would ignore (see below) |
| `u` | Browse **local files for upload** instead of syncing — upload anything, ignoring `.gitignore` (see below) |
| `t` | Open an **interactive ssh shell** into the project's server using the account credentials (see below) |
| `e` | Open the configured terminal **editor** on the entry under the cursor (see below) |
| `q`, `Esc` | Quit |

The `/` search is an ordered pipeline separated by `|`. A plain first stage is
an implicit `%s` path search; use `%s` explicitly when placing a path search
later in the pipeline. Stages run left-to-right, so put a path or extension
filter before `%contents` to avoid opening unrelated files.

| Search | Meaning |
|---|---|
| `actions/ \| %ext *.php \| %contents: machines_demo` | PHP files below any matching `actions` path containing the literal text `machines_demo` |
| `actions \| %type: folder` | Matching folders only |
| `edit2 \| %type: file` | Matching files only |
| `%type: folder \| %s actions` | The same folder search with the path stage explicitly placed second |

`%type` accepts `file`, `folder`, `dir`, or `directory`. `%ext` accepts `php`,
`.php`, or glob forms such as `*.php`. Content matching is literal and
case-sensitive.

Remote source browser (press `r` in the folder browser, or run `godeploy -p <id> -remote`):

| Key | Action |
|---|---|
| `↑`/`↓`, `j`/`k` | Move between entries |
| `Enter` on a folder | Descend into it |
| `Enter` on a file | Mark/unmark the file for download |
| `Space` | Mark/unmark the file or folder (marking a folder downloads its whole subtree) |
| `s` / `Enter` on `(select marked)` | Start the download flow |
| `e` on a file | Download it to a temp file and open it in the configured editor — every save re-uploads it, and the temp file is cleaned up when you quit the editor (see below) |
| `←`, `Backspace`, `..` | Go up one level (`..` links the mapping roots and `/`, so anything on the server is reachable) |
| `→` | Descend into the highlighted folder |
| `/` | Filter the current folder's listing by name (files and folders; `Alt+M` marks the highlighted match, `Alt+E` edits it) |
| `q`, `Esc` | Back to the folder selection |

Remote downloads: nothing is filtered — files excluded by `.gitignore` or the project's `exclusions` are listed and downloadable. When a marked remote path sits under a `path_mapping`, it downloads to the project path (`local_root` + `local_path` + the suffix below the mapping root). Paths outside every mapping open a local folder picker (starting at `~`) and the absolute remote path is preserved under the chosen destination. A review list shows every file as `remote path → local destination`; `Enter` downloads with live progress, then `r` returns to the project picker and `b` re-opens the remote browser.

Local source browser (press `u` in the folder browser, or run `godeploy -p <id> -upload`):

| Key | Action |
|---|---|
| `↑`/`↓`, `j`/`k` | Move between entries |
| `Enter` on a folder | Descend into it |
| `Enter` on a file | Mark/unmark the file for upload |
| `Space` | Mark/unmark the file or folder (marking a folder uploads its whole subtree) |
| `s` / `Enter` on `(select marked)` | Start the upload flow |
| `e` | Toggle project `exclusions`: OFF (default) uploads everything, ON skips dimmed `⊘` entries |
| `Alt+E` | Open the configured editor on the entry under the cursor (local file — no transfer needed) |
| `←`, `Backspace`, `..` | Go up one level (`..` links the mapping roots and the project root) |
| `→` | Descend into the highlighted folder |
| `/` | Filter the current folder's listing by name (files and folders; `Alt+M` marks the highlighted match, `Alt+E` edits it) |
| `q`, `Esc` | Back to the folder selection |

Local uploads: the browser starts at the project's mapped local paths (each labeled with its remote target) and lists **everything** — `.gitignore` never hides a file here, so gitignored files upload too. Files and folders matching the project's `exclusions` are shown dimmed with a `⊘` marker; the `e` key toggles whether they are skipped when building the upload list (OFF by default — everything uploads). If a marked path itself matches an exclusion, the whole folder is excluded outright regardless of the toggle. When a marked local path sits under a `path_mapping`, it uploads to the mapped remote path (`remote_path` + the suffix below the mapping's local folder). Paths outside every mapping open a remote folder picker (starting at `/`) and the project-relative path is preserved under the chosen destination. A review list shows every file as `local path → remote path`; `Enter` uploads with live progress, then `r` returns to the project picker and `b` re-opens the local browser.

Interactive shell (press `T` in the startup picker or Manage Accounts tab, press `t` in the folder browser, or run `godeploy -p <id> -shell`): godeploy exits the TUI and hands the terminal to the system `ssh` client with the account's host, port, username and key — the shell itself is a normal ssh session (prompts, pty, and Ctrl+C all handled by ssh). The startup Accounts list includes unlinked accounts. When the stored secret (`secrets.yaml`) covers the account, it is fed to ssh via `SSH_ASKPASS`; otherwise ssh prompts interactively. FTP/FTPS accounts remain visible but show an SFTP-only hint instead of opening a shell, and `-shell` cannot be combined with `-remote` or `-upload`. Exit the shell to return to the screen that launched it.

Editor (`e` in the folder browser, or run `godeploy -p <id>` with `editor: "hx"` set in config.yaml): the editor command is a plain command line (quotes honored, `~` expanded, no shell) — GUI editors that detach need a wait flag (e.g. `code --wait`). godeploy exits the TUI, hands the terminal to the editor on the file or folder under the cursor, and returns to the folder browser with your marks intact when you quit the editor. In the remote source browser, `e` on a remote file downloads it to a temp copy, opens it in the editor, **re-uploads on every save** (debounced ~0.4 s, watched via the file system), and uploads the final state when you quit. If the editor exits with an error and the file changed, the temp copy is kept for recovery and the remote is left at the last uploaded version. Leave `editor` empty to disable the editor keys.

Sync review:

Text files or generated unified diffs larger than 1,000 lines are still marked
for upload/download, but the preview shows `(diff too large)` instead of
building and syntax-highlighting the full diff. Press `v` to explicitly load
and render the full diff anyway. Files over 1 MiB and log files
(`*.log` and rotated `*.log.*`) never enter the in-memory text diff engine;
they are compared accurately with streaming SHA-256 and show why the text diff
was skipped in the review. The `v` override is available for oversized text
files, but not for files skipped because they are logs.

| Key | Action |
|---|---|
| `j` / `k`, arrows | Select file |
| `Tab` | Focus the diff in a full-width view / return to the file list |
| `v` | Load and view an oversized text diff anyway |
| `Space` | Cycle action |
| `f` | Toggle filter: pending actions only / all files (default: pending only) |
| `u` | Upload |
| `d` | Download |
| `x` | Delete local/remote |
| `s` | Skip (the row stays visible so the choice can be reversed without rescanning) |
| `i` | Ignore the file: adds it to the project's `exclusions` in config.yaml and removes it from the queue (no rescan needed) |
| `PageUp` / `PageDown` | Page through the list, or scroll the diff when focused |
| `Enter` | Execute selected plan |
| `r` | After a deploy: back to the project picker |
| `b` | After a deploy: back to the folder browser (same project, pick more folders) |
| `q` | Quit |

## Structure

```text
cmd/godeploy/              CLI entry point
internal/config/           YAML schema/loading
internal/domain/           transport-independent types/interfaces
internal/diff/             hashing/text normalization/unified diff
internal/ignore/           .gitignore and glob matching
internal/local/            local filesystem scanner
internal/remote/           protocol client factory
internal/remote/sftpclient SFTP/SSH implementation
internal/remote/ftpclient  FTP/FTPS implementation
internal/remotebrowser/   remote source browser (download outside sync)
internal/syncer/           remote scan, planning, execution
internal/manage/           interactive config manager TUI (-manage)
internal/picker/           interactive project picker TUI
internal/tui/              Bubble Tea review UI
docs/ARCHITECTURE.md        planning/execution rules and next milestones
```

## Notes

The starter intentionally does not write passwords to `config.yaml`. Use
`password_env`, interactive prompting, or the `-manage` TUI, which stores
secrets in `secrets.yaml` (0600) next to the config file. SFTP host-key
verification is currently permissive so the starter is easy to bring up;
replace `ssh.InsecureIgnoreHostKey()` with a `known_hosts` callback before
production use.

The comment-stripping text normalizer is regex-based and language-agnostic. It is suitable as a deploy-diff heuristic, not as a source parser.
