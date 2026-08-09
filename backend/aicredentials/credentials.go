// Package aicredentials provides strict loading, validation, authentication,
// and rotation primitives for AI API credentials.
package aicredentials

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

const (
	// CurrentVersion is the only credential file schema version currently
	// accepted by this package.
	CurrentVersion = 1

	MaxCredentialLifetime = 90 * 24 * time.Hour
	MinAnalysisDays       = 1
	MaxAnalysisDays       = 366
	MinResults            = 1
	MaxResults            = 500

	ScopeTransactionsCreate   = "transactions:create"
	ScopeAnalysisSummary      = "analysis:summary"
	ScopeAnalysisTransactions = "analysis:transactions"
	ScopeAnalysisMemo         = "analysis:memo"
	ScopeConsoleRelay         = "console:relay"

	maxCredentialIDLength = 64
	maxAccountLength      = 256
)

var (
	// ErrAuthenticationFailed intentionally covers unknown, inactive, and
	// expired credentials so callers do not disclose credential state.
	ErrAuthenticationFailed = errors.New("AI API credential authentication failed")
)

var allowedScopes = map[string]struct{}{
	ScopeTransactionsCreate:   {},
	ScopeAnalysisSummary:      {},
	ScopeAnalysisTransactions: {},
	ScopeAnalysisMemo:         {},
	ScopeConsoleRelay:         {},
}

// File is the on-disk JSON credential document.
type File struct {
	Version     int          `json:"version"`
	Credentials []Credential `json:"credentials"`
}

// Credential is a single scoped AI API credential. TokenSHA256 contains only
// the lowercase hexadecimal SHA-256 digest; raw bearer tokens are never
// serialized by this package.
type Credential struct {
	ID                string    `json:"id"`
	TokenSHA256       string    `json:"token_sha256"`
	NotBefore         time.Time `json:"not_before"`
	ExpiresAt         time.Time `json:"expires_at"`
	Scopes            []string  `json:"scopes"`
	Accounts          []string  `json:"accounts"`
	MaxAnalysisDays   int       `json:"max_analysis_days"`
	MaxResults        int       `json:"max_results"`
	AnalysisStartDate string    `json:"analysis_start_date,omitempty"`
	AnalysisEndDate   string    `json:"analysis_end_date,omitempty"`
	AllowConsoleRelay bool      `json:"-"`

	tokenHash  [sha256.Size]byte
	scopeSet   map[string]struct{}
	accountSet map[string]struct{}
}

// HashToken returns the canonical hash stored in a credential file.
func HashToken(rawToken string) string {
	sum := sha256.Sum256([]byte(rawToken))
	return hex.EncodeToString(sum[:])
}

// Validate performs strict semantic validation and prepares immutable lookup
// data used after a File has been loaded into a Store.
func (f *File) Validate() error {
	if f == nil {
		return errors.New("credential file is nil")
	}
	if f.Version != CurrentVersion {
		return fmt.Errorf("unsupported credential file version %d", f.Version)
	}
	if f.Credentials == nil {
		return errors.New("credentials array is required")
	}

	seenIDs := make(map[string]struct{}, len(f.Credentials))
	seenHashes := make(map[string]struct{}, len(f.Credentials))
	for i := range f.Credentials {
		credential := &f.Credentials[i]
		if err := credential.validate(); err != nil {
			return fmt.Errorf("credential %d: %w", i+1, err)
		}
		if _, exists := seenIDs[credential.ID]; exists {
			return fmt.Errorf("credential %q: duplicate id", credential.ID)
		}
		seenIDs[credential.ID] = struct{}{}
		if _, exists := seenHashes[credential.TokenSHA256]; exists {
			return fmt.Errorf("credential %q: duplicate token hash", credential.ID)
		}
		seenHashes[credential.TokenSHA256] = struct{}{}
	}
	return nil
}

func (c *Credential) validate() error {
	if !validCredentialID(c.ID) {
		return fmt.Errorf("id must match [A-Za-z0-9][A-Za-z0-9._-]{0,63}")
	}

	if len(c.TokenSHA256) != sha256.Size*2 || strings.ToLower(c.TokenSHA256) != c.TokenSHA256 {
		return errors.New("token_sha256 must be a 64-character lowercase hexadecimal SHA-256 digest")
	}
	decodedHash, err := hex.DecodeString(c.TokenSHA256)
	if err != nil || len(decodedHash) != sha256.Size {
		return errors.New("token_sha256 must be a 64-character lowercase hexadecimal SHA-256 digest")
	}
	copy(c.tokenHash[:], decodedHash)

	if c.NotBefore.IsZero() {
		return errors.New("not_before is required")
	}
	if c.ExpiresAt.IsZero() {
		return errors.New("expires_at is required")
	}
	if !c.ExpiresAt.After(c.NotBefore) {
		return errors.New("expires_at must be after not_before")
	}
	if c.ExpiresAt.Sub(c.NotBefore) > MaxCredentialLifetime {
		return fmt.Errorf("credential lifetime must not exceed %s", MaxCredentialLifetime)
	}

	if len(c.Scopes) == 0 {
		return errors.New("at least one scope is required")
	}
	c.scopeSet = make(map[string]struct{}, len(c.Scopes))
	for _, scope := range c.Scopes {
		if _, allowed := allowedScopes[scope]; !allowed {
			return fmt.Errorf("unsupported scope %q", scope)
		}
		if _, duplicate := c.scopeSet[scope]; duplicate {
			return fmt.Errorf("duplicate scope %q", scope)
		}
		c.scopeSet[scope] = struct{}{}
	}
	c.AllowConsoleRelay = c.HasScope(ScopeConsoleRelay)
	if c.HasScope(ScopeAnalysisTransactions) && !c.HasScope(ScopeAnalysisSummary) {
		return errors.New("analysis:transactions requires analysis:summary")
	}
	if c.HasScope(ScopeAnalysisMemo) && !c.HasScope(ScopeAnalysisTransactions) {
		return errors.New("analysis:memo requires analysis:transactions")
	}

	if len(c.Accounts) == 0 {
		return errors.New("at least one account is required")
	}
	c.accountSet = make(map[string]struct{}, len(c.Accounts))
	for _, account := range c.Accounts {
		if !validAccount(account) {
			return fmt.Errorf("invalid account %q", account)
		}
		if _, duplicate := c.accountSet[account]; duplicate {
			return fmt.Errorf("duplicate account %q", account)
		}
		c.accountSet[account] = struct{}{}
	}

	if c.MaxAnalysisDays < MinAnalysisDays || c.MaxAnalysisDays > MaxAnalysisDays {
		return fmt.Errorf("max_analysis_days must be between %d and %d", MinAnalysisDays, MaxAnalysisDays)
	}
	if c.MaxResults < MinResults || c.MaxResults > MaxResults {
		return fmt.Errorf("max_results must be between %d and %d", MinResults, MaxResults)
	}
	hasAnalysisScope := c.HasScope(ScopeAnalysisSummary) || c.HasScope(ScopeAnalysisTransactions) || c.HasScope(ScopeAnalysisMemo)
	if (c.AnalysisStartDate == "") != (c.AnalysisEndDate == "") {
		return errors.New("analysis_start_date and analysis_end_date must be specified together")
	}
	if hasAnalysisScope && c.AnalysisStartDate == "" {
		return errors.New("analysis scopes require analysis_start_date and analysis_end_date")
	}
	if c.AnalysisStartDate != "" {
		start, err := time.Parse("2006-01-02", c.AnalysisStartDate)
		if err != nil || start.Format("2006-01-02") != c.AnalysisStartDate {
			return errors.New("analysis_start_date must use YYYY-MM-DD")
		}
		end, err := time.Parse("2006-01-02", c.AnalysisEndDate)
		if err != nil || end.Format("2006-01-02") != c.AnalysisEndDate {
			return errors.New("analysis_end_date must use YYYY-MM-DD")
		}
		if end.Before(start) {
			return errors.New("analysis_end_date must not be before analysis_start_date")
		}
	}
	return nil
}

func validCredentialID(id string) bool {
	if len(id) == 0 || len(id) > maxCredentialIDLength {
		return false
	}
	for i, r := range id {
		isAlphaNumeric := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
		if isAlphaNumeric {
			continue
		}
		if i > 0 && (r == '.' || r == '_' || r == '-') {
			continue
		}
		return false
	}
	return true
}

func validAccount(account string) bool {
	if account == "" || account == "*" || len(account) > maxAccountLength || strings.TrimSpace(account) != account {
		return false
	}
	for _, r := range account {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}

// HasScope reports whether this credential includes scope.
func (c *Credential) HasScope(scope string) bool {
	if c == nil {
		return false
	}
	if c.scopeSet != nil {
		_, ok := c.scopeSet[scope]
		return ok
	}
	for _, candidate := range c.Scopes {
		if candidate == scope {
			return true
		}
	}
	return false
}

// AllowsAccount reports whether account is explicitly allowed. Wildcards are
// deliberately unsupported so newly created accounts never become accessible
// to an existing credential.
func (c *Credential) AllowsAccount(account string) bool {
	if c == nil || account == "" {
		return false
	}
	if c.accountSet != nil {
		_, ok := c.accountSet[account]
		return ok
	}
	for _, candidate := range c.Accounts {
		if candidate == account {
			return true
		}
	}
	return false
}

func (c Credential) clone() Credential {
	cloned := c
	cloned.Scopes = append([]string(nil), c.Scopes...)
	cloned.Accounts = append([]string(nil), c.Accounts...)
	cloned.scopeSet = make(map[string]struct{}, len(c.scopeSet))
	for scope := range c.scopeSet {
		cloned.scopeSet[scope] = struct{}{}
	}
	cloned.accountSet = make(map[string]struct{}, len(c.accountSet))
	for account := range c.accountSet {
		cloned.accountSet[account] = struct{}{}
	}
	return cloned
}

func tokenHashMatches(tokenHash [sha256.Size]byte, credential *Credential) int {
	return subtle.ConstantTimeCompare(tokenHash[:], credential.tokenHash[:])
}
