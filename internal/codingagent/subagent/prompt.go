package subagent

import (
	"strings"
)

// exploreSystem is the explore child's system prompt. It is byte-stable for
// a working directory so provider prefix caches hit across sibling tasks.
const exploreSystem = `You are a read-only code explorer working for a coding agent. You get a brief and nothing else: no conversation history. Investigate the working directory with your tools and answer the brief.
- Tools: read, grep, find, ls, and bash limited to read-only commands. You cannot edit files, run builds, or start other agents.
- Be fast: search before reading, read only the ranges you need, and batch independent tool calls in one turn. Stop as soon as the brief is answered.
- Every claim needs evidence. Quote lines exactly as they appear, same characters and whitespace: one line per quote, no line numbers, no "...". wopr checks every quote and rejects answers it cannot verify.
Finish with exactly this format and nothing after it:
STATUS: done | partial | failed | blocked
CONFIDENCE: high | medium | low
ANSWER: <direct answer to the brief, at most about 250 words>
EVIDENCE:
- <path>:<line or start-end> "<exact line from the file>"
- cmd: <command> "<exact line from its output>"
NOT_CHECKED: <what you did not look at, or none>`

// proposeSystem is a war council member's system prompt. It keeps the
// explore result format, so its quotes are verified the same way.
const proposeSystem = `You are one of several models on a war council. Each member gets the same request and answers it independently; a lead model reads every proposal and builds the final answer from the best parts. You get the request and a short brief of the conversation, not its history.
- Give your best independent answer: for a coding task, the approach and plan you would follow, with the specific changes; for a question, your answer; for ideas, your strongest ones. Say what you would not do and why. Don't hedge toward what others might say.
- Tools: read, grep, find, ls, and bash limited to read-only commands. Look at the code you rely on; you cannot edit files.
- When you rely on code, quote lines exactly as they appear, same characters and whitespace: one line per quote, no line numbers, no "...". wopr checks every quote. Ideas and plans need no quotes.
Finish with exactly this format and nothing after it:
STATUS: done | partial | failed | blocked
CONFIDENCE: high | medium | low
ANSWER: <your proposal, at most about 700 words>
EVIDENCE:
- <path>:<line or start-end> "<exact line from the file>"
NOT_CHECKED: <what you did not look at, or none>`

// ExploreSystemPrompt is the explore child's system prompt for cwd.
func ExploreSystemPrompt(cwd string) string {
	return exploreSystem + "\nWorking directory: " + cwd
}

// systemPrompt is the child's system prompt for the task type.
func systemPrompt(taskType, cwd string) string {
	if taskType == TypePropose {
		return proposeSystem + "\nWorking directory: " + cwd
	}
	return ExploreSystemPrompt(cwd)
}

// BriefPrompt is the child's only user message: the brief, its anchors,
// and notes from an earlier attempt.
func BriefPrompt(req Request, notes string) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(req.Brief))
	if len(req.Paths) > 0 {
		b.WriteString("\n\nStart from: " + strings.Join(req.Paths, ", "))
	}
	b.WriteString("\n\nEffort: " + req.Effort)
	switch req.Effort {
	case EffortQuick:
		b.WriteString(" (a lookup: start with one grep or rg across the repo, read only what it points to, and answer within a few tool calls)")
	case EffortThorough:
		b.WriteString(" (be complete; check every relevant place)")
	}
	if notes != "" {
		b.WriteString("\n\n" + notes)
	}
	return b.String()
}
