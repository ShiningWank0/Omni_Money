package main

import (
	"fmt"
	"os"

	"omni_money/backend/publicassets"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: verify-public-assets PUBLIC_ROOT MANIFEST")
		os.Exit(2)
	}
	if err := publicassets.Verify(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, "public asset verification failed:", err)
		os.Exit(1)
	}
	fmt.Println("public asset inventory verified")
}
