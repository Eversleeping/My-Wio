package protocol

const (
	MaxArtifactFiles = 64
	MaxArtifactBytes = 1 << 20
)

type TaskArtifactCapture struct {
	Workspace string   `json:"workspace"`
	Paths     []string `json:"paths"`
}

type ArtifactFile struct {
	Path       string `json:"path"`
	BeforeHash string `json:"before_hash,omitempty"`
	BeforeMode string `json:"before_mode,omitempty"`
	AfterHash  string `json:"after_hash,omitempty"`
	AfterMode  string `json:"after_mode,omitempty"`
	Content    []byte `json:"content,omitempty"`
}

type ArtifactManifest struct {
	BaseSHA string         `json:"base_sha"`
	Files   []ArtifactFile `json:"files"`
}

type TaskArtifactHandoff struct {
	Workspace string            `json:"workspace"`
	Action    string            `json:"action"`
	Digest    string            `json:"digest"`
	Manifest  ArtifactManifest  `json:"manifest"`
	Previous  *ArtifactManifest `json:"previous,omitempty"`
}

type TaskArtifactReceipt struct {
	Action string `json:"action"`
	Digest string `json:"digest"`
}
