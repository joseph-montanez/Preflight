package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"preflight/internal/browser"
	"preflight/internal/config"
	diffengine "preflight/internal/diff"
	"preflight/internal/domain"
	"preflight/internal/git"
	ignoreengine "preflight/internal/ignore"
	"preflight/internal/localbrowser"
	"preflight/internal/manage"
	"preflight/internal/picker"
	"preflight/internal/remote"
	"preflight/internal/remotebrowser"
	"preflight/internal/syncer"
	"preflight/internal/taskrun"
	"preflight/internal/tui"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"
)

// runProgram creates and runs a tea program wired to the real terminal. The
// input goes through keyboardInput, which translates CSI-u (kitty keyboard
// protocol) sequences into the legacy escape forms bubbletea understands.
// makeStdinRaw ensures os.Stdin is in raw mode and emits VT sequences.
func runProgram(model tea.Model, opts ...tea.ProgramOption) (tea.Model, error) {
	restore, err := makeStdinRaw()
	if err != nil {
		return nil, err
	}
	defer restore()

	p := tea.NewProgram(model, append([]tea.ProgramOption{tea.WithInput(&keyboardInput{inner: os.Stdin})}, opts...)...)
	return p.Run()
}

func makeStdinRaw() (func(), error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return func() {}, nil
	}
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	return func() {
		_ = term.Restore(fd, oldState)
	}, nil
}

// remoteFlow reports how the remote-browse flow ended, so the main loop
// knows where to continue.
type remoteFlow int

const (
	remoteExit          remoteFlow = iota // user quit after downloads — exit the app
	remoteBackToBrowser                   // user quit the remote browser — re-run folder selection
	remoteContinueOuter                   // user asked for another project — restart the loop
)

func main() {
	configPath := flag.String("config", config.DefaultPath(), "path to config.yaml")
	projectID := flag.String("p", "", "project id")
	scope := flag.String("path", "", "relative project subfolder to synchronize")
	workers := flag.Int("workers", 4, "concurrent transfer workers")
	compareConns := flag.Int("compare", 4, "parallel connections for comparing files")
	manageMode := flag.Bool("manage", false, "manage accounts/projects in an interactive config editor")
	remoteFlag := flag.Bool("remote", false, "browse the remote source and download files instead of syncing")
	uploadFlag := flag.Bool("upload", false, "browse local files and upload them instead of syncing")
	shellFlag := flag.Bool("shell", false, "open an interactive ssh shell into the project's account")
	runFlag := flag.String("run", "", "run a saved task by id")
	gitFlag := flag.Bool("git", false, "sync files changed in git status")
	askpassFlag := flag.Bool("askpass", false, "internal: print the stored secret for ssh (used via SSH_ASKPASS)")
	flag.Parse()

	// -askpass, or an ssh askpass invocation: ssh spawns this binary with
	// the prompt as a positional argument, so detect that shape too (the
	// secret env var is only set by configureAskPass).
	if *askpassFlag || (os.Getenv("PREFLIGHT_ASKPASS_SECRET") != "" && len(flag.Args()) > 0) {
		askpassMode()
		return
	}

	if *remoteFlag && *uploadFlag {
		fatalIf(fmt.Errorf("-remote and -upload are mutually exclusive"))
	}
	if *shellFlag && (*remoteFlag || *uploadFlag) {
		fatalIf(fmt.Errorf("-shell cannot be combined with -remote or -upload"))
	}
	if *gitFlag && *scope != "" {
		fatalIf(fmt.Errorf("-git and -path are mutually exclusive"))
	}
	if *gitFlag && (*remoteFlag || *uploadFlag || *shellFlag) {
		fatalIf(fmt.Errorf("-git cannot be combined with -remote, -upload, or -shell"))
	}
	if *runFlag != "" && (*projectID != "" || *scope != "" || *remoteFlag || *uploadFlag || *shellFlag || *gitFlag) {
		fatalIf(fmt.Errorf("-run cannot be combined with -p, -path, -remote, -upload, -shell, or -git"))
	}

	if *manageMode {
		cfg, err := config.LoadOrEmpty(*configPath)
		fatalIf(err)
		runManageLoop(cfg, *configPath)
		return
	}

	cfg, err := config.LoadOrEmpty(*configPath)
	fatalIf(err)
	fatalIf(cfg.ValidateTasks())
	secrets, err := config.LoadSecrets(config.SecretsPath(*configPath))
	fatalIf(err)

	// Direct task entry: run and exit without touching the launcher.
	if *runFlag != "" {
		task, err := cfg.Task(*runFlag)
		fatalIf(err)
		fatalIf(runTaskFlow(cfg, secrets, *task, os.Stderr))
		return
	}

	forcedAction, hasForced := actionFromFlags(*remoteFlag, *uploadFlag, *shellFlag)

	// The launcher action drives the project flow below: sync runs the
	// scope picker + compare/review, while upload/download skip straight
	// to their browsers. -p with mode flags selects the action directly;
	// otherwise the launcher picks it.
	launcherAction := picker.ActionSync
	if hasForced {
		launcherAction = forcedAction
	}
	var project *config.Project
	if *projectID != "" {
		var err error
		project, err = cfg.Project(*projectID)
		fatalIf(err)
		fatalIf(project.ValidatePathMappings())
	}

	// Loop so the review TUI can send us back: r = back to the launcher,
	// b = re-browse folders for the same project, and the remote browser
	// can return to the folder selection.
	skipProjectPick := false
	var lastMarks []string // sync-browser marks restored after editor detours
	for {
		if *projectID == "" && !skipProjectPick {
			// Action-first launcher: pick what to do, then the filtered
			// target. SSH and Manage run here and return to the
			// launcher; a project choice drops into the flow below.
			// [r] from a review flow clears the project and returns here.
			if len(cfg.Projects) == 0 && len(cfg.Accounts) == 0 {
				fmt.Fprintln(os.Stderr, "No projects or accounts defined. Pick Manage to add one")
			}
			act, pid, aid, tid, ok, err := pickLauncher(cfg.Projects, cfg.Accounts, cfg.Tasks, forcedAction, hasForced)
			fatalIf(err)
			if !ok {
				return
			}
			switch act {
			case picker.ActionManage:
				runManageLoop(cfg, *configPath)
				// The manager writes on save, so re-read before
				// showing the launcher again.
				cfg, err = config.LoadOrEmpty(*configPath)
				fatalIf(err)
				fatalIf(cfg.ValidateTasks())
				secrets, err = config.LoadSecrets(config.SecretsPath(*configPath))
				fatalIf(err)
				continue
			case picker.ActionSSH:
				account, err := cfg.Account(aid)
				fatalIf(err)
				fatalIf(openShell(*account, secrets))
				continue
			case picker.ActionRun:
				task, err := cfg.Task(tid)
				fatalIf(err)
				fatalIf(runTaskFlow(cfg, secrets, *task, os.Stderr))
				continue
			default:
				*projectID = pid
				launcherAction = act
				project, err = cfg.Project(pid)
				fatalIf(err)
				fatalIf(project.ValidatePathMappings())
			}
		}
		skipProjectPick = false

		// Without an explicit -path, let the user pick the sync scopes from
		// the local file system BEFORE any connection or scan happens. The
		// folder browser also offers e = open the configured editor on the
		// entry under the cursor; upload, download, and shell are chosen
		// up front in the launcher instead.
		// The flow comes from the launcher action (-p seeds it from the
		// mode flags), so a stale mode flag can never leak into an
		// explicitly picked action (e.g. -remote, then Esc to Sync).
		remoteBrowse := launcherAction == picker.ActionDownload
		localUpload := launcherAction == picker.ActionUpload
		openShellMode := launcherAction == picker.ActionSSH
		openEditorMode := false
		editTarget := ""
		var syncScopes []string
		if *scope != "" {
			syncScopes = []string{*scope}
		} else if *gitFlag && !remoteBrowse && !localUpload && !openShellMode {
			gitPaths, err := git.ChangedPaths(project.LocalRoot, project.Exclusions)
			fatalIf(err)
			if len(gitPaths) == 0 {
				fmt.Fprintln(os.Stderr, "preflight: git working tree clean, nothing to sync")
				return
			}
			mappings := project.EffectivePathMappings()
			var mapped []string
			for _, p := range gitPaths {
				if config.IsMappingCovered(p, false, mappings) {
					mapped = append(mapped, p)
				}
			}
			if len(mapped) == 0 {
				fmt.Fprintln(os.Stderr, "preflight: git working tree has no changes in mapped folders")
				return
			}
			syncScopes = mapped
		} else if !remoteBrowse && !localUpload && !openShellMode {
			initialMarks := lastMarks
			mappings := project.EffectivePathMappings()
			if project.GitStatus && len(initialMarks) == 0 {
				if gitPaths, err := git.ChangedPaths(project.LocalRoot, project.Exclusions); err == nil {
					for _, p := range gitPaths {
						if config.IsMappingCovered(p, false, mappings) {
							initialMarks = append(initialMarks, p)
						}
					}
				}
			}
			picked, edit, target, marks, ok, err := browseProject(project.LocalRoot, cfg.Editor, project.Exclusions, mappings, initialMarks)
			fatalIf(err)
			if !ok {
				return
			}
			openEditorMode = edit
			editTarget = target
			lastMarks = marks
			syncScopes = picked
		}
		for _, s := range syncScopes {
			if err := validateScope(s); err != nil {
				fatalIf(err)
			}
		}

		account, err := cfg.Account(project.AccountID)
		fatalIf(err)
		prompt := cachedPrompt(promptFor(account.ID, secrets))

		if remoteBrowse {
			switch runRemoteBrowse(*project, *account, prompt, *workers, cfg.Editor) {
			case remoteExit:
				return
			case remoteBackToBrowser:
				skipProjectPick = true
				continue
			case remoteContinueOuter:
				// [r]: back to the launcher.
				*projectID = ""
				continue
			}
		}

		if localUpload {
			switch runLocalUpload(*project, *account, prompt, *workers, cfg.Editor) {
			case remoteExit:
				return
			case remoteBackToBrowser:
				skipProjectPick = true
				continue
			case remoteContinueOuter:
				// [r]: back to the launcher.
				*projectID = ""
				continue
			}
		}

		if openShellMode {
			// Only reachable via -p with -shell: shell into the
			// project's account, then back to its folder browser.
			if err := openShell(*account, secrets); err != nil {
				fatalIf(err)
			}
			skipProjectPick = true
			continue
		}

		if openEditorMode {
			if err := openEditor(cfg.Editor, editTarget, project.LocalRoot); err != nil {
				fatalIf(err)
			}
			// Back to the folder browser for the same project with the
			// marked set restored, so "edit a file, jump back, continue
			// the sync" loses nothing.
			skipProjectPick = true
			continue
		}

		// Open one connection per compare worker so the compare phase can
		// diff files in parallel across independent transports.
		conns := *compareConns
		if conns < 1 {
			conns = 1
		}
		clients := make([]domain.RemoteClient, 0, conns)
		for i := 0; i < conns; i++ {
			c, err := remote.NewClient(*account, prompt)
			fatalIf(err)
			if err := c.Connect(); err != nil {
				fatalIf(err)
			}
			clients = append(clients, c)
		}
		client := clients[0]
		fmt.Fprintf(os.Stderr, "Connecting to %s (%d connections)...\n", account.Name, conns)

		ignores, err := ignoreengine.New(project.LocalRoot, project.Exclusions)
		fatalIf(err)
		planner := &syncer.Planner{
			Project:        *project,
			Client:         client,
			Diff:           diffengine.NewEngine(),
			Ignore:         ignores,
			Scopes:         syncScopes,
			CompareClients: clients,
		}
		sink := syncer.NewTransferSink()
		executor := &syncer.Executor{
			LocalRoot:  project.LocalRoot,
			RemoteRoot: project.RemoteRoot,
			Client:     client,
			Workers:    *workers,
			OnEvent:    sink.Record,
			OnProgress: sink.RecordProgress,
		}
		execute := func(ctx context.Context, items []*domain.SyncFileItem) error {
			return executor.Execute(ctx, items)
		}
		// The review TUI's [I]gnore key: append the file's project-relative
		// path to the project exclusions, persist config.yaml, and teach the
		// running ignore engine so rescanning skips it too.
		ignore := func(projectPath string) error {
			pattern := strings.ReplaceAll(projectPath, "\\", "/")
			for _, ex := range project.Exclusions {
				if ex == pattern {
					return nil
				}
			}
			project.Exclusions = append(project.Exclusions, pattern)
			if err := cfg.Save(*configPath); err != nil {
				return err
			}
			ignores.Add(pattern)
			return nil
		}
		build := func(onProgress func(stage string, done, total int)) ([]*domain.SyncFileItem, error) {
			planner.OnProgress = onProgress
			return planner.Build()
		}
		model := tui.New(project.Name, project.RemoteSummary(), strings.ToUpper(account.Protocol)+" "+account.ID, domain.DiffMode(project.DefaultDiffMode), build, execute, sink, ignore).WithDiffLoader(planner.LoadTextDiff)
		final, err := runProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())
		fatalIf(err)
		for _, c := range clients {
			_ = c.Close()
		}

		f := final.(tui.Model)
		if f.ReBrowse() {
			continue
		}
		if f.GoBack() {
			// [r] after a deploy: back to the launcher.
			*projectID = ""
			continue
		}
		break
	}
}

// runRemoteBrowse runs the remote source browser and the download flow on
// a single connection. The user picks files and folders to download even
// when a sync would ignore them; mapped paths go to the project's local
// path, unmapped paths ask for a destination directory. The [E] key on
// a remote file hands control to the configured editor (see
// openRemoteEdit) and re-opens the browser with the marks restored.
func runRemoteBrowse(project config.Project, account config.Account, prompt remote.SecretPrompt, workers int, editorCmd string) remoteFlow {
	client, err := remote.NewClient(account, prompt)
	fatalIf(err)
	if err := client.Connect(); err != nil {
		fatalIf(err)
	}
	defer client.Close()
	fmt.Fprintf(os.Stderr, "Connecting to %s...\n", account.Name)

	var marks []string // selection preserved across edit sessions

	for {
		// 1. Browse the remote source.
		model := remotebrowser.New(client, project.EffectivePathMappings(), project.LocalRoot, editorCmd)
		model.RestoreMarks(marks)
		final, err := runProgram(model, tea.WithAltScreen())
		fatalIf(err)
		b := final.(remotebrowser.Model)
		if editPath := b.EditRequest(); editPath != "" {
			marks = b.MarkedPaths()
			newWatchClient := func() (domain.RemoteClient, error) {
				c, err := remote.NewClient(account, prompt)
				if err != nil {
					return nil, err
				}
				if err := c.Connect(); err != nil {
					return nil, err
				}
				return c, nil
			}
			if err := openRemoteEdit(client, newWatchClient, editorCmd, editPath); err != nil {
				fatalIf(err)
			}
			continue // back to the browser with the selection restored
		}
		roots, ok := b.Selected()
		if !ok || len(roots) == 0 {
			// Nothing selected: back to the local folder selection.
			return remoteBackToBrowser
		}

		// 2. Resolve destinations. Mapped selections already carry their
		// project-relative local path; unmapped ones share one destination
		// picker (the remote path is preserved under the chosen base).
		destBase := ""
		hasUnmapped := false
		for _, root := range roots {
			if root.LocalPath == "" {
				hasUnmapped = true
				break
			}
		}
		if hasUnmapped {
			home, _ := os.UserHomeDir()
			if home == "" {
				home = "."
			}
			picked, ok, err := pickDestination(home)
			fatalIf(err)
			if !ok {
				// Destination cancelled: back to the remote browser.
				continue
			}
			destBase = picked
		}

		// 3. Review + download. The build callback walks the selected
		// remote paths (with progress) and emits pre-resolved download
		// items; the review TUI confirms and executes them.
		sink := syncer.NewTransferSink()
		executor := &syncer.Executor{
			Client:     client,
			Workers:    workers,
			OnEvent:    sink.Record,
			OnProgress: sink.RecordProgress,
		}
		build := func(onProgress func(stage string, done, total int)) ([]*domain.SyncFileItem, error) {
			return buildDownloadItems(client, roots, destBase, onProgress)
		}
		execute := func(ctx context.Context, items []*domain.SyncFileItem) error {
			return executor.Execute(ctx, items)
		}
		model2 := tui.New(project.Name, project.RemoteSummary(), strings.ToUpper(account.Protocol)+" "+account.ID, "", build, execute, sink, nil, tui.TransferDownload)
		final2, err := runProgram(model2, tea.WithAltScreen(), tea.WithMouseCellMotion())
		fatalIf(err)
		f := final2.(tui.Model)
		if f.ReBrowse() {
			continue // [b]: browse the remote source again for this project
		}
		if f.GoBack() {
			return remoteContinueOuter // [r]: back to the project picker
		}
		return remoteExit
	}
}

// buildDownloadItems converts the selected download roots into download
// items. A marked folder is walked recursively (ignore-free) with every
// file keeping its relative path below the root; a marked file becomes a
// single item. Progress reports the walk stage.
func buildDownloadItems(client domain.RemoteClient, roots []remotebrowser.DownloadRoot, destBase string, onProgress func(stage string, done, total int)) ([]*domain.SyncFileItem, error) {
	var items []*domain.SyncFileItem
	done := 0
	progress := func() {
		done++
		if onProgress != nil {
			onProgress("walking remote files", done, len(roots))
		}
	}
	for _, root := range roots {
		localBase := root.LocalPath
		if localBase == "" {
			// Unmapped: preserve the absolute remote path under the picked
			// destination base (minus the leading slash).
			localBase = filepath.Join(destBase, filepath.FromSlash(strings.TrimPrefix(root.RemotePath, "/")))
		}
		meta, err := client.Stat(root.RemotePath)
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", root.RemotePath, err)
		}
		if meta.Mode.IsDir() {
			if err := syncer.WalkRemote(client, root.RemotePath, func(remotePath string, fileMeta *domain.FileMeta) {
				rel := strings.TrimPrefix(remotePath, root.RemotePath)
				localPath := filepath.Join(localBase, filepath.FromSlash(strings.TrimPrefix(rel, "/")))
				items = append(items, &domain.SyncFileItem{
					RelativePath: remotePath,
					RemotePath:   remotePath,
					LocalPath:    localPath,
					RemoteStat:   fileMeta,
					Action:       domain.ActionDownload,
					DiffDetails:  &domain.DiffResult{Summary: "→ " + localPath},
				})
			}); err != nil {
				return nil, err
			}
		} else {
			items = append(items, &domain.SyncFileItem{
				RelativePath: root.RemotePath,
				RemotePath:   root.RemotePath,
				LocalPath:    localBase,
				RemoteStat:   meta,
				Action:       domain.ActionDownload,
				DiffDetails:  &domain.DiffResult{Summary: "→ " + localBase},
			})
		}
		progress()
	}
	return items, nil
}

// runLocalUpload runs the local source browser and the upload flow on a
// single connection. The user picks local files and folders to upload
// even when a sync would ignore them; mapped paths go to their resolved
// remote path, unmapped paths ask for a remote destination. Project
// exclusions are dimmed in the browser and applied via the e-toggle;
// a picked root that itself matches an exclusion is excluded outright.
// The [Alt+E] key opens the configured editor on the entry under the
// cursor (local file — no transfer) and re-opens the browser with the
// marks restored.
func runLocalUpload(project config.Project, account config.Account, prompt remote.SecretPrompt, workers int, editorCmd string) remoteFlow {
	client, err := remote.NewClient(account, prompt)
	fatalIf(err)
	if err := client.Connect(); err != nil {
		fatalIf(err)
	}
	defer client.Close()
	fmt.Fprintf(os.Stderr, "Connecting to %s...\n", account.Name)

	ignores := ignoreengine.NewExclusionsOnly(project.Exclusions)
	mappings := project.EffectivePathMappings()
	var marks []string // selection preserved across edit sessions

	for {
		// 1. Browse local files.
		model := localbrowser.New(project.LocalRoot, mappings, ignores, editorCmd)
		model.RestoreMarks(marks)
		final, err := runProgram(model, tea.WithAltScreen())
		fatalIf(err)
		b := final.(localbrowser.Model)
		if editPath := b.EditRequest(); editPath != "" {
			marks = b.MarkedPaths()
			if err := openEditor(editorCmd, editPath, project.LocalRoot); err != nil {
				fatalIf(err)
			}
			continue // back to the browser with the selection restored
		}
		roots, filterOn, ok := b.Selected()
		if !ok || len(roots) == 0 {
			// Nothing selected: back to the local folder selection.
			return remoteBackToBrowser
		}

		// 2. Resolve destinations. Mapped selections already carry their
		// remote path; unmapped ones share one remote destination picker
		// (the project-relative local path is preserved under the base).
		destBase := ""
		hasUnmapped := false
		for _, root := range roots {
			if root.RemotePath == "" {
				hasUnmapped = true
				break
			}
		}
		if hasUnmapped {
			picked, ok, err := pickRemoteDestination(client)
			fatalIf(err)
			if !ok {
				// Destination cancelled: back to the local browser.
				continue
			}
			destBase = picked
		}

		// 3. Review + upload. The build callback walks the selected local
		// paths (with progress) and emits pre-resolved upload items; the
		// review TUI confirms and executes them.
		sink := syncer.NewTransferSink()
		executor := &syncer.Executor{
			Client:     client,
			Workers:    workers,
			OnEvent:    sink.Record,
			OnProgress: sink.RecordProgress,
		}
		build := func(onProgress func(stage string, done, total int)) ([]*domain.SyncFileItem, error) {
			return buildUploadItems(project.LocalRoot, mappings, roots, destBase, filterOn, ignores, onProgress)
		}
		execute := func(ctx context.Context, items []*domain.SyncFileItem) error {
			return executor.Execute(ctx, items)
		}
		model2 := tui.New(project.Name, project.RemoteSummary(), strings.ToUpper(account.Protocol)+" "+account.ID, "", build, execute, sink, nil, tui.TransferUpload)
		final2, err := runProgram(model2, tea.WithAltScreen(), tea.WithMouseCellMotion())
		fatalIf(err)
		f := final2.(tui.Model)
		if f.ReBrowse() {
			continue // [b]: browse local files again for this project
		}
		if f.GoBack() {
			return remoteContinueOuter // [r]: back to the project picker
		}
		return remoteExit
	}
}

// pickRemoteDestination runs the remote browser in single-select mode
// and returns the absolute destination folder the user chose.
func pickRemoteDestination(client domain.RemoteClient) (string, bool, error) {
	model := remotebrowser.PickRemoteDir(client, "/")
	final, err := runProgram(model, tea.WithAltScreen())
	if err != nil {
		return "", false, err
	}
	dir, ok := final.(remotebrowser.Model).PickedDir()
	return dir, ok, nil
}

// buildUploadItems converts the selected upload roots into upload items.
// A marked folder is walked recursively (gitignore-free, exclusions
// applied only when filterOn) with every file keeping its relative path
// below the root; a marked file becomes a single item. A picked root
// that itself matches an exclusion pattern is skipped outright (the
// root-match rule), regardless of the toggle. Progress reports the walk
// stage.
func buildUploadItems(localRoot string, mappings []config.PathMapping, roots []localbrowser.UploadRoot, destBase string, filterOn bool, ignores *ignoreengine.Engine, onProgress func(stage string, done, total int)) ([]*domain.SyncFileItem, error) {
	var items []*domain.SyncFileItem
	done := 0
	progress := func() {
		done++
		if onProgress != nil {
			onProgress("walking local files", done, len(roots))
		}
	}
	for _, root := range roots {
		projectRel, err := filepath.Rel(localRoot, root.LocalPath)
		if err != nil {
			return nil, fmt.Errorf("rel %s: %w", root.LocalPath, err)
		}
		projectRel = filepath.ToSlash(projectRel)
		if ignores != nil && ignores.RootIgnored(projectRel, true) {
			if onProgress != nil {
				onProgress("skipped "+root.LocalPath+" (matches an exclusion)", done+1, len(roots))
			}
			progress()
			continue
		}
		info, err := os.Stat(root.LocalPath)
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", root.LocalPath, err)
		}
		remoteBase := root.RemotePath
		if remoteBase == "" {
			// Unmapped: preserve the project-relative path under the
			// picked destination base.
			remoteBase = path.Join(destBase, projectRel)
		}
		if info.IsDir() {
			ignored := func(abs, walkRel string, isDir bool) bool {
				if !filterOn || ignores == nil {
					return false
				}
				rel, err := filepath.Rel(localRoot, abs)
				if err != nil {
					return false
				}
				return ignores.Ignored(filepath.ToSlash(rel), walkRel, isDir)
			}
			if err := syncer.WalkLocal(root.LocalPath, ignored, func(localPath string, meta *domain.FileMeta) {
				walkRel, _ := filepath.Rel(root.LocalPath, localPath)
				remote := path.Join(remoteBase, filepath.ToSlash(walkRel))
				items = append(items, &domain.SyncFileItem{
					RelativePath: localPath,
					LocalPath:    localPath,
					RemotePath:   remote,
					LocalStat:    meta,
					Action:       domain.ActionUpload,
					DiffDetails:  &domain.DiffResult{Summary: "→ " + remote},
				})
			}); err != nil {
				return nil, err
			}
		} else {
			remote := remoteBase
			if root.RemotePath == "" {
				// Unmapped single file: lands directly under the base.
				remote = path.Join(destBase, filepath.Base(root.LocalPath))
			}
			items = append(items, &domain.SyncFileItem{
				RelativePath: root.LocalPath,
				LocalPath:    root.LocalPath,
				RemotePath:   remote,
				LocalStat:    &domain.FileMeta{Size: info.Size(), ModTime: info.ModTime(), Mode: info.Mode(), Exists: true},
				Action:       domain.ActionUpload,
				DiffDetails:  &domain.DiffResult{Summary: "→ " + remote},
			})
		}
		progress()
	}
	return items, nil
}

// browseProject runs the local folder browser TUI and returns the chosen
// scopes (relative to the project root; an empty list = full project),
// whether the user asked to open the editor instead, the editor target,
// the marked set (for restoration after editor detours), and whether
// the user actually picked something. Upload, download, and shell are
// chosen up front in the launcher, not here.
func browseProject(localRoot, editorCmd string, exclusions []string, mappings []config.PathMapping, restoreMarks []string) (scopes []string, editorMode bool, editTarget string, marks []string, ok bool, err error) {
	model := browser.New(localRoot, editorCmd).WithExclusions(exclusions).WithMappings(mappings)
	model.RestoreMarks(restoreMarks)
	final, err := runProgram(model, tea.WithAltScreen())
	if err != nil {
		return nil, false, "", nil, false, err
	}
	b := final.(browser.Model)
	if b.EditorMode() {
		return nil, true, b.EditTarget(), b.MarkedPaths(), true, nil
	}
	scopes, ok = b.Selected()
	return scopes, false, "", b.MarkedPaths(), ok, nil
}

// pickDestination runs the folder browser in directory mode and returns
// the absolute destination folder the user chose.
func pickDestination(root string) (string, bool, error) {
	model := browser.PickDir(root)
	final, err := runProgram(model, tea.WithAltScreen())
	if err != nil {
		return "", false, err
	}
	b := final.(browser.Model)
	sel, ok := b.Selected()
	if !ok || len(sel) == 0 {
		return "", false, nil
	}
	return sel[0], true, nil
}

// actionFromFlags maps CLI mode flags to the launcher action. -p with no
// mode flag defaults to sync at the call site.
func actionFromFlags(remote, upload, shell bool) (picker.Action, bool) {
	switch {
	case remote:
		return picker.ActionDownload, true
	case upload:
		return picker.ActionUpload, true
	case shell:
		return picker.ActionSSH, true
	default:
		return "", false
	}
}

// pickLauncher runs the startup launcher. With hasForced (a mode flag given
// without -p) it skips the action menu and picks only the filtered target;
// otherwise it shows the full action-first menu. The task ID is set only
// for the run action.
func pickLauncher(projects []config.Project, accounts []config.Account, tasks []config.Task, forced picker.Action, hasForced bool) (picker.Action, string, string, string, bool, error) {
	if hasForced {
		return pickTargetForAction(projects, accounts, tasks, forced)
	}
	return pickActionTarget(projects, accounts, tasks)
}

// pickActionTarget runs the full action-first launcher: an action menu
// followed by the filtered target list. It returns the action plus the
// chosen project, account, or task ID.
func pickActionTarget(projects []config.Project, accounts []config.Account, tasks []config.Task) (picker.Action, string, string, string, bool, error) {
	model := picker.New(projects, accounts, tasks)
	final, err := runProgram(model, tea.WithAltScreen())
	if err != nil {
		return "", "", "", "", false, err
	}
	picked := final.(picker.Model)
	return unpackLauncherPick(picked)
}

// pickTargetForAction runs the launcher directly on the filtered target
// step for a forced action (a mode flag without -p). Esc backs out to the
// full action menu, so the result is unpacked the same way.
func pickTargetForAction(projects []config.Project, accounts []config.Account, tasks []config.Task, action picker.Action) (picker.Action, string, string, string, bool, error) {
	model := picker.NewForAction(projects, accounts, tasks, action)
	final, err := runProgram(model, tea.WithAltScreen())
	if err != nil {
		return "", "", "", "", false, err
	}
	return unpackLauncherPick(final.(picker.Model))
}

// unpackLauncherPick converts a finished launcher model into the action
// plus the chosen project, account, or task ID. ok is false when the user
// quit without confirming a choice.
func unpackLauncherPick(picked picker.Model) (picker.Action, string, string, string, bool, error) {
	act := picked.SelectedAction()
	switch act {
	case "":
		return "", "", "", "", false, nil
	case picker.ActionManage:
		return act, "", "", "", true, nil
	case picker.ActionSSH:
		if picked.SelectedAccount() == "" {
			return "", "", "", "", false, nil
		}
		return act, "", picked.SelectedAccount(), "", true, nil
	case picker.ActionRun:
		if picked.SelectedTask() == "" {
			return "", "", "", "", false, nil
		}
		return act, "", "", picked.SelectedTask(), true, nil
	default:
		if picked.Selected() == "" {
			return "", "", "", "", false, nil
		}
		return act, picked.Selected(), "", "", true, nil
	}
}

// runTaskFlow executes a saved task, streaming step headers and command
// output to out. Connections open lazily per account through the same
// secret prompt as sync flows.
func runTaskFlow(cfg *config.Config, secrets *config.Secrets, task config.Task, out io.Writer) error {
	lookup := func(id string) (config.Account, error) {
		account, err := cfg.Account(id)
		if err != nil {
			return config.Account{}, err
		}
		return *account, nil
	}
	connect := func(account config.Account) (domain.RemoteClient, error) {
		fmt.Fprintf(out, "Connecting to %s...\n", account.Name)
		client, err := remote.NewClient(account, cachedPrompt(promptFor(account.ID, secrets)))
		if err != nil {
			return nil, err
		}
		if err := client.Connect(); err != nil {
			return nil, err
		}
		return client, nil
	}
	_, err := taskrun.Run(task, lookup, connect, out)
	return err
}

// runManageLoop runs the interactive config manager until it quits. A T on
// an SFTP account shells out and returns to the manager when the shell
// exits. The manager writes on save, so callers re-read the config after
// it returns.
func runManageLoop(cfg *config.Config, configPath string) {
	for {
		model := manage.New(cfg, configPath)
		final, err := runProgram(model, tea.WithAltScreen())
		fatalIf(err)
		accountID := final.(manage.Model).ShellAccountID()
		if accountID == "" {
			return
		}
		account, err := cfg.Account(accountID)
		fatalIf(err)
		secrets, err := config.LoadSecrets(config.SecretsPath(configPath))
		fatalIf(err)
		fatalIf(openShell(*account, secrets))
		// Return to the manager after the SSH session exits so another
		// account can be edited or opened without restarting preflight.
	}
}

// cachedPrompt wraps a SecretPrompt so each label is resolved once and
// reused — otherwise every parallel connection would prompt again.
func cachedPrompt(next remote.SecretPrompt) remote.SecretPrompt {
	var mu sync.Mutex
	cache := map[string]string{}
	return func(label string) (string, error) {
		mu.Lock()
		if v, ok := cache[label]; ok {
			mu.Unlock()
			return v, nil
		}
		mu.Unlock()
		v, err := next(label)
		if err != nil {
			return "", err
		}
		if v != "" {
			mu.Lock()
			cache[label] = v
			mu.Unlock()
		}
		return v, nil
	}
}

// promptFor returns a SecretPrompt that first tries the secrets file
// (per-account password/passphrase), then falls back to an interactive
// terminal prompt.
func promptFor(accountID string, secrets *config.Secrets) remote.SecretPrompt {
	return func(label string) (string, error) {
		if secrets != nil {
			if stored, ok := secrets.Accounts[accountID]; ok {
				if strings.Contains(label, "passphrase") && stored.Passphrase != "" {
					return stored.Passphrase, nil
				}
				if stored.Password != "" {
					return stored.Password, nil
				}
			}
		}
		return promptSecret(label)
	}
}

func promptSecret(label string) (string, error) {
	fmt.Fprintf(os.Stderr, "%s: ", label)
	value, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	return string(value), err
}

func validateScope(scope string) error {
	cleaned := strings.ReplaceAll(scope, "\\", "/")
	if strings.HasPrefix(cleaned, "/") || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.Contains(cleaned, "/../") {
		return fmt.Errorf("scope must be a relative path inside the project")
	}
	return nil
}

func fatalIf(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "preflight:", err)
		os.Exit(1)
	}
}
