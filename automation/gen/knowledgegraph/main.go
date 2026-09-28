// Command knowledgegraph renders docs/knowledge-graph/wopr.graph.json into its
// published forms. Behind `make knowledge-graph`.
//
//	go run ./automation/gen/knowledgegraph [-check]
//
// Outputs: the docs page (docs/site/docs/knowledge-graph.md, which also ships
// in the binary), JSON-LD for tools
// (docs/knowledge-graph/wopr-knowledge-graph.jsonld), and Mermaid source
// (docs/knowledge-graph/wopr.mmd). -check fails when any output is stale and
// writes nothing.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/alexrudloff/wopr/automation/internal/repo"
)

type entity struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Kind    string `json:"kind"`
	Summary string `json:"summary"`
	Where   string `json:"where"`
	Inspect string `json:"inspect"`
	Doc     string `json:"doc"`
}

// relation is [source, relation, target, note].
type relation [4]string

type graph struct {
	Entities  []entity   `json:"entities"`
	Relations []relation `json:"relations"`
}

// Crow's-foot cardinality per relation; unlisted relations are one-to-many.
var cardinality = map[string]string{
	"implements": "||--||", "is_a": "}o--||", "owns": "||--||", "pins": "||--||", "selects": "}o--o{",
	"written_with": "}o--||", "realized_as": "}o--||", "realizes": "}o--||", "drives": "}o--||",
	"streams_through": "}o--||", "documents_difference_from": "}o--||",
}

// output is one generated file, in the order they are written and reported.
type output struct{ path, text string }

func main() {
	root, err := repo.Root()
	if err == nil {
		err = run(root, os.Args[1:], os.Stdout)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(root string, args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("knowledgegraph", flag.ContinueOnError)
	check := flags.Bool("check", false, "fail when any output is stale; write nothing")
	if err := flags.Parse(args); err != nil {
		return err
	}
	outputs, err := render(root)
	if err != nil {
		return err
	}
	if *check {
		var stale []string
		for _, o := range outputs {
			if current, err := readText(filepath.Join(root, o.path)); err != nil || current != o.text {
				stale = append(stale, o.path)
			}
		}
		if len(stale) > 0 {
			return errors.New("stale knowledge-graph outputs; run make knowledge-graph: " + strings.Join(stale, ", "))
		}
		return nil
	}
	written := make([]string, 0, len(outputs))
	for _, o := range outputs {
		if err := os.WriteFile(filepath.Join(root, o.path), []byte(o.text), 0o644); err != nil {
			return err
		}
		written = append(written, o.path)
	}
	_, err = fmt.Fprintln(stdout, "wrote", strings.Join(written, ", "))
	return err
}

// readText reads a file with universal newlines, as the outputs are compared.
func readText(path string) (string, error) {
	data, err := os.ReadFile(path)
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	return strings.ReplaceAll(text, "\r", "\n"), err
}

func render(root string) ([]output, error) {
	source, err := readText(filepath.Join(root, "docs/knowledge-graph/wopr.graph.json"))
	if err != nil {
		return nil, err
	}
	var g graph
	if err := json.Unmarshal([]byte(source), &g); err != nil {
		return nil, fmt.Errorf("docs/knowledge-graph/wopr.graph.json: %w", err)
	}
	entities := map[string]entity{}
	for _, e := range g.Entities {
		entities[e.ID] = e
	}
	for _, r := range g.Relations {
		_, okSource := entities[r[0]]
		_, okTarget := entities[r[2]]
		if !okSource || !okTarget {
			return nil, fmt.Errorf("relation references an unknown entity: %s -> %s", r[0], r[2])
		}
	}

	var mermaid strings.Builder
	mermaid.WriteString("erDiagram\n")
	for _, r := range g.Relations {
		shape, ok := cardinality[r[1]]
		if !ok {
			shape = "||--o{"
		}
		fmt.Fprintf(&mermaid, "    %s %s %s : \"%s\"\n", strings.ToUpper(r[0]), shape, strings.ToUpper(r[2]), strings.ReplaceAll(r[1], "_", " "))
	}

	site := "# Knowledge graph\n\nWOPR is built from a small set of entities. This page lists each entity, where it lives, the command that inspects it, and how the entities relate. " +
		"Use it to locate a concept before reading its page; each row links the page to read next. " +
		"The same graph is published as JSON-LD at `docs/knowledge-graph/wopr-knowledge-graph.jsonld` and as Mermaid source in `docs/knowledge-graph/wopr.mmd` in the repository, " +
		"and it ships inside every wopr binary as `knowledge-graph.md` in the local docs (`wopr docs show knowledge-graph`).\n\n" +
		tables(g, entities, func(d string) string { return "[" + d + "](" + d + ")" }) +
		"\nThe graph source is `docs/knowledge-graph/wopr.graph.json`. Run `make knowledge-graph` after editing it.\n"

	return []output{
		{"docs/site/docs/knowledge-graph.md", site},
		{"docs/knowledge-graph/wopr-knowledge-graph.jsonld", jsonLD(g)},
		{"docs/knowledge-graph/wopr.mmd", mermaid.String()},
	}, nil
}

func tables(g graph, entities map[string]entity, docLink func(string) string) string {
	var b strings.Builder
	b.WriteString("| Entity | What it is | Where it lives | Inspect with | Read |\n|---|---|---|---|---|\n")
	for _, e := range g.Entities {
		fmt.Fprintf(&b, "| %s | %s | `%s` | `%s` | %s |\n", e.Label, e.Summary, e.Where, e.Inspect, docLink(e.Doc))
	}
	b.WriteString("\n| From | Relation | To | Note |\n|---|---|---|---|\n")
	for _, r := range g.Relations {
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", entities[r[0]].Label, strings.ReplaceAll(r[1], "_", " "), entities[r[2]].Label, r[3])
	}
	return b.String()
}
