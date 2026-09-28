package askuser

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func call(t *testing.T, tool *Tool, args map[string]any) (string, error) {
	t.Helper()
	raw, _ := json.Marshal(args)
	res, err := tool.Execute(context.Background(), "c", raw, nil)
	return res.Content, err
}

var question = map[string]any{"question": "Which database?", "options": []map[string]any{{"label": "Postgres", "description": "already in docker-compose"}, {"label": "SQLite"}}}

func TestAnswersReachTheModel(t *testing.T) {
	for _, tc := range []struct {
		answer Answer
		want   string
	}{
		{Answer{Choice: "Postgres"}, "User chose: Postgres"},
		{Answer{Other: "MySQL, we already pay for it"}, "User answered: MySQL, we already pay for it"},
		{Answer{Declined: true}, "User declined to answer."},
	} {
		var got Question
		tool := &Tool{Ask: func() Asker {
			return func(_ context.Context, q Question) (Answer, error) { got = q; return tc.answer, nil }
		}}
		text, err := call(t, tool, question)
		if err != nil || !strings.HasPrefix(text, tc.want) {
			t.Fatalf("%+v: %q %v", tc.answer, text, err)
		}
		if got.Question != "Which database?" || len(got.Options) != 2 || got.Options[0].Description != "already in docker-compose" {
			t.Fatalf("asker got %+v", got)
		}
	}
}

func TestNoUserMeansDecideYourself(t *testing.T) {
	text, err := call(t, &Tool{Ask: func() Asker { return nil }}, question)
	if err != nil || !strings.Contains(text, "Decide yourself") {
		t.Fatalf("%q %v", text, err)
	}
}

func TestQuestionsAreValidated(t *testing.T) {
	tool := &Tool{}
	for _, args := range []map[string]any{
		{"question": "", "options": []map[string]any{{"label": "a"}, {"label": "b"}}},
		{"question": "q", "options": []map[string]any{{"label": "a"}}},
		{"question": "q", "options": []map[string]any{{"label": "a"}, {"label": "A"}}},
		{"question": "q", "options": []map[string]any{{"label": "a"}, {"label": "b"}, {"label": "c"}, {"label": "d"}, {"label": "e"}, {"label": "f"}}},
	} {
		if _, err := call(t, tool, args); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
}
