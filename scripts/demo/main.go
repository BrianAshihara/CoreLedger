// Command demo conta a história do CoreLedger contra uma API em execução:
// abre contas, movimenta dinheiro, repete requisições, dispara concorrência e
// reconcilia. Mostra cada requisição e resposta HTTP crua e confere o
// resultado de cada passo; sai com código 1 se alguma conferência falhar.
//
// Uso: `make run` em um terminal e `make demo` em outro.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
)

func main() {
	noColor := flag.Bool("no-color", false, "disable ANSI colors")
	flag.Parse()

	cfg := config{
		BaseURL:    apiURL(os.Getenv),
		AdminToken: os.Getenv("ADMIN_TOKEN"),
		Color:      !*noColor && os.Getenv("NO_COLOR") == "" && isTerminal(os.Stdout),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := run(ctx, cfg, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "demo:", err)
		os.Exit(1)
	}
}

// apiURL usa DEMO_API_URL quando definido; senão deriva o endereço do
// HTTP_ADDR que a própria API usa (o Makefile carrega os dois do .env).
func apiURL(getenv func(string) string) string {
	if u := getenv("DEMO_API_URL"); u != "" {
		return strings.TrimSuffix(u, "/")
	}
	addr := getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://localhost:8080"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// isTerminal evita códigos de cor quando a saída vai para um arquivo ou pipe.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

var errChecksFailed = errors.New("some checks failed")
