package geminiadapter

import (
	_ "embed"
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
	loaderPath := filepath.ToSlash(path)
	if !strings.HasPrefix(loaderPath, "/") {
		loaderPath = "/" + loaderPath
	}
	loaderURL := (&url.URL{Scheme: "file", Path: loaderPath}).String()
	result := make([]string, 0, len(environment)+1)
	options := ""
	for _, entry := range environment {
		if value, ok := strings.CutPrefix(entry, "NODE_OPTIONS="); ok {
			options = value
		} else {
			result = append(result, entry)
		}
	}
	// Node 20+ supports loader hooks; URL encoding also handles temporary
	// directories containing spaces on Windows. Preserve the account's flags.
	result = append(result, "NODE_OPTIONS="+options+" --experimental-loader="+loaderURL)
	return result, cleanup, nil
}
