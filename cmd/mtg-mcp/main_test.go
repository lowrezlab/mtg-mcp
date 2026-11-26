package main

import (
	"errors"
	"flag"
	"io"
	"testing"

	"github.com/nathanmartins/mtg-mcp/internal/logging"
)

func noEnv(string) (string, bool) { return "", false }

func TestParseOptions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		args    []string
		getenv  func(string) (string, bool)
		want    options
		wantErr bool
	}{
		{name: "defaults", args: nil, getenv: noEnv, want: options{logFile: logging.DefaultFilePath()}},
		{
			name: "double-dash version", args: []string{"--version"}, getenv: noEnv,
			want: options{showVersion: true, logFile: logging.DefaultFilePath()},
		},
		{
			name: "single-dash version", args: []string{"-version"}, getenv: noEnv,
			want: options{showVersion: true, logFile: logging.DefaultFilePath()},
		},
		{
			name: "flags combine in any order", args: []string{"--debug", "--version"}, getenv: noEnv,
			want: options{showVersion: true, debug: true, logFile: logging.DefaultFilePath()},
		},
		{
			name: "env sets the log file",
			args: nil,
			getenv: func(key string) (string, bool) {
				if key == logFileEnv {
					return testEnvLogFile, true
				}
				return "", false
			},
			want: options{logFile: testEnvLogFile},
		},
		{
			name: "flag beats env",
			args: []string{testLogFileFlag, testFlagLogFile},
			getenv: func(string) (string, bool) {
				return testEnvLogFile, true
			},
			want: options{logFile: testFlagLogFile},
		},
		{name: "unknown flag", args: []string{"--bogus"}, getenv: noEnv, wantErr: true},
		{name: "stray argument", args: []string{"serve"}, getenv: noEnv, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseOptions(tt.args, tt.getenv, io.Discard)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseOptions() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("parseOptions() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseOptionsHelp(t *testing.T) {
	t.Parallel()
	if _, err := parseOptions([]string{"--help"}, noEnv, io.Discard); !errors.Is(err, flag.ErrHelp) {
		t.Errorf("parseOptions(--help) error = %v, want flag.ErrHelp", err)
	}
}

func TestParseOptionsLogFilePrecedence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
		env  map[string]string
		want string
	}{
		{name: "absent variables keep the default", want: logging.DefaultFilePath()},
		{
			name: "fork variable sets the log file",
			env:  map[string]string{testForkLogFileEnv: testForkLogFile},
			want: testForkLogFile,
		},
		{
			name: "empty fork variable disables the log file",
			env:  map[string]string{testForkLogFileEnv: ""},
			want: "",
		},
		{
			name: "upstream variable sets the log file",
			env:  map[string]string{testUpstreamLogFileEnv: testEnvLogFile},
			want: testEnvLogFile,
		},
		{
			name: "empty upstream variable disables the log file",
			env:  map[string]string{testUpstreamLogFileEnv: ""},
			want: "",
		},
		{
			name: "fork variable takes precedence over upstream",
			env:  map[string]string{testForkLogFileEnv: testForkLogFile, testUpstreamLogFileEnv: testEnvLogFile},
			want: testForkLogFile,
		},
		{
			name: "empty fork variable takes precedence over upstream",
			env:  map[string]string{testForkLogFileEnv: "", testUpstreamLogFileEnv: testEnvLogFile},
			want: "",
		},
		{
			name: "flag takes precedence over both variables",
			args: []string{testLogFileFlag, testFlagLogFile},
			env:  map[string]string{testForkLogFileEnv: testForkLogFile, testUpstreamLogFileEnv: testEnvLogFile},
			want: testFlagLogFile,
		},
		{
			name: "flag takes precedence over empty fork variable",
			args: []string{testLogFileFlag, testFlagLogFile},
			env:  map[string]string{testForkLogFileEnv: ""},
			want: testFlagLogFile,
		},
		{
			name: "empty flag disables the log file",
			args: []string{"--log-file="},
			env:  map[string]string{testForkLogFileEnv: testForkLogFile, testUpstreamLogFileEnv: testEnvLogFile},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseOptions(tt.args, func(key string) (string, bool) {
				value, present := tt.env[key]
				return value, present
			}, io.Discard)
			if err != nil {
				t.Fatalf("parseOptions() error = %v", err)
			}
			if got.logFile != tt.want {
				t.Errorf("parseOptions().logFile = %q, want %q", got.logFile, tt.want)
			}
		})
	}
}
