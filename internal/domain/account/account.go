// Package account define a conta e suas regras (SPEC 7.1): quem pode ser dono,
// quais status existem e quais transições entre eles são permitidas. É Go
// puro: não conhece banco nem HTTP.
package account

import (
	"errors"
	"fmt"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/brianashihara/coreledger/internal/domain/money"
)

type Kind string

const (
	// KindUser é uma conta de um dono externo. O saldo nunca fica negativo.
	KindUser Kind = "USER"
	// KindSystem é uma conta da plataforma, como a WORLD. Pode ficar negativa
	// e é invisível para os endpoints públicos.
	KindSystem Kind = "SYSTEM"
)

type Status string

const (
	StatusActive    Status = "ACTIVE"
	StatusSuspended Status = "SUSPENDED"
	StatusClosed    Status = "CLOSED"
)

type Account struct {
	ID           uuid.UUID
	Kind         Kind
	OwnerID      string // vazio em contas SYSTEM
	SystemCode   string // vazio em contas USER
	Currency     money.Currency
	Status       Status
	Balance      int64 // saldo materializado, em unidades mínimas
	LastEntrySeq int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

var (
	ErrNotFound                = errors.New("account not found")
	ErrInvalidOwnerID          = errors.New("invalid owner_id")
	ErrInvalidStatus           = errors.New("invalid account status")
	ErrInvalidStatusTransition = errors.New("invalid account status transition")
	ErrHasBalance              = errors.New("account has a non-zero balance")
	// ErrNotActive: só contas ACTIVE podem ser debitadas ou creditadas (SPEC 7.1).
	ErrNotActive = errors.New("account is not active")
)

const maxOwnerIDLength = 128

// ValidateOwnerID aplica as regras do SPEC 7.1: de 1 a 128 caracteres, UTF-8
// válido, sem caracteres de controle e sem espaço no início ou no fim. O banco
// tem um CHECK equivalente como segunda linha de defesa.
func ValidateOwnerID(s string) error {
	if !utf8.ValidString(s) {
		return fmt.Errorf("%w: not valid UTF-8", ErrInvalidOwnerID)
	}
	n := utf8.RuneCountInString(s)
	if n < 1 || n > maxOwnerIDLength {
		return fmt.Errorf("%w: must have between 1 and %d characters", ErrInvalidOwnerID, maxOwnerIDLength)
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: control characters are not allowed", ErrInvalidOwnerID)
		}
	}
	first, _ := utf8.DecodeRuneInString(s)
	last, _ := utf8.DecodeLastRuneInString(s)
	if unicode.IsSpace(first) || unicode.IsSpace(last) {
		return fmt.Errorf("%w: leading or trailing whitespace is not allowed", ErrInvalidOwnerID)
	}
	return nil
}

// ParseStatus aceita só os valores exatos do spec, sem normalizar caixa.
func ParseStatus(s string) (Status, error) {
	switch st := Status(s); st {
	case StatusActive, StatusSuspended, StatusClosed:
		return st, nil
	default:
		return "", ErrInvalidStatus
	}
}

// CheckTransition decide se uma conta pode ir de from para to (SPEC 7.1):
//
//	ACTIVE    <-> SUSPENDED
//	ACTIVE     -> CLOSED   (só com saldo zero)
//	SUSPENDED  -> CLOSED   (só com saldo zero)
//	CLOSED é terminal.
//
// Repetir o status atual é sempre permitido e não tem efeito. A transição é
// checada antes do saldo, então uma transição inválida nunca vira
// ErrHasBalance.
func CheckTransition(from, to Status, balance int64) error {
	if from == to {
		return nil
	}
	allowed := false
	switch from {
	case StatusActive:
		allowed = to == StatusSuspended || to == StatusClosed
	case StatusSuspended:
		allowed = to == StatusActive || to == StatusClosed
	case StatusClosed:
		allowed = false
	}
	if !allowed {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidStatusTransition, from, to)
	}
	if to == StatusClosed && balance != 0 {
		return ErrHasBalance
	}
	return nil
}
