package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

// requireAdmin protege as rotas de admin com um Bearer token (SPEC 12.1).
//
// A comparação é feita sobre o SHA-256 dos dois lados, em tempo constante:
// comparar as strings direto vazaria pelo tempo de resposta quantos
// caracteres do início estão certos, e comparar só em tempo constante ainda
// vazaria o tamanho do token. O token nunca é logado.
func requireAdmin(token string) func(http.Handler) http.Handler {
	want := sha256.Sum256([]byte(token))
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got, ok := bearerToken(r.Header.Get("Authorization"))
			gotSum := sha256.Sum256([]byte(got))
			if !ok || subtle.ConstantTimeCompare(gotSum[:], want[:]) != 1 {
				w.Header().Set("WWW-Authenticate", `Bearer realm="coreledger-admin"`)
				writeError(w, r, errUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// bearerToken extrai o token de "Bearer <token>". O nome do esquema não
// diferencia maiúsculas de minúsculas (RFC 6750).
func bearerToken(header string) (string, bool) {
	const prefix = "bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	token := header[len(prefix):]
	return token, token != ""
}
