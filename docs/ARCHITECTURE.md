# Architecture

## Layers

```text
TUI
  -> sync planner / executor
      -> diff + ignore engines
      -> local scanner
      -> remote transport interface
          -> SFTP
          -> FTP / FTPS
```

## Planning rules

- Browser-selected scopes are intersected with the project's non-overlapping
  `path_mappings`; every file receives fully resolved local and remote paths
  before comparison or execution.
- Local-only files default to Upload.
- Remote-only files default to Download.
- Existing files use the configured diff mode.
- Differing files use modification time as the default direction tie-breaker.
- Deletion is never selected automatically.
- Text mode uses normalized content to detect meaningful changes, then generates
  the unified diff from content that retains its original whitespace for display.
  Inputs are bounded to 1 MiB while reading; larger files and log files use the
  streaming binary hash path so metadata races cannot trigger an unbounded diff.
  The review UI can explicitly re-read an oversized text file and generate its
  full diff on demand.
- Binary mode prefers a remote `sha256sum` over SSH and falls back to streaming the file.

## Execution rules

1. Execute explicitly selected deletions first.
2. Ensure destination directories exist.
3. Execute upload/download transfers with the configured worker count.
4. Write transfers to `.preflight-tmp` staging files when rename is available.
5. Rename staging files over their destination.
6. Preserve modification time where supported.

FTP/FTPS operations are serialized on a single control connection. SFTP transfers can use the worker pool concurrently.

## TUI milestones

- Interactive account manager (done — `internal/manage`, launch with `-manage` or the launcher Manage action).
- Startup launcher (done — `internal/picker`; running without `-p` opens the action-first menu).
- Scope tree browser (done — `internal/browser`; pre-scan folder picker; the review `c` shortcut still just suggests a restart).
- Scan progress + scrolling/filtered review list (done — `internal/tui`: live stage/count progress, pending-only filter with `f`, paged scrolling, mouse wheel).
- Diff-mode/options modal for the `m` shortcut.
- Multi-select/check state for true batch toolbar operations.
- Per-transfer progress events and retry UI.
- Known-host management UI for SFTP.
