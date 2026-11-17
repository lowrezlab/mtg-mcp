package main

// Test fixture values shared across this package's tests.
const (
	testEnvLogFile         = "/data/env.log"
	testForkLogFile        = "/tmp/fork.log"
	testFlagLogFile        = "/tmp/flag.log"
	testLogFileFlag        = "--log-file"
	testForkLogFileEnv     = "LOG_FILE"
	testUpstreamLogFileEnv = "MTG_MCP_LOG_FILE"
)
