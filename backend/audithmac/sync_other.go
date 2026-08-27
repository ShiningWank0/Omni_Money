//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package audithmac

func syncDirectory(_ string) error { return nil }
