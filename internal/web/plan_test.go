package web

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNewPlanFormatsBothFigures pins the two numbers a Lithuanian buyer reads:
// the quote (excluding VAT) and the invoice total. The 50 € / 21% case is the
// live configuration, so a change to the formatting shows up here first.
func TestNewPlanFormatsBothFigures(t *testing.T) {
	p := NewPlan(50, 21)

	require.Equal(t, "50", p.PriceExVAT)
	require.Equal(t, "60,50", p.PriceIncVAT)
	require.Equal(t, "21%", p.VATLabel)
	require.True(t, p.HasVAT())
	// The Offer in the page's structured data emits the bare number, so it must
	// survive as an int rather than only as display text.
	require.Equal(t, 50, p.PriceEUR)
}

// TestNewPlanCentsDoNotDrift covers prices whose VAT lands on a fractional cent
// boundary — the case float arithmetic gets subtly wrong and integer cents get
// exactly right.
func TestNewPlanCentsDoNotDrift(t *testing.T) {
	cases := map[string]struct {
		price, vat int
		want       string
	}{
		"round total":     {price: 100, vat: 21, want: "121,00"},
		"single cent":     {price: 1, vat: 21, want: "1,21"},
		"nine percent":    {price: 35, vat: 9, want: "38,15"},
		"five percent":    {price: 49, vat: 5, want: "51,45"},
		"awkward decimal": {price: 17, vat: 21, want: "20,57"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, c.want, NewPlan(c.price, c.vat).PriceIncVAT)
		})
	}
}

// TestNewPlanWithoutVAT: an operator who is not VAT registered configures 0%,
// and the pages must then say nothing about VAT at all rather than print a
// meaningless "+ 0%".
func TestNewPlanWithoutVAT(t *testing.T) {
	p := NewPlan(50, 0)

	require.False(t, p.HasVAT())
	require.Equal(t, "50", p.PriceExVAT)
	require.Equal(t, "50,00", p.PriceIncVAT)
}
