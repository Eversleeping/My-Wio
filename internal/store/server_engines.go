package store

// These projections preserve the native drivers' column names and revision
// expressions. A CLI heartbeat row is not an applied credential binding.
const geminiEngineBinding = `(SELECT server_id,credential_profile_id AS profile_id,profile_revision,updated_at FROM server_engines WHERE engine='gemini' AND updated_at IS NOT NULL)`
const claudeEngineBinding = `(SELECT server_id,credential_profile_id AS profile_id,profile_revision,model,updated_at FROM server_engines WHERE engine='claude' AND updated_at IS NOT NULL)`
const claudeEngineCLI = `(SELECT server_id,cli_version AS version,ready FROM server_engines WHERE engine='claude')`

// The alias s is the owning server in native and unified session projections.
const codexEngineVersion = `COALESCE((SELECT cli_version FROM server_engines WHERE server_id=s.id AND engine='codex'),'')`
const codexEngineReady = `COALESCE((SELECT ready FROM server_engines WHERE server_id=s.id AND engine='codex'),0)`
const geminiEngineVersion = `COALESCE((SELECT cli_version FROM server_engines WHERE server_id=s.id AND engine='gemini'),'')`
const geminiEngineReady = `COALESCE((SELECT ready FROM server_engines WHERE server_id=s.id AND engine='gemini'),0)`
