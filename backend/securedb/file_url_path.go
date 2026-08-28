//go:build !windows

package securedb

func databaseFileURLPath(path string) string { return path }
