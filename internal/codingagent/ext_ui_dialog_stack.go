package codingagent

import (
	"slices"

	"github.com/MichaelKinsy/PiG/tui"
)

// The extension dialog stack of upstream's InteractiveMode (packages/coding-agent/src/modes/interactive/interactive-mode.ts:
// pushExtensionDialog, removeExtensionDialog, showTopExtensionDialog and the dialog part of resetExtensionUI).
//
// Dialogs in the editor slot (select, confirm, input, editor and an editor-slot custom component) form a stack on the owner
// loop: a new dialog hides the visible one, and closing a dialog shows the previous one again. Every dialog stays answerable,
// so no dialog call is left pending, and a dialog opened from inside another dialog works.

// pushExtensionDialog shows dialog in the editor slot above the dialogs already open.
func (m *InteractiveMode) pushExtensionDialog(dialog *extensionDialog) {
	m.disposeActiveSelector()
	m.extensionDialogs = append(m.extensionDialogs, dialog)
	m.showTopExtensionDialog()
}

// removeExtensionDialog removes dialog from the stack. When it was the visible one, the previous dialog or the editor shows.
func (m *InteractiveMode) removeExtensionDialog(dialog *extensionDialog) {
	index := slices.Index(m.extensionDialogs, dialog)
	if index < 0 {
		return
	}
	m.extensionDialogs = slices.Delete(m.extensionDialogs, index, index+1)
	if index == len(m.extensionDialogs) {
		m.showTopExtensionDialog()
	}
}

// finishExtensionDialog stops dialog's countdown and removes it from the stack, as Pi's selector and input finish does.
func (m *InteractiveMode) finishExtensionDialog(dialog *extensionDialog) {
	if dialog.dispose != nil {
		dialog.dispose()
	}
	m.removeExtensionDialog(dialog)
}

// showTopExtensionDialog puts the visible dialog, or the editor when none is open, in the editor slot with focus, and reports
// the dialog's blocked status. A custom overlay whose focus a dialog took gets it back when the dialog goes (tui.ts:616-627
// resumes the overlay once the dialog that blocked its restore is unmounted).
func (m *InteractiveMode) showTopExtensionDialog() {
	var component tui.Component = m.editor
	var blocked *BlockedStatus
	top := m.topExtensionDialog()
	if top != nil {
		component, blocked = top.component, top.blocked
	}
	m.editorContainer.SetChildren(component)
	m.tuiInst.SetFocus(component)
	m.programStatusReporter().SetBlocked(extensionDialogStatusSource, blocked)
	if shown := m.shownExtensionDialog(); shown != nil {
		m.setExtensionDialogViewMode(true, m.extensionDialogLines(shown.component))
	} else {
		m.setExtensionDialogViewMode(false, 0)
	}
	m.tuiInst.RequestRender()
}

// topExtensionDialog is the visible entry of the stack, nil when the editor shows.
func (m *InteractiveMode) topExtensionDialog() *extensionDialog {
	if len(m.extensionDialogs) == 0 {
		return nil
	}
	return m.extensionDialogs[len(m.extensionDialogs)-1]
}

// shownExtensionDialog is the visible select, confirm, input or editor dialog, which takes the terminal's keys. It is nil
// when the editor, or a custom component that reads its keys through focus, shows.
func (m *InteractiveMode) shownExtensionDialog() *extensionDialog {
	if top := m.topExtensionDialog(); top != nil && top.handle != nil {
		return top
	}
	return nil
}

// resetExtensionDialogs disposes every open dialog and shows the editor. The calls behind the dialogs end with their own
// contexts.
func (m *InteractiveMode) resetExtensionDialogs() {
	if len(m.extensionDialogs) == 0 {
		return
	}
	for _, dialog := range m.extensionDialogs {
		if dialog.dispose != nil {
			dialog.dispose()
		}
	}
	m.extensionDialogs = nil
	m.showTopExtensionDialog()
}
