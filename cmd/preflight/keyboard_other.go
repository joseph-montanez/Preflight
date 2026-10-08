//go:build !windows

package main

import "os"

// newConsoleHook uses the plain blocking file read on non-windows systems.
// keyboardInput additionally exposes Fd()/Name() here so muesli/cancelreader
// picks its select-based cancel path (instead of the no-op fallback) and the
// read loop cannot outlive the program to swallow the next program's first
// keypress (charmbracelet/bubbletea#1167).
func newConsoleHook(inner *os.File) consoleHook {
	return &plainConsole{in: inner}
}

// Fd lets cancelreader wait on stdin with select/poll and interrupt reads.
func (k *keyboardInput) Fd() uintptr { return k.inner.Fd() }

// Name is part of the cancelreader.File interface.
func (k *keyboardInput) Name() string { return k.inner.Name() }
