package mermaid

import (
	"testing"
)

// A diagram wider than the area used to be replaced by its source. Narrowing the
// node labels draws it instead, and the label width is chosen by measuring real
// layouts rather than from a fixed table, so the result lands as close under the
// target as the diagram allows.
const fitFixture = `flowchart LR
  subgraph NB["Notebook"]
    direction TB
    NBCFG["the config file has many short words in it"]
    NBENV["the env file also has short words in it"]
  end
  subgraph PG["App"]
    direction TB
    PGAPP["the cartridge file lists the tools it may use"]
    PGGW["the model code is about one seven five lines"]
  end
  NBCFG -. "one two three" .-> PGAPP
  NBENV -. "four five six" .-> PGGW
  PGAPP ==> PGGW`

func TestRenderWithinNarrowsUntilTheDiagramFits(t *testing.T) {
	natural, ok := Render(fitFixture)
	if !ok {
		t.Fatal("the fixture must render at its natural width")
	}

	fitted := 0
	for _, target := range []int{natural.Width - 10, natural.Width - 30, natural.Width - 50} {
		art, ok := RenderWithin(fitFixture, target)
		if !ok {
			t.Errorf("target %d: refused to render", target)
			continue
		}
		if art.splitWord {
			t.Errorf("target %d: sliced a word to reach %d columns", target, art.Width)
		}
		if art.Width <= target {
			fitted++
			continue
		}
		// Not fitting is allowed: boxes, arrows and the diagram's own longest
		// word set a floor. It must still have narrowed as far as that floor.
		if art.Width >= natural.Width {
			t.Errorf("target %d: returned the natural %d columns without narrowing", target, natural.Width)
		}
	}
	if fitted == 0 {
		t.Error("no target was met, so this fixture proves nothing about narrowing")
	}
}

// The legibility contract, stated once: narrowing never introduces a word break
// the natural layout did not already have, at any target, for any diagram.
// Narrowing past a diagram's longest word produces a column of fragments harder
// to read than the source it replaces, so the floor is the diagram's own
// vocabulary rather than a constant someone picked.
//
// A word longer than the natural wrap width is sliced by grok-mermaid itself.
// That is upstream behavior and is preserved; the contract is that fitting adds
// none of its own.
func TestRenderWithinNeverReturnsSlicedWords(t *testing.T) {
	sources := []string{
		fitFixture,
		"flowchart LR\n  A[\"antidisestablishmentarianism\"] --> B[\"short\"]",
		"flowchart TB\n  A[\"one\"] --> B[\"two\"] --> C[\"three\"] --> D[\"four\"]",
		"flowchart LR\n  subgraph S[\"frame\"]\n    X[\"internationalization\"]\n  end\n  X --> Y[\"y\"]",
	}
	for _, src := range sources {
		natural, ok := Render(src)
		if !ok {
			t.Fatalf("fixture must render: %q", src)
		}
		for target := 1; target <= natural.Width; target++ {
			art, ok := RenderWithin(src, target)
			if !ok {
				t.Errorf("target %d refused: %q", target, src)
				break
			}
			if art.splitWord && !natural.splitWord {
				t.Errorf("target %d sliced a word the natural layout kept whole (%d columns): %q",
					target, art.Width, src)
				break
			}
		}
	}
}
