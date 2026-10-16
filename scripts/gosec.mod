// Keep scanner dependencies separate from the application's dependency graph.
module omni_money/securitytools

go 1.26.0

require (
	github.com/securego/gosec/v2 v2.29.0
	// v0.50.0 reads the export data v5 produced by Go 1.27.2.
	golang.org/x/tools v0.50.0
)
