// Command ledger é o binário único do projeto.
//
//	ledger serve                 sobe a API HTTP
//	ledger migrate up            aplica todas as migrations pendentes
//	ledger migrate down N        reverte exatamente N migrations
//	ledger migrate down --all    reverte todas as migrations (apaga todos os dados)
//	ledger migrate version       mostra a versão de migration aplicada
//	ledger reconcile             confere o ledger; sai com código 1 se houver inconsistência
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

const usage = `usage:
  ledger serve
  ledger migrate up
  ledger migrate down N
  ledger migrate down --all
  ledger migrate version
  ledger reconcile
`

// errUsage marca erros causados por argumentos errados (exit code 2).
var errUsage = errors.New("usage error")

func main() {
	// os.Interrupt é o Ctrl+C em qualquer plataforma; SIGTERM é o que os
	// gerenciadores de serviço enviam no Linux e no macOS (nunca chega no Windows).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr)
	stop()

	switch {
	case err == nil:
	case errors.Is(err, errInconsistent):
		os.Exit(1) // o relatório já foi impresso
	case errors.Is(err, errUsage):
		fmt.Fprintf(os.Stderr, "error: %v\n\n%s", err, usage)
		os.Exit(2)
	default:
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("%w: missing command", errUsage)
	}
	switch args[0] {
	case "serve":
		if len(args) > 1 {
			return fmt.Errorf("%w: serve takes no arguments", errUsage)
		}
		return serve(ctx, getenv, stderr)
	case "migrate":
		return migrateCmd(args[1:], getenv, stdout, stderr)
	case "reconcile":
		if len(args) > 1 {
			return fmt.Errorf("%w: reconcile takes no arguments", errUsage)
		}
		return reconcileCmd(ctx, getenv, stdout, stderr)
	case "help", "-h", "--help":
		_, err := io.WriteString(stdout, usage)
		return err
	default:
		return fmt.Errorf("%w: unknown command %q", errUsage, args[0])
	}
}
