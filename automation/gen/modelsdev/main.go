// Command modelsdev refreshes ai/modelsdev.json, the models.dev snapshot
// wopr embeds for first runs and offline use: it downloads models.dev's
// api.json and keeps the providers wopr has rules for and the fields it
// reads.
package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/alexrudloff/wopr/ai"
)

const source = "https://models.dev/api.json"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "modelsdev:", err)
		os.Exit(1)
	}
}

func run() error {
	client := &http.Client{Timeout: time.Minute}
	resp, err := client.Get(source)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", source, resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	trimmed, err := ai.TrimModelsDev(raw)
	if err != nil {
		return err
	}
	if err := os.WriteFile("ai/modelsdev.json", append(trimmed, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("ai/modelsdev.json: %d bytes\n", len(trimmed)+1)
	return nil
}
