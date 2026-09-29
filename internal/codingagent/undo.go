package codingagent

// UndoChange is one file change /undo reverted.
type UndoChange struct {
	Path string
	// Tool is the tool that made the change: write, edit, or apply_patch.
	Tool string
	// Deleted reports that the change created the file, so undoing it
	// removed the file.
	Deleted bool
	// ChangedSince reports that the file changed after the tool's change
	// (a shell command or the user); Saved is where that version was kept.
	ChangedSince bool
	Saved        string
}
