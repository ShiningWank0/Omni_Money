package securedb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"omni_money/backend/fileprivacy"
)

// CopyPlaintextToEncrypted creates destinationPath as a verified SQLCipher
// copy of sourcePath. The source tuple is copied byte-for-byte into a private
// workspace before SQLite opens that disposable copy for WAL or rollback
// journal recovery. The caller's source is never opened by SQLite, renamed,
// deleted, checkpointed, or otherwise modified. A same-directory staging file
// is published with an atomic, no-overwrite hard link only after all
// verification has succeeded, so destinationPath is never a partial database.
func CopyPlaintextToEncrypted(ctx context.Context, sourcePath, destinationPath string, opener *Opener) (retErr error) {
	if opener == nil || !opener.Encrypted() {
		return errors.New("encrypted opener is required")
	}
	if strings.TrimSpace(sourcePath) == "" {
		return errors.New("plaintext source path is required")
	}
	if strings.TrimSpace(destinationPath) == "" {
		return errors.New("encrypted destination path is required")
	}

	sourceIdentity, err := inspectMigrationSource(sourcePath)
	if err != nil {
		return err
	}
	if err := requirePlaintextHeader(sourcePath); err != nil {
		return err
	}
	if err := requireMigrationDestinationAbsent(destinationPath); err != nil {
		return err
	}
	destinationDir := filepath.Dir(destinationPath)
	if destinationDir == "" {
		destinationDir = "."
	}
	destinationDirInfo, err := inspectRealDirectory(destinationDir)
	if err != nil {
		return fmt.Errorf("inspect encrypted destination directory: %w", err)
	}

	stageDir, stageMarker, stageRoot, err := prepareMigrationStage(destinationDir, destinationPath)
	if err != nil {
		return err
	}
	stageRootClosed := false
	stageCleaned := false
	defer func() {
		if !stageRootClosed {
			_ = stageRoot.Close()
		}
		if !stageCleaned {
			cleanupErr := cleanupMigrationStage(stageDir, stageMarker, false)
			if cleanupErr != nil {
				retErr = errors.Join(retErr, cleanupErr)
			}
		}
	}()
	workingSourcePath := filepath.Join(stageDir, "plaintext.db")
	if err := copyMigrationSourceTuple(sourcePath, workingSourcePath, stageDir, stageRoot, sourceIdentity); err != nil {
		return err
	}
	if err := verifyMigrationSource(sourcePath, sourceIdentity); err != nil {
		return err
	}

	// The private copy is intentionally writable so SQLite, not ad-hoc file
	// manipulation, performs any required WAL or hot rollback-journal recovery.
	source, err := openPlain(workingSourcePath, Writable)
	if err != nil {
		return fmt.Errorf("open private plaintext working copy: %w", err)
	}
	source.SetMaxOpenConns(1)
	defer source.Close()
	sourceConn, err := source.Conn(ctx)
	if err != nil {
		return fmt.Errorf("open plaintext source connection: %w", err)
	}
	defer sourceConn.Close()

	// The private working copy has no other writers. Opening it through SQLite
	// performs journal recovery and leaves a stable logical source for export.
	if err := expectSQLiteIntegrity(ctx, sourceConn); err != nil {
		return fmt.Errorf("verify plaintext source: %w", err)
	}
	if err := expectForeignKeyIntegrity(ctx, sourceConn); err != nil {
		return fmt.Errorf("verify plaintext source: %w", err)
	}
	if err := requireSQLCipherRuntime(ctx, sourceConn); err != nil {
		return err
	}
	sourceFingerprint, err := logicalDatabaseFingerprint(ctx, sourceConn)
	if err != nil {
		return fmt.Errorf("fingerprint plaintext source: %w", err)
	}
	var userVersion, applicationID int64
	if err := sourceConn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&userVersion); err != nil {
		return fmt.Errorf("read plaintext source user_version: %w", err)
	}
	if err := sourceConn.QueryRowContext(ctx, "PRAGMA application_id").Scan(&applicationID); err != nil {
		return fmt.Errorf("read plaintext source application_id: %w", err)
	}

	stagePath := filepath.Join(stageDir, "encrypted.db")
	placeholder, err := fileprivacy.CreateExclusive(stageRoot, stageDir, "encrypted.db")
	if err != nil {
		return fmt.Errorf("create encrypted migration target: %w", err)
	}
	if err := fileprivacy.Harden(placeholder); err != nil {
		_ = placeholder.Close()
		return fmt.Errorf("secure encrypted migration target before use: %w", err)
	}
	if err := placeholder.Close(); err != nil {
		return fmt.Errorf("close encrypted migration target: %w", err)
	}
	key, err := opener.copyKey()
	if err != nil {
		return err
	}
	defer key.Destroy()
	if err := exportEncryptedConnection(ctx, sourceConn, stagePath, &key, userVersion, applicationID); err != nil {
		return fmt.Errorf("export plaintext source into SQLCipher: %w", err)
	}

	verified, err := opener.Open(ctx, stagePath, Snapshot)
	if err != nil {
		return fmt.Errorf("reopen encrypted migration target: %w", err)
	}
	if err := opener.CheckIntegrity(ctx, verified); err != nil {
		_ = verified.Close()
		return fmt.Errorf("verify encrypted migration target: %w", err)
	}
	if err := expectForeignKeyIntegrity(ctx, verified); err != nil {
		_ = verified.Close()
		return fmt.Errorf("verify encrypted migration target: %w", err)
	}
	targetFingerprint, err := logicalDatabaseFingerprint(ctx, verified)
	if err != nil {
		_ = verified.Close()
		return fmt.Errorf("fingerprint encrypted migration target: %w", err)
	}
	if err := verified.Close(); err != nil {
		return err
	}
	if sourceFingerprint != targetFingerprint {
		return errors.New("encrypted migration target differs logically from plaintext source")
	}
	if err := sourceConn.Close(); err != nil {
		return fmt.Errorf("close private plaintext working connection: %w", err)
	}
	if err := source.Close(); err != nil {
		return fmt.Errorf("close private plaintext working database: %w", err)
	}
	if err := RequireEncryptedHeader(stagePath); err != nil {
		return fmt.Errorf("verify encrypted migration target header: %w", err)
	}
	if err := os.Chmod(stagePath, 0600); err != nil {
		return fmt.Errorf("secure encrypted migration target: %w", err)
	}
	if err := syncFile(stagePath); err != nil {
		return fmt.Errorf("sync encrypted migration target: %w", err)
	}
	if err := verifyMigrationSource(sourcePath, sourceIdentity); err != nil {
		return err
	}
	currentDestinationDirInfo, err := inspectRealDirectory(destinationDir)
	if err != nil || !os.SameFile(destinationDirInfo, currentDestinationDirInfo) {
		return errors.New("encrypted destination directory changed during migration")
	}
	if err := requireMigrationDestinationAbsent(destinationPath); err != nil {
		return err
	}
	if err := stageRoot.Close(); err != nil {
		return fmt.Errorf("close private migration directory: %w", err)
	}
	stageRootClosed = true

	// os.Link is an atomic no-replace publication primitive on the same
	// filesystem. Unlike os.Rename, it cannot overwrite a destination created
	// between our initial existence check and publication.
	if err := os.Link(stagePath, destinationPath); err != nil {
		return fmt.Errorf("publish encrypted migration target: %w", err)
	}
	published := true
	defer func() {
		if retErr != nil && published {
			_ = os.Remove(destinationPath)
			_ = syncDirectory(destinationDir)
		}
	}()
	if err := syncDirectory(destinationDir); err != nil {
		return fmt.Errorf("sync encrypted migration destination directory: %w", err)
	}
	if err := cleanupMigrationStage(stageDir, stageMarker, false); err != nil {
		return err
	}
	stageCleaned = true
	if err := syncDirectory(destinationDir); err != nil {
		return fmt.Errorf("sync migration staging cleanup: %w", err)
	}
	published = false
	return nil
}

// VerifyPlaintextMatchesEncrypted proves that destinationPath is the complete
// encrypted logical equivalent of sourcePath without modifying either input.
// It is intended for crash recovery after a destination was atomically
// published but the caller's migration journal was not yet advanced.
func VerifyPlaintextMatchesEncrypted(ctx context.Context, sourcePath, destinationPath string, opener *Opener) (retErr error) {
	if opener == nil || !opener.Encrypted() {
		return errors.New("encrypted opener is required")
	}
	if strings.TrimSpace(sourcePath) == "" {
		return errors.New("plaintext source path is required")
	}
	if strings.TrimSpace(destinationPath) == "" {
		return errors.New("encrypted destination path is required")
	}
	sourceIdentity, err := inspectMigrationSource(sourcePath)
	if err != nil {
		return err
	}
	if err := requirePlaintextHeader(sourcePath); err != nil {
		return err
	}
	destinationIdentity, err := inspectMigrationTuple(destinationPath, "encrypted destination")
	if err != nil {
		return err
	}
	if err := RequireEncryptedHeader(destinationPath); err != nil {
		return fmt.Errorf("verify encrypted destination header: %w", err)
	}
	destinationDir := filepath.Dir(destinationPath)
	if destinationDir == "" {
		destinationDir = "."
	}
	destinationDirInfo, err := inspectRealDirectory(destinationDir)
	if err != nil {
		return fmt.Errorf("inspect encrypted destination directory: %w", err)
	}

	stageDir, stageMarker, stageRoot, err := prepareMigrationStage(destinationDir, destinationPath)
	if err != nil {
		return err
	}
	stageRootClosed := false
	defer func() {
		if !stageRootClosed {
			_ = stageRoot.Close()
		}
		cleanupErr := cleanupMigrationStage(stageDir, stageMarker, false)
		if cleanupErr != nil {
			retErr = errors.Join(retErr, cleanupErr)
		}
	}()
	workingSourcePath := filepath.Join(stageDir, "plaintext.db")
	if err := copyMigrationSourceTuple(sourcePath, workingSourcePath, stageDir, stageRoot, sourceIdentity); err != nil {
		return err
	}
	if err := verifyMigrationSource(sourcePath, sourceIdentity); err != nil {
		return err
	}

	source, err := openPlain(workingSourcePath, Writable)
	if err != nil {
		return fmt.Errorf("open private plaintext verification copy: %w", err)
	}
	source.SetMaxOpenConns(1)
	sourceConn, err := source.Conn(ctx)
	if err != nil {
		_ = source.Close()
		return fmt.Errorf("open private plaintext verification connection: %w", err)
	}
	if err := expectSQLiteIntegrity(ctx, sourceConn); err != nil {
		_ = sourceConn.Close()
		_ = source.Close()
		return fmt.Errorf("verify plaintext source: %w", err)
	}
	if err := expectForeignKeyIntegrity(ctx, sourceConn); err != nil {
		_ = sourceConn.Close()
		_ = source.Close()
		return fmt.Errorf("verify plaintext source: %w", err)
	}
	if err := requireSQLCipherRuntime(ctx, sourceConn); err != nil {
		_ = sourceConn.Close()
		_ = source.Close()
		return err
	}
	sourceFingerprint, err := logicalDatabaseFingerprint(ctx, sourceConn)
	if err != nil {
		_ = sourceConn.Close()
		_ = source.Close()
		return fmt.Errorf("fingerprint plaintext source: %w", err)
	}
	if err := sourceConn.Close(); err != nil {
		_ = source.Close()
		return err
	}
	if err := source.Close(); err != nil {
		return err
	}

	destination, err := opener.Open(ctx, destinationPath, ReadOnly)
	if err != nil {
		return fmt.Errorf("open encrypted destination read-only: %w", err)
	}
	destination.SetMaxOpenConns(1)
	if err := opener.CheckIntegrity(ctx, destination); err != nil {
		_ = destination.Close()
		return fmt.Errorf("verify encrypted destination: %w", err)
	}
	if err := expectForeignKeyIntegrity(ctx, destination); err != nil {
		_ = destination.Close()
		return fmt.Errorf("verify encrypted destination: %w", err)
	}
	destinationFingerprint, err := logicalDatabaseFingerprint(ctx, destination)
	if err != nil {
		_ = destination.Close()
		return fmt.Errorf("fingerprint encrypted destination: %w", err)
	}
	if err := destination.Close(); err != nil {
		return err
	}
	if sourceFingerprint != destinationFingerprint {
		return errors.New("encrypted destination differs logically from plaintext source")
	}
	if err := verifyMigrationSource(sourcePath, sourceIdentity); err != nil {
		return err
	}
	if err := verifyMigrationTuple(destinationPath, destinationIdentity, "encrypted destination"); err != nil {
		return err
	}
	currentDestinationDirInfo, err := inspectRealDirectory(destinationDir)
	if err != nil || !os.SameFile(destinationDirInfo, currentDestinationDirInfo) {
		return errors.New("encrypted destination directory changed during verification")
	}
	if err := RequireEncryptedHeader(destinationPath); err != nil {
		return fmt.Errorf("verify encrypted destination header: %w", err)
	}
	if err := stageRoot.Close(); err != nil {
		return fmt.Errorf("close private verification directory: %w", err)
	}
	stageRootClosed = true
	return nil
}

const migrationStageMarkerName = ".omni-securedb-copy-v1"

func prepareMigrationStage(destinationDir, destinationPath string) (string, []byte, *os.Root, error) {
	absoluteDestination, err := filepath.Abs(destinationPath)
	if err != nil {
		return "", nil, nil, fmt.Errorf("resolve encrypted migration destination: %w", err)
	}
	destinationDigest := sha256.Sum256([]byte(filepath.Clean(absoluteDestination)))
	stageDir := filepath.Join(destinationDir, ".omni-cipher-copy-"+hex.EncodeToString(destinationDigest[:12]))
	marker := []byte("omni-money securedb copy v1\n" + hex.EncodeToString(destinationDigest[:]) + "\n")
	if _, err := os.Lstat(stageDir); err == nil {
		if err := cleanupMigrationStage(stageDir, marker, true); err != nil {
			return "", nil, nil, fmt.Errorf("recover prior migration staging directory: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", nil, nil, fmt.Errorf("inspect migration staging directory: %w", err)
	}
	if err := createPrivateMigrationDirectory(stageDir); err != nil {
		return "", nil, nil, fmt.Errorf("create private migration directory: %w", err)
	}
	stageRoot, err := os.OpenRoot(stageDir)
	if err != nil {
		_ = os.Remove(stageDir)
		return "", nil, nil, fmt.Errorf("pin private migration directory: %w", err)
	}
	markerFile, err := fileprivacy.CreateExclusive(stageRoot, stageDir, migrationStageMarkerName)
	if err != nil {
		_ = stageRoot.Close()
		_ = os.Remove(stageDir)
		return "", nil, nil, fmt.Errorf("create migration staging marker: %w", err)
	}
	if err := fileprivacy.Harden(markerFile); err != nil {
		_ = markerFile.Close()
		_ = stageRoot.Close()
		_ = os.Remove(filepath.Join(stageDir, migrationStageMarkerName))
		_ = os.Remove(stageDir)
		return "", nil, nil, fmt.Errorf("secure migration staging marker: %w", err)
	}
	_, writeErr := markerFile.Write(marker)
	syncErr := markerFile.Sync()
	closeErr := markerFile.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		_ = stageRoot.Close()
		_ = os.Remove(filepath.Join(stageDir, migrationStageMarkerName))
		_ = os.Remove(stageDir)
		return "", nil, nil, fmt.Errorf("persist migration staging marker: %w", errors.Join(writeErr, syncErr, closeErr))
	}
	if err := syncDirectory(destinationDir); err != nil {
		_ = stageRoot.Close()
		_ = cleanupMigrationStage(stageDir, marker, true)
		return "", nil, nil, fmt.Errorf("sync migration staging directory: %w", err)
	}
	return stageDir, marker, stageRoot, nil
}

func cleanupMigrationStage(stageDir string, expectedMarker []byte, allowEmpty bool) error {
	info, err := os.Lstat(stageDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("migration staging path is not a real directory")
	}
	entries, err := os.ReadDir(stageDir)
	if err != nil {
		return err
	}
	if len(entries) == 0 && allowEmpty {
		return os.Remove(stageDir)
	}
	allowed := map[string]bool{
		migrationStageMarkerName: false,
		"plaintext.db":           false,
		"plaintext.db-wal":       false,
		"plaintext.db-shm":       false,
		"plaintext.db-journal":   false,
		"encrypted.db":           false,
		"encrypted.db-wal":       false,
		"encrypted.db-shm":       false,
		"encrypted.db-journal":   false,
	}
	for _, entry := range entries {
		if _, ok := allowed[entry.Name()]; !ok {
			return fmt.Errorf("migration staging directory contains unknown artifact %q", entry.Name())
		}
		entryInfo, err := os.Lstat(filepath.Join(stageDir, entry.Name()))
		if err != nil {
			return err
		}
		if entryInfo.Mode()&os.ModeSymlink != 0 || !entryInfo.Mode().IsRegular() {
			return fmt.Errorf("migration staging artifact %q is not a regular non-symlink file", entry.Name())
		}
		allowed[entry.Name()] = true
	}
	if !allowed[migrationStageMarkerName] {
		return errors.New("migration staging marker is missing")
	}
	marker, err := os.ReadFile(filepath.Join(stageDir, migrationStageMarkerName)) // #nosec G304 -- the exact regular marker path was checked above.
	if err != nil {
		return err
	}
	if string(marker) != string(expectedMarker) {
		// A crash can leave the newly-created marker empty or partially
		// written. It is safe to discard only when it is the stage's sole
		// artifact and is an exact prefix of the deterministic marker. Once a
		// plaintext or encrypted database exists, the complete marker remains
		// mandatory.
		if len(entries) == 1 && len(marker) < len(expectedMarker) && bytes.HasPrefix(expectedMarker, marker) {
			if err := os.Remove(filepath.Join(stageDir, migrationStageMarkerName)); err != nil {
				return err
			}
			return os.Remove(stageDir)
		}
		return errors.New("migration staging marker does not match the destination")
	}
	for _, name := range []string{
		"plaintext.db-wal", "plaintext.db-shm", "plaintext.db-journal", "plaintext.db",
		"encrypted.db-wal", "encrypted.db-shm", "encrypted.db-journal", "encrypted.db",
	} {
		if allowed[name] {
			if err := os.Remove(filepath.Join(stageDir, name)); err != nil {
				return err
			}
		}
	}
	if err := os.Remove(filepath.Join(stageDir, migrationStageMarkerName)); err != nil {
		return err
	}
	return os.Remove(stageDir)
}

func requirePlaintextHeader(path string) error {
	header, err := readHeader(path)
	if err != nil {
		return fmt.Errorf("read plaintext source header: %w", err)
	}
	if string(header) != sqlitePlaintextHeader {
		return errors.New("source is not a plaintext SQLite database")
	}
	return nil
}

func requireMigrationDestinationAbsent(path string) error {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		_, err := os.Lstat(path + suffix)
		switch {
		case err == nil:
			return fmt.Errorf("encrypted migration destination already exists: %w", os.ErrExist)
		case errors.Is(err, os.ErrNotExist):
			continue
		default:
			return fmt.Errorf("inspect encrypted migration destination: %w", err)
		}
	}
	return nil
}

type migrationSourceIdentity struct {
	files map[string]migrationSourceFile
}

type migrationSourceFile struct {
	info   os.FileInfo
	digest [sha256.Size]byte
}

func inspectMigrationSource(path string) (migrationSourceIdentity, error) {
	return inspectMigrationTuple(path, "plaintext source")
}

func inspectMigrationTuple(path, label string) (migrationSourceIdentity, error) {
	identity := migrationSourceIdentity{files: make(map[string]migrationSourceFile)}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		candidate := path + suffix
		info, err := os.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return migrationSourceIdentity{}, fmt.Errorf("inspect %s%s: %w", label, suffix, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return migrationSourceIdentity{}, fmt.Errorf("%s%s must be a regular non-symlink file", label, suffix)
		}
		digest, err := hashStableMigrationFile(candidate, info)
		if err != nil {
			return migrationSourceIdentity{}, fmt.Errorf("hash %s%s: %w", label, suffix, err)
		}
		identity.files[suffix] = migrationSourceFile{info: info, digest: digest}
	}
	if _, ok := identity.files[""]; !ok {
		return migrationSourceIdentity{}, fmt.Errorf("%s is missing", label)
	}
	return identity, nil
}

func verifyMigrationSource(path string, expected migrationSourceIdentity) error {
	return verifyMigrationTuple(path, expected, "plaintext source")
}

func verifyMigrationTuple(path string, expected migrationSourceIdentity, label string) error {
	current, err := inspectMigrationTuple(path, label)
	if err != nil {
		return fmt.Errorf("%s changed during migration: %w", label, err)
	}
	if len(current.files) != len(expected.files) {
		return fmt.Errorf("%s sidecar set changed during migration", label)
	}
	for suffix, expectedFile := range expected.files {
		currentFile, ok := current.files[suffix]
		if !ok || !sameMigrationFile(expectedFile, currentFile) {
			return fmt.Errorf("%s%s identity changed during migration", label, suffix)
		}
	}
	return nil
}

func sameMigrationFile(left, right migrationSourceFile) bool {
	return os.SameFile(left.info, right.info) &&
		left.info.Size() == right.info.Size() &&
		left.info.ModTime().Equal(right.info.ModTime()) &&
		left.digest == right.digest
}

func hashStableMigrationFile(path string, expected os.FileInfo) ([sha256.Size]byte, error) {
	file, err := os.Open(path) // #nosec G304 -- path is the validated configured migration source tuple.
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	if !os.SameFile(expected, before) || !before.Mode().IsRegular() {
		return [sha256.Size]byte{}, errors.New("source file changed before hashing")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return [sha256.Size]byte{}, err
	}
	after, err := file.Stat()
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return [sha256.Size]byte{}, errors.New("source file changed while hashing")
	}
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	return digest, nil
}

func copyMigrationSourceTuple(sourcePath, workingPath, stageDir string, stageRoot *os.Root, expected migrationSourceIdentity) error {
	for _, suffix := range []string{"", "-wal", "-journal"} {
		expectedFile, ok := expected.files[suffix]
		if !ok {
			continue
		}
		source, err := os.Open(sourcePath + suffix) // #nosec G304 -- source identity is checked against the initial inventory below.
		if err != nil {
			return fmt.Errorf("open plaintext source%s for private copy: %w", suffix, err)
		}
		openedInfo, err := source.Stat()
		if err != nil || !os.SameFile(expectedFile.info, openedInfo) {
			_ = source.Close()
			return fmt.Errorf("plaintext source%s changed before private copy", suffix)
		}
		name := filepath.Base(workingPath) + suffix
		destination, err := fileprivacy.CreateExclusive(stageRoot, stageDir, name)
		if err != nil {
			_ = source.Close()
			return fmt.Errorf("create private plaintext working copy%s: %w", suffix, err)
		}
		if err := fileprivacy.Harden(destination); err != nil {
			_ = source.Close()
			_ = destination.Close()
			return fmt.Errorf("secure private plaintext working copy%s: %w", suffix, err)
		}
		hash := sha256.New()
		_, copyErr := io.Copy(io.MultiWriter(destination, hash), source)
		sourceCloseErr := source.Close()
		syncErr := destination.Sync()
		destinationCloseErr := destination.Close()
		if copyErr != nil || sourceCloseErr != nil || syncErr != nil || destinationCloseErr != nil {
			return fmt.Errorf("copy plaintext source%s into private workspace: %w", suffix, errors.Join(copyErr, sourceCloseErr, syncErr, destinationCloseErr))
		}
		var copiedDigest [sha256.Size]byte
		copy(copiedDigest[:], hash.Sum(nil))
		if copiedDigest != expectedFile.digest {
			return fmt.Errorf("plaintext source%s changed during private copy", suffix)
		}
	}
	return nil
}

func inspectRealDirectory(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, errors.New("path must be a real directory, not a symlink")
	}
	return info, nil
}
