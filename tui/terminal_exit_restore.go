package tui

// Ports packages/tui/src/terminal.ts (Terminal.restoreOnExit, ProcessTerminal.restoreOnExit),
// packages/tui/src/tui.ts (TuiBase.getExitRestorePrefix, getExitRestoreSuffix, exitRestoreHandler) and
// packages/tui/src/tui-alt-screen.ts (TuiAltScreen.getExitRestorePrefix, getExitRestoreSuffix) at 9df819de (#11170).
//
// Pi installs a process 'exit' handler in start() and removes it in stop(): a process.exit() that skips stop() still
// runs the handler, which writes the mode resets synchronously. Go has no exit hook: os.Exit runs nothing. RestoreTerminalOnExit
// is the handler's body, and every exit path reachable while a TUI runs calls it right before os.Exit:
//
//   - coding/cli exitProcess, the funnel of every exit coding/cli.Main takes;
//   - internal/codingagent InteractiveMode.emergencyTerminalExit (exit 129), handleInterruptSignal (exit 130) and
//     uncaughtOffLoop (exit 1), which call os.Exit themselves.
//
// TestExitPathsRestoreTerminal pins that list. Hard kills, runtime aborts and a Go panic outside the recovered owner
// loop run no Go code and stay out of scope, as Pi says of kills and native aborts.

import (
	"sync"
)

// exitRestoreRegistry is what Pi's registered 'exit' handlers are: the terminals whose raw mode is on (started and not
// stopped) and the alternate-screen renderers that are active. Entries leave when the terminal restores or the renderer
// stops, so a normal stop leaves nothing for RestoreTerminalOnExit to write twice.
var exitRestoreRegistry struct {
	mu sync.Mutex
	// terminals maps a raw-mode terminal to the cooked-mode restore of its enterRawMode.
	terminals map[*ProcessTerminal]func()
	// altScreens maps an active alternate-screen renderer to whether it enabled mouse tracking.
	altScreens map[*TuiAltScreen]bool
}

func registerExitRestoreTerminal(t *ProcessTerminal, cooked func()) {
	exitRestoreRegistry.mu.Lock()
	defer exitRestoreRegistry.mu.Unlock()
	if exitRestoreRegistry.terminals == nil {
		exitRestoreRegistry.terminals = map[*ProcessTerminal]func(){}
	}
	exitRestoreRegistry.terminals[t] = cooked
}

// unregisterExitRestoreTerminal reports whether t was still registered, that is, whether the exit handler had not run.
func unregisterExitRestoreTerminal(t *ProcessTerminal) bool {
	exitRestoreRegistry.mu.Lock()
	defer exitRestoreRegistry.mu.Unlock()
	_, ok := exitRestoreRegistry.terminals[t]
	delete(exitRestoreRegistry.terminals, t)
	return ok
}

func registerExitRestoreAltScreen(t *TuiAltScreen) {
	exitRestoreRegistry.mu.Lock()
	defer exitRestoreRegistry.mu.Unlock()
	if exitRestoreRegistry.altScreens == nil {
		exitRestoreRegistry.altScreens = map[*TuiAltScreen]bool{}
	}
	exitRestoreRegistry.altScreens[t] = t.mouseEnabled
}

func unregisterExitRestoreAltScreen(t *TuiAltScreen) {
	exitRestoreRegistry.mu.Lock()
	defer exitRestoreRegistry.mu.Unlock()
	delete(exitRestoreRegistry.altScreens, t)
}

// RestoreTerminalOnExit is Pi's process 'exit' handler: last-resort restore for exit paths that skip Stop, such as a direct
// os.Exit (#11170). It writes, synchronously and exactly once per started terminal, the mode resets (mouse tracking, focus
// reporting, Kitty keyboard protocol, bracketed paste, alternate screen) and puts the terminal back in cooked mode,
// which Node's libuv does at exit and Go does not. Idempotent, and a no-op after a normal Stop. Never panics.
func RestoreTerminalOnExit() {
	exitRestoreRegistry.mu.Lock()
	terminals := exitRestoreRegistry.terminals
	exitRestoreRegistry.terminals = nil
	altMouse, altActive := false, false
	for _, mouse := range exitRestoreRegistry.altScreens {
		altActive = true
		altMouse = altMouse || mouse
	}
	exitRestoreRegistry.altScreens = nil
	exitRestoreRegistry.mu.Unlock()
	for terminal, cooked := range terminals {
		func() {
			defer func() { _ = recover() }() // Never throw from an exit handler.
			before, after := exitRestorePrefix(terminal, altActive, altMouse), exitRestoreSuffix(altActive)
			terminal.restoreOnExit(before, after)
			if cooked != nil {
				cooked()
			}
		}()
	}
}

// exitRestorePrefix is the mode resets written before the terminal's own: TuiBase.getExitRestorePrefix, behind
// TuiAltScreen.getExitRestorePrefix's synchronized-output end, mouse disable and autowrap enable while the alternate screen is active.
func exitRestorePrefix(terminal *ProcessTerminal, altActive, altMouse bool) string {
	prefix := "\x1b[?25h"
	if terminal.colorSchemeReports.Load() {
		prefix = "\x1b[?2031l" + prefix
	}
	if !altActive {
		return prefix
	}
	mouse := ""
	if altMouse {
		mouse = altDisableMouse
	}
	return altEndSynchronizedOutput + mouse + altEnableAutowrap + prefix
}

// exitRestoreSuffix is the mode resets written after the terminal's own: TuiAltScreen.getExitRestoreSuffix. Kitty keyboard
// flags are per screen, so the terminal pops them before the alternate screen is left.
func exitRestoreSuffix(altActive bool) string {
	if !altActive {
		return ""
	}
	return altExitAltScreen + "\x1b[?25h"
}

// restoreOnExit is ProcessTerminal.restoreOnExit: it writes before, then this terminal's own mode resets, then after.
// The write is one synchronous write to the output. It skips the output lock when another goroutine holds it, because
// that goroutine may be the one stuck on a terminal that went away.
func (t *ProcessTerminal) restoreOnExit(before, after string) {
	sequence := before
	if t.progressActiveForExit() {
		sequence += terminalProgressClearSeq
	}
	sequence += "\x1b[?2004l"
	pushed := keyboardProtocolPushed.Swap(false)
	if pushed || kittyProtocolActive.Swap(false) {
		sequence += keyboardProtocolPop
	}
	if modifyOtherKeysActive.Swap(false) {
		sequence += modifyOtherKeysDisable
	}
	sequence += after
	if t.outMu.TryLock() {
		defer t.outMu.Unlock()
	}
	t.write(sequence) // The terminal may already be gone.
}

// progressActiveForExit reports whether the OSC 9;4 keepalive runs. When the progress lock is held the answer is unknown and
// the clear is written: it is harmless on a terminal that shows no progress.
func (t *ProcessTerminal) progressActiveForExit() bool {
	if !t.progressMu.TryLock() {
		return true
	}
	defer t.progressMu.Unlock()
	return t.progressTicker != nil
}
