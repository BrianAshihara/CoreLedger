package reconcile

import (
	"github.com/google/uuid"

	"github.com/brianashihara/coreledger/internal/domain/ledger"
)

// ReportView é o formato JSON do relatório, igual no endpoint de admin e no
// CLI (SPEC 13). Fica aqui, e não em cada transporte, para os dois nunca
// divergirem.
type ReportView struct {
	Status              Status      `json:"status"`
	CheckedAccounts     int64       `json:"checked_accounts"`
	CheckedTransactions int64       `json:"checked_transactions"`
	Issues              []IssueView `json:"issues"`
}

// IssueView descreve um achado. Os campos de identificação só aparecem quando
// se aplicam à checagem. expected é o que o ledger diz que deveria existir;
// actual é o que está gravado.
type IssueView struct {
	Check         ledger.Check `json:"check"`
	AccountID     *string      `json:"account_id,omitempty"`
	TransactionID *string      `json:"transaction_id,omitempty"`
	Currency      string       `json:"currency,omitempty"`
	AccountSeq    *int64       `json:"account_seq,omitempty"`
	Expected      int64        `json:"expected"`
	Actual        int64        `json:"actual"`
	Detail        string       `json:"detail"`
}

func (r Report) View() ReportView {
	v := ReportView{
		Status:              r.Status,
		CheckedAccounts:     r.CheckedAccounts,
		CheckedTransactions: r.CheckedTransactions,
		Issues:              make([]IssueView, 0, len(r.Issues)),
	}
	for _, i := range r.Issues {
		iv := IssueView{
			Check:    i.Check,
			Currency: string(i.Currency),
			Expected: i.Expected,
			Actual:   i.Actual,
			Detail:   i.Detail,
		}
		if i.AccountID != uuid.Nil {
			s := i.AccountID.String()
			iv.AccountID = &s
		}
		if i.TransactionID != uuid.Nil {
			s := i.TransactionID.String()
			iv.TransactionID = &s
		}
		if i.AccountSeq > 0 {
			seq := i.AccountSeq
			iv.AccountSeq = &seq
		}
		v.Issues = append(v.Issues, iv)
	}
	return v
}
