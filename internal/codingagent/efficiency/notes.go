package efficiency

// Harness notes are the short messages wopr adds to a run on its own: the
// stall nudge, the final check, and deadline notes. The model reads the
// full text; the transcript shows one muted line.
const (
	StallNudgeMessageType = "stall_nudge"
	FinalCheckMessageType = "final_check"
	DeadlineMessageType   = "deadline_note"
)

var noteLabels = map[string]string{
	StallNudgeMessageType: "Nudged the model to change approach",
	FinalCheckMessageType: "Final check: verifying each requirement before finishing",
	DeadlineMessageType:   "Time note",
}

// NoteLabel returns the transcript line for a harness note's custom type.
func NoteLabel(customType string) (string, bool) {
	label, ok := noteLabels[customType]
	return label, ok
}
