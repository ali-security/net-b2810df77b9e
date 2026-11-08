// Command create_mod packages a directory into a Go module zip that is
// byte-identical to what the module proxy serves, using the canonical
// golang.org/x/mod/zip implementation.
//
// Usage: create_mod <module-path> <version> <dir> <out.zip>
package main

import (
	"log"
	"os"

	"golang.org/x/mod/module"
	modzip "golang.org/x/mod/zip"
)

func main() {
	if len(os.Args) != 5 {
		log.Fatalf("usage: %s <module-path> <version> <dir> <out.zip>", os.Args[0])
	}

	mv := module.Version{Path: os.Args[1], Version: os.Args[2]}

	f, err := os.Create(os.Args[4])
	if err != nil {
		log.Fatal(err)
	}
	if err := modzip.CreateFromDir(f, mv, os.Args[3]); err != nil {
		log.Fatal(err)
	}
	if err := f.Close(); err != nil {
		log.Fatal(err)
	}
}
