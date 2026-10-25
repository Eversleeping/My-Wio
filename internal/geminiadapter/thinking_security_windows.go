package geminiadapter

// Windows system settings use the existing administrator-managed ProgramData
// directory and its inherited ACLs, which the official CLI validates itself.
func validateThinkingDirectory(directory string) error { return nil }
