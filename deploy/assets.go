package deploy

import _ "embed"

//go:embed agent.service
var AgentService []byte

//go:embed prerequisite.service
var PrerequisiteService []byte
