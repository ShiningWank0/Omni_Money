package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"omni_money/backend/aicredentials"
)

type stringList []string

func (values *stringList) String() string { return strings.Join(*values, ",") }
func (values *stringList) Set(value string) error {
	*values = append(*values, value)
	return nil
}

type int64List []int64

func (values *int64List) String() string {
	parts := make([]string, len(*values))
	for i, value := range *values {
		parts[i] = strconv.FormatInt(value, 10)
	}
	return strings.Join(parts, ",")
}
func (values *int64List) Set(value string) error {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fmt.Errorf("tag id must be an integer: %w", err)
	}
	*values = append(*values, parsed)
	return nil
}

type commandEnvironment struct {
	stdout io.Writer
	stderr io.Writer
	now    func() time.Time
	random io.Reader
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

	var err error
	switch args[0] {
	case "issue":
		err = runIssue(args[1:], environment)
	case "rotate":
		err = runRotate(args[1:], environment)
	case "revoke":
		err = runRevoke(args[1:], environment)
	case "list":
		err = runList(args[1:], environment)
	default:
		printUsage(environment.stderr)
		return 2
	}
	if err != nil {
		fmt.Fprintln(environment.stderr, err)
		return 1
	}
	return 0
}

func printUsage(writer io.Writer) {
	fmt.Fprintln(writer, "usage: ai-credential <issue|rotate|revoke|list> [flags]")
}

func runIssue(args []string, environment commandEnvironment) error {
	flags := flag.NewFlagSet("issue", flag.ContinueOnError)
	flags.SetOutput(environment.stderr)
	path := flags.String("file", "", "credential JSON file")
	id := flags.String("id", "", "unique credential id")
	notBeforeValue := flags.String("not-before", "", "RFC3339 activation time (default: now)")
	expiresAtValue := flags.String("expires-at", "", "required RFC3339 expiration time")
	maxAnalysisDays := flags.Int("max-analysis-days", 30, "maximum analysis period")
	maxResults := flags.Int("max-results", 100, "maximum analysis result count")
	analysisStartDate := flags.String("analysis-start-date", "", "earliest analysis date, YYYY-MM-DD")
	analysisEndDate := flags.String("analysis-end-date", "", "latest analysis date, YYYY-MM-DD")
	var scopes stringList
	var accounts stringList
	var allowedTagIDs int64List
	flags.Var(&scopes, "scope", "allowed scope (repeatable)")
	flags.Var(&accounts, "account", "explicitly allowed account (repeatable; wildcard is forbidden)")
	flags.Var(&allowedTagIDs, "tag-id", "explicitly allowed tag id (repeatable; default: none)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *path == "" || *id == "" {
		return errors.New("--file and --id are required")
	}

	notBefore, expiresAt, err := parseValidity(*notBeforeValue, *expiresAtValue, environment.now())
	if err != nil {
		return err
	}
	document, err := loadOrCreate(*path)
	if err != nil {
		return err
	}
	if findCredential(document, *id) >= 0 {
		return fmt.Errorf("credential %q already exists", *id)
	}
	rawToken, err := generateToken(environment.random)
	if err != nil {
		return err
	}
	document.Credentials = append(document.Credentials, aicredentials.Credential{
		ID:                *id,
		TokenSHA256:       aicredentials.HashToken(rawToken),
		NotBefore:         notBefore,
		ExpiresAt:         expiresAt,
		Scopes:            append([]string(nil), scopes...),
		Accounts:          append([]string(nil), accounts...),
		AllowedTagIDs:     append([]int64(nil), allowedTagIDs...),
		MaxAnalysisDays:   *maxAnalysisDays,
		MaxResults:        *maxResults,
		AnalysisStartDate: *analysisStartDate,
		AnalysisEndDate:   *analysisEndDate,
	})
	if err := aicredentials.WriteFileAtomic(*path, document); err != nil {
		return err
	}
	writeCredentialAudit(environment, "issue", *id)
	_, err = fmt.Fprintln(environment.stdout, rawToken)
	return err
}

func runRotate(args []string, environment commandEnvironment) error {
	flags := flag.NewFlagSet("rotate", flag.ContinueOnError)
	flags.SetOutput(environment.stderr)
	path := flags.String("file", "", "credential JSON file")
	id := flags.String("id", "", "credential id")
	notBeforeValue := flags.String("not-before", "", "RFC3339 activation time (default: now)")
	expiresAtValue := flags.String("expires-at", "", "required RFC3339 expiration time")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *path == "" || *id == "" {
		return errors.New("--file and --id are required")
	}
	notBefore, expiresAt, err := parseValidity(*notBeforeValue, *expiresAtValue, environment.now())
	if err != nil {
		return err
	}
	document, err := aicredentials.LoadFile(*path)
	if err != nil {
		return err
	}
	index := findCredential(document, *id)
	if index < 0 {
		return fmt.Errorf("credential %q does not exist", *id)
	}
	rawToken, err := generateToken(environment.random)
	if err != nil {
		return err
	}
	document.Credentials[index].TokenSHA256 = aicredentials.HashToken(rawToken)
	document.Credentials[index].NotBefore = notBefore
	document.Credentials[index].ExpiresAt = expiresAt
	if err := aicredentials.WriteFileAtomic(*path, document); err != nil {
		return err
	}
	writeCredentialAudit(environment, "rotate", *id)
	_, err = fmt.Fprintln(environment.stdout, rawToken)
	return err
}

func runRevoke(args []string, environment commandEnvironment) error {
	flags := flag.NewFlagSet("revoke", flag.ContinueOnError)
	flags.SetOutput(environment.stderr)
	path := flags.String("file", "", "credential JSON file")
	id := flags.String("id", "", "credential id")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *path == "" || *id == "" {
		return errors.New("--file and --id are required")
	}
	document, err := aicredentials.LoadFile(*path)
	if err != nil {
		return err
	}
	index := findCredential(document, *id)
	if index < 0 {
		return fmt.Errorf("credential %q does not exist", *id)
	}
	document.Credentials = append(document.Credentials[:index], document.Credentials[index+1:]...)
	if err := aicredentials.WriteFileAtomic(*path, document); err != nil {
		return err
	}
	writeCredentialAudit(environment, "revoke", *id)
	return nil
}

func runList(args []string, environment commandEnvironment) error {
	flags := flag.NewFlagSet("list", flag.ContinueOnError)
	flags.SetOutput(environment.stderr)
	path := flags.String("file", "", "credential JSON file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *path == "" {
		return errors.New("--file is required")
	}
	document, err := aicredentials.LoadFile(*path)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(environment.stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(document)
}

func loadOrCreate(path string) (*aicredentials.File, error) {
	document, err := aicredentials.LoadFile(path)
	if err == nil {
		return document, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return &aicredentials.File{Version: aicredentials.CurrentVersion, Credentials: []aicredentials.Credential{}}, nil
}

func parseValidity(notBeforeValue, expiresAtValue string, now time.Time) (time.Time, time.Time, error) {
	if expiresAtValue == "" {
		return time.Time{}, time.Time{}, errors.New("--expires-at is required")
	}
	notBefore := now.UTC().Truncate(time.Second)
	var err error
	if notBeforeValue != "" {
		notBefore, err = time.Parse(time.RFC3339, notBeforeValue)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid --not-before: %w", err)
		}
	}
	expiresAt, err := time.Parse(time.RFC3339, expiresAtValue)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid --expires-at: %w", err)
	}
	return notBefore, expiresAt, nil
}

func generateToken(reader io.Reader) (string, error) {
	buffer := make([]byte, 32)
	if _, err := io.ReadFull(reader, buffer); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func findCredential(document *aicredentials.File, id string) int {
	for i := range document.Credentials {
		if document.Credentials[i].ID == id {
			return i
		}
	}
	return -1
}

func writeCredentialAudit(environment commandEnvironment, action, id string) {
	now := time.Now
	if environment.now != nil {
		now = environment.now
	}
	record := struct {
		Timestamp    string `json:"timestamp"`
		Action       string `json:"action"`
		CredentialID string `json:"credential_id"`
	}{
		Timestamp:    now().UTC().Format(time.RFC3339Nano),
		Action:       action,
		CredentialID: id,
	}
	if encoded, err := json.Marshal(record); err == nil {
		fmt.Fprintf(environment.stderr, "AI_CREDENTIAL_AUDIT %s\n", encoded)
	}
}
