package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"omni_money/backend/audithmac"
)

type commandEnvironment struct {
	stdout io.Writer
	stderr io.Writer
	now    func() time.Time
	random io.Reader
}

type statusOutput struct {
	CurrentKeyID    string `json:"current_key_id"`
	PreviousKeyID   string `json:"previous_key_id,omitempty"`
	PreviousExpires string `json:"previous_emit_until,omitempty"`
}

func main() {
	environment := commandEnvironment{
		stdout: os.Stdout,
		stderr: os.Stderr,
		now:    time.Now,
		random: rand.Reader,
	}
	os.Exit(run(os.Args[1:], environment))
}

func run(args []string, environment commandEnvironment) int {
	if len(args) == 0 {
		printUsage(environment.stderr)
		return 2
	}
	if environment.now == nil {
		environment.now = time.Now
	}
	if environment.random == nil {
		environment.random = rand.Reader
	}

	var status audithmac.FileStatus
	var err error
	switch args[0] {
	case "init":
		status, err = runInit(args[1:], environment)
	case "rotate":
		status, err = runRotate(args[1:], environment)
	case "retire":
		status, err = runRetire(args[1:], environment)
	default:
		printUsage(environment.stderr)
		return 2
	}
	if err != nil {
		fmt.Fprintln(environment.stderr, err)
		return 1
	}
	if err := writeStatus(environment.stdout, status); err != nil {
		fmt.Fprintln(environment.stderr, err)
		return 1
	}
	return 0
}

func runInit(args []string, environment commandEnvironment) (audithmac.FileStatus, error) {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(environment.stderr)
	path := flags.String("file", "", "audit HMAC keyring file")
	if err := flags.Parse(args); err != nil {
		return audithmac.FileStatus{}, err
	}
	if flags.NArg() != 0 || *path == "" {
		return audithmac.FileStatus{}, errors.New("--file is required")
	}
	return audithmac.InitializeFile(*path, environment.random)
}

func runRotate(args []string, environment commandEnvironment) (audithmac.FileStatus, error) {
	flags := flag.NewFlagSet("rotate", flag.ContinueOnError)
	flags.SetOutput(environment.stderr)
	path := flags.String("file", "", "audit HMAC keyring file")
	overlap := flags.Duration("overlap", audithmac.DefaultOverlap, "current/previous overlap duration")
	if err := flags.Parse(args); err != nil {
		return audithmac.FileStatus{}, err
	}
	if flags.NArg() != 0 || *path == "" {
		return audithmac.FileStatus{}, errors.New("--file is required")
	}
	return audithmac.RotateFile(*path, environment.random, environment.now().UTC(), *overlap)
}

func runRetire(args []string, environment commandEnvironment) (audithmac.FileStatus, error) {
	flags := flag.NewFlagSet("retire", flag.ContinueOnError)
	flags.SetOutput(environment.stderr)
	path := flags.String("file", "", "audit HMAC keyring file")
	if err := flags.Parse(args); err != nil {
		return audithmac.FileStatus{}, err
	}
	if flags.NArg() != 0 || *path == "" {
		return audithmac.FileStatus{}, errors.New("--file is required")
	}
	return audithmac.RetirePreviousFile(*path, environment.now().UTC())
}

func writeStatus(writer io.Writer, status audithmac.FileStatus) error {
	output := statusOutput{CurrentKeyID: status.CurrentKeyID, PreviousKeyID: status.PreviousKeyID}
	if !status.PreviousExpires.IsZero() {
		output.PreviousExpires = status.PreviousExpires.UTC().Format(time.RFC3339)
	}
	encoder := json.NewEncoder(writer)
	return encoder.Encode(output)
}

func printUsage(writer io.Writer) {
	fmt.Fprintln(writer, "usage: ai-audit-key <init|rotate|retire> --file <path> [flags]")
}
