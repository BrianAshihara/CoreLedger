package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"

	"github.com/brianashihara/coreledger/internal/domain/money"
)

const maxBodyBytes = 64 << 10 // 64 KB (SPEC 12.1)

var (
	errMalformedBody    = errors.New("malformed request body")
	errUnsupportedMedia = errors.New("unsupported media type")
)

// decodeJSON lê o corpo da requisição de forma estrita (SPEC 12.1):
// Content-Type application/json, no máximo 64 KB, nenhum campo desconhecido
// e exatamente um objeto JSON, sem nada depois dele.
//
// Campos desconhecidos são rejeitados também por segurança: um cliente não
// consegue, por exemplo, mandar "kind": "SYSTEM" e ter isso ignorado em
// silêncio por um decoder permissivo.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return errUnsupportedMedia
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("%w: %w", errMalformedBody, err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: unexpected data after the JSON object", errMalformedBody)
	}
	return nil
}

// parseAmount converte o campo amount, recebido cru, num inteiro (SPEC 5).
// Nunca passa por float64: só aceita um número JSON escrito como inteiro.
// "1050", 10.5, 1e3, 1050.0, null, ausente ou maior que um int64 são todos
// INVALID_AMOUNT. O intervalo (maior que zero, no máximo MaxAmount) é checado
// depois, pela aplicação.
func parseAmount(raw json.RawMessage) (int64, error) {
	s := string(raw)
	if s == "" || s == "null" || s[0] == '"' {
		return 0, money.ErrInvalidAmount
	}
	for i, c := range s {
		isDigit := c >= '0' && c <= '9'
		isLeadingMinus := i == 0 && c == '-'
		if !isDigit && !isLeadingMinus {
			return 0, money.ErrInvalidAmount // ponto, expoente ou qualquer outra coisa
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, money.ErrInvalidAmount
	}
	return n, nil
}
