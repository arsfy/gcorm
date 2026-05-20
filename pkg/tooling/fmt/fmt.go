// Package gcofmt implements the `gco fmt` command for formatting schema files.
package gcofmt

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/arsfy/gco-orm/pkg/schema/formatter"
	"github.com/arsfy/gco-orm/pkg/schema/parser"
)

// Run executes the fmt command.
func Run(args []string) error {
	check := false
	var targets []string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--check":
			check = true
		default:
			targets = append(targets, args[i])
		}
	}

	if len(targets) == 0 {
		targets = []string{"."}
	}

	var files []string
	for _, t := range targets {
		info, err := os.Stat(t)
		if err != nil {
			return fmt.Errorf("stat %s: %w", t, err)
		}
		if info.IsDir() {
			entries, err := os.ReadDir(t)
			if err != nil {
				return err
			}
			for _, e := range entries {
				if !e.IsDir() && filepath.Ext(e.Name()) == ".gcorm" {
					files = append(files, filepath.Join(t, e.Name()))
				}
			}
		} else {
			files = append(files, t)
		}
	}

	if len(files) == 0 {
		return fmt.Errorf("no .gcorm files found")
	}

	unformatted := 0
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("read %s: %w", f, err)
		}

		doc, err := parser.Parse(f, data)
		if err != nil {
			return fmt.Errorf("parse %s: %w", f, err)
		}

		formatted := formatter.Format(doc)

		if check {
			if string(data) != formatted {
				fmt.Fprintf(os.Stderr, "%s: not formatted\n", f)
				unformatted++
			}
		} else {
			if err := os.WriteFile(f, []byte(formatted), 0o644); err != nil {
				return fmt.Errorf("write %s: %w", f, err)
			}
			fmt.Printf("Formatted %s\n", f)
		}
	}

	if check && unformatted > 0 {
		return fmt.Errorf("%d file(s) need formatting", unformatted)
	}

	return nil
}
