# Temporary security backport

Upstream: picomatch 4.0.7, MIT, npm's published archive verified by SHA-512.
Tracking: https://github.com/ShiningWank0/Omni_Money/issues/348

This is a locally maintained security patch, not an official release. The
upstream name/version and original copyright/license are retained.

GHSA-fc5q-39xc-vjpf allows exponential backtracking when a single-branch
repeated extglob contains an unbounded wildcard. The change in `lib/parse.js`
removes that nested repetition. A simple slash-free glob containing a wildcard
is closed under concatenation: its positive closure is the same language, and
its Kleene closure additionally permits empty input. The patch reuses the
already parsed body so platform, dotfile and capture settings are retained.
Complex branches that cannot use this equivalence follow upstream's existing
literalization policy. Their generated regexp and capture layout may differ.
Previously unsafe complex patterns can therefore match their literal text
instead of acting as a glob. These boundaries are covered by security tests.

The original 1,983 upstream compatibility assertions are unchanged. Separate
security tests exercise the real installed dependency and time-limit attack
inputs. The source/file-set hashes and every declared importer's resolution
are checked. npm's omission of file dependencies is not treated as an audit:
the original upstream version is queried separately, and only the exact
recorded advisory revision is considered remediated by this verified patch.
New or changed advisories remain findings.

Revision 2 also addresses GitHub Advanced Security's CodeQL alert #8. The
backslash-removal regexp repeatedly searched unmatched bracket suffixes and
could take quadratic time. It is replaced by two linear passes that preserve
the original handling of bracket spans, escaped characters and line endings.
Exhaustive short-input comparisons and long adversarial-input tests cover the
replacement. No CodeQL rule or security finding is suppressed.

When an official stable fix is available, adopt it in a new PR, rerun the
security/compatibility tests, remove this directory, its local dependency and
override, the patch manifest and temporary verification code. Retain useful
ReDoS regression tests and normal vulnerability scanning. Issue #348 stays
open until that replacement is merged and validated.
