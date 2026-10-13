// Package migrations embute as migrations SQL no binário, para que ele possa
// aplicá-las e para que o helper de teste monte o banco template a partir dos
// mesmos arquivos.
package migrations

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
)

//go:embed *.sql
var FS embed.FS

var fileName = regexp.MustCompile(`^(\d+)_[a-z0-9_]+\.(up|down)\.sql$`)

// Latest devolve a maior versão de migration embutida no binário.
func Latest() (uint, error) {
	names, err := sqlFiles()
	if err != nil {
		return 0, err
	}
	var latest uint
	for _, name := range names {
		m := fileName.FindStringSubmatch(name)
		if m == nil {
			return 0, fmt.Errorf("migration file %q does not match NNNNNN_name.(up|down).sql", name)
		}
		v, err := strconv.ParseUint(m[1], 10, 32)
		if err != nil {
			return 0, fmt.Errorf("migration file %q: %w", name, err)
		}
		latest = max(latest, uint(v))
	}
	if latest == 0 {
		return 0, fmt.Errorf("no migrations embedded")
	}
	return latest, nil
}

// Hash identifica o conteúdo exato das migrations embutidas: os 12 primeiros
// caracteres hex de um SHA-256 sobre o nome e o conteúdo de cada arquivo.
// Editar uma migration no lugar muda o hash, mesmo sem mudar a versão.
func Hash() (string, error) {
	names, err := sqlFiles()
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, name := range names {
		content, err := FS.ReadFile(name)
		if err != nil {
			return "", err
		}
		// Prefixa cada parte com o tamanho, para que divisões diferentes
		// entre arquivos não gerem o mesmo hash.
		_, _ = fmt.Fprintf(h, "%d:%s%d:", len(name), name, len(content))
		_, _ = h.Write(content)
	}
	return hex.EncodeToString(h.Sum(nil))[:12], nil
}

func sqlFiles() ([]string, error) {
	names, err := fs.Glob(FS, "*.sql")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}
