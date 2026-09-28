package codingagent

// slash_bug.go implements /bug: consent, an optional model-written summary, then a local zip archive. WOPR is
// export-only and links to the WOPR issue form instead of uploading (D62).

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	bugReportDisclaimer     = "WOPR writes this report as a zip archive in the current directory and never uploads it. It includes your wopr version, operating system, the current model and provider configuration (without API keys), settings, and provider error diagnostics from this session. You decide whether to attach it to a WOPR issue on GitHub."
	bugReportTranscriptNote = "The transcript contains your messages, model output, tool calls and their results, including file contents and command output read during this session."
	bugReportCancelled      = "Bug report cancelled"
	bugReportExport         = "Export as Zip"
)

var errNoBugReportSummarizer = errors.New("the current session cannot write a summary")

// BugReportSessionEntryData is the custom entry /bug records in the session.
// Delivery is always "zip".
type BugReportSessionEntryData struct {
	ID              string  `json:"id"`
	CreatedAt       string  `json:"createdAt"`
	Hint            *string `json:"hint"`
	SessionIncluded bool    `json:"sessionIncluded"`
	SummaryIncluded bool    `json:"summaryIncluded"`
	Delivery        string  `json:"delivery"`
	Path            string  `json:"path,omitempty"`
}

type bugReportChoices struct {
	hint           string
	includeSession bool
	includeSummary bool
}

// bugCommand runs /bug. The argument prefills the description.
func (m *InteractiveMode) bugCommand(args string) error {
	choices, ok := m.promptBugReportChoices(args)
	if !ok {
		m.showStatus(bugReportCancelled)
		return nil
	}
	var summary *string
	if choices.includeSummary {
		text, aborted, err := m.summarizeForBugReport(m.bugReportModelName(), choices.hint)
		if aborted {
			m.showStatus(bugReportCancelled)
			return nil
		}
		if err != nil {
			return fmt.Errorf("Failed to write bug report summary: %w", err)
		}
		summary = &text
	}
	bundle, err := m.buildBugReportBundle(choices, summary)
	if err != nil {
		return fmt.Errorf("Failed to build bug report: %w", err)
	}
	return m.exportBugReport(bundle)
}

func (m *InteractiveMode) bugReportModelName() string {
	if m.opts.Model != nil && m.opts.Model.DisplayName != "" {
		return m.opts.Model.DisplayName
	}
	return "the current model"
}

// promptBugReportChoices asks for the description, transcript consent, and
// summary consent, then confirms the export. There is no upload choice.
func (m *InteractiveMode) promptBugReportChoices(args string) (bugReportChoices, bool) {
	hint, ok := m.editInSlot("Report a bug", bugReportDisclaimer+"\n\nWhat went wrong? (optional)", strings.TrimSpace(args))
	if !ok {
		return bugReportChoices{}, false
	}
	transcript, ok := m.selectInSlot("Include the session transcript?", []string{"Yes, include the transcript", "No"}, bugReportTranscriptNote)
	if !ok {
		return bugReportChoices{}, false
	}
	choices := bugReportChoices{hint: strings.TrimSpace(hint), includeSession: transcript == 0}
	modelName := m.bugReportModelName()
	if !choices.includeSession {
		provider := cmp.Or(m.bugReportProviderName(), "your provider")
		summary, ok := m.selectInSlot(
			"Attach a summary written by "+modelName+" instead?",
			[]string{"Yes, generate a summary", "No"},
			"The transcript is sent to "+provider+" with your credentials and tokens. Only the generated summary is attached; the transcript stays on your machine.",
		)
		if !ok {
			return bugReportChoices{}, false
		}
		choices.includeSummary = summary == 0
	}
	transcriptState := "not included"
	if choices.includeSession {
		transcriptState = "included"
	}
	summaryState := "none"
	if choices.includeSummary {
		summaryState = "written by " + modelName
	}
	// Export is the only delivery; WOPR never uploads.
	delivery, ok := m.selectInSlot("Bug report", []string{bugReportExport, "Cancel"}, fmt.Sprintf(
		"Description: %s\nTranscript: %s\nSummary: %s\n\nExport writes a zip archive to the current directory. Nothing is uploaded; you can attach the archive to a WOPR issue.",
		cmp.Or(choices.hint, "none"), transcriptState, summaryState,
	))
	if !ok || delivery != 0 {
		return bugReportChoices{}, false
	}
	return choices, true
}

func (m *InteractiveMode) buildBugReportBundle(choices bugReportChoices, summary *string) (BugReportBundle, error) {
	inputs, err := m.bugReportInputs()
	if err != nil {
		return BugReportBundle{}, err
	}
	if session := m.currentSession(); session != nil {
		inputs.SessionID = session.ID()
		inputs.CWD = session.CWD()
		inputs.Header = session.Header()
		inputs.Entries = session.Entries()
		inputs.Branch = BugReportBranch(session)
	}
	now := time.Now()
	metadata, err := CollectBugReportMetadata(inputs, BugReportOptions{Hint: choices.hint, IncludeSession: choices.includeSession}, summary != nil, now)
	if err != nil {
		return BugReportBundle{}, err
	}
	bundle := BugReportBundle{
		Metadata:    metadata,
		Diagnostics: CollectBugReportDiagnostics(inputs.SessionID, inputs.Entries, ReadCrashLog(CrashLogPath(m.opts.AgentDir))),
		Summary:     summary,
	}
	if choices.includeSession {
		// Attach the wopr.share entry (system prompt and tools) to the transcript.
		jsonl, err := SerializeSessionBranch(inputs.Header, inputs.Branch, now, shareTrailingEntries(m.shareState()))
		if err != nil {
			return BugReportBundle{}, err
		}
		bundle.SessionJSONL = &jsonl
	}
	return bundle, nil
}

// exportBugReport writes the archive to the working directory, records the
// report in the session, and prints the WOPR issue link.
func (m *InteractiveMode) exportBugReport(bundle BugReportBundle) error {
	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("Failed to write bug report: %w", err)
	}
	archiveName := BugReportArchiveFileName(bundle.Metadata.ID)
	archivePath := bugReportArchivePath(dir, bundle.Metadata.ID)
	if err := WriteBugReportArchive(bundle, archivePath); err != nil {
		return fmt.Errorf("Failed to write bug report: %w", err)
	}
	if err := m.recordBugReport(bundle, archivePath); err != nil {
		return fmt.Errorf("Bug report exported to %s, but recording it in the session failed: %w", archivePath, err)
	}
	// Clear the crash log once a report carries it.
	if len(bundle.Diagnostics.Crashes) > 0 {
		ClearCrashLog(CrashLogPath(m.opts.AgentDir))
	}
	m.showStatus(fmt.Sprintf("Bug report exported to: %s\nReport ID: %s", archivePath, bundle.Metadata.ID))
	// Link to the WOPR issue form; the user attaches the archive.
	m.appendMarkdown(fmt.Sprintf("To report it, open a WOPR issue and attach `%s`:\n\n%s", archiveName, BugReportIssueLink(bundle.Metadata, archiveName)))
	return nil
}

func (m *InteractiveMode) recordBugReport(bundle BugReportBundle, archivePath string) error {
	session := m.currentSession()
	if session == nil {
		return nil
	}
	_, err := appendOnLeaf(session, "custom", func(base SessionEntryBase) CustomEntry {
		base.Timestamp = isoTimestamp(time.Now())
		return CustomEntry{
			SessionEntryBase: base,
			CustomType:       BugReportCustomEntryType,
			Data: BugReportSessionEntryData{
				ID:              bundle.Metadata.ID,
				CreatedAt:       bundle.Metadata.CreatedAt,
				Hint:            bundle.Metadata.Hint,
				SessionIncluded: bundle.Metadata.Session.Included,
				SummaryIncluded: bundle.Metadata.Session.SummaryIncluded,
				Delivery:        "zip",
				Path:            archivePath,
			},
		}
	})
	return err
}
