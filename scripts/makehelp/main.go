// Command makehelp imprime os targets documentados de um Makefile (linhas no
// formato `target: ## descrição`). Existe para que o `make help` funcione
// igual no sh e no cmd.exe, sem grep nem awk (SPEC 15.3).
package main

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
)

var target = regexp.MustCompile(`^([a-zA-Z0-9_-]+):[^#]*## (.+)$`)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: makehelp <Makefile>")
		os.Exit(2)
	}
	f, err := os.Open(os.Args[1]) //nolint:gosec // ferramenta de dev; o caminho vem do próprio Makefile
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()

	s := bufio.NewScanner(f)
	for s.Scan() {
		if m := target.FindStringSubmatch(s.Text()); m != nil {
			fmt.Printf("  %-18s %s\n", m[1], m[2])
		}
	}
	if err := s.Err(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
