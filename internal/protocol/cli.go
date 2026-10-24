package protocol

type CLIInstallCommand struct {
	Kind    string `json:"kind"`
	Version string `json:"version"`
}
