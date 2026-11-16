package claudeadapter

import (
	"encoding/json"
	"os"
)

func recordTestMCPDelivery() {
	path := os.Getenv("CLAUDE_TEST_MCP_RECORD")
	if path == "" {
		return
	}
	for i, argument := range os.Args {
		if argument != "--mcp-config" || i+1 == len(os.Args) {
			continue
		}
		source := os.Args[i+1]
		raw := []byte(source)
		if !json.Valid(raw) {
			var err error
			raw, err = os.ReadFile(source)
			if err != nil {
				os.Exit(2)
			}
		}
		data, err := json.Marshal(map[string]any{"source": source, "args": os.Args[1:], "configuration": json.RawMessage(raw)})
		if err != nil || os.WriteFile(path, data, 0600) != nil {
			os.Exit(2)
		}
		return
	}
	os.Exit(2)
}
