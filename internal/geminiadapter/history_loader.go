package geminiadapter

import (
	_ "embed"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

//go:embed history-loader.mjs
var historyLoader []byte

func acpHistoryEnvironment(environment []string) ([]string, func(), error) {
	directory, err := os.MkdirTemp("", "wio-gemini-history-*")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	path := filepath.Join(directory, "loader.mjs")
	if err := os.WriteFile(path, historyLoader, 0600); err != nil {
		cleanup()
		return nil, nil, err
	}
	bootstrapPath := filepath.ToSlash(filepath.Join(directory, "bootstrap.mjs"))
	if !strings.HasPrefix(bootstrapPath, "/") {
		bootstrapPath = "/" + bootstrapPath
	}
	bootstrapURL := (&url.URL{Scheme: "file", Path: bootstrapPath}).String()
	result := make([]string, 0, len(environment)+1)
	options := ""
	for _, entry := range environment {
		if value, ok := strings.CutPrefix(entry, "NODE_OPTIONS="); ok {
			options = value
		} else {
			result = append(result, entry)
		}
	}
	// Register from the main process, restoring NODE_OPTIONS before any tools
	// or MCP children start. In particular, long-lived project processes must
	// never inherit a reference to a loader we remove at the end of this turn.
	// Node 20.6+ supports register; managed Wio runtimes use Node 22.
	originalOptions, _ := json.Marshal(options)
	bootstrap := "import { register } from 'node:module';\nprocess.env.NODE_OPTIONS = " + string(originalOptions) + ";\nregister('./loader.mjs', import.meta.url);\n"
	if err := os.WriteFile(filepath.Join(directory, "bootstrap.mjs"), []byte(bootstrap), 0600); err != nil {
		cleanup()
		return nil, nil, err
	}
	result = append(result, "NODE_OPTIONS="+options+" --import="+bootstrapURL)
	return result, cleanup, nil
}
