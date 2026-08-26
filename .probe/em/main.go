package main

import (
	"embed"
	"fmt"
	"io/fs"
)

//go:embed tree
var plain embed.FS

//go:embed all:tree
var all embed.FS

func list(f embed.FS, label string) {
	fmt.Println(label)
	_ = fs.WalkDir(f, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			fmt.Println("   ", p)
		}
		return nil
	})
}

func main() { list(plain, "//go:embed tree"); list(all, "//go:embed all:tree") }
