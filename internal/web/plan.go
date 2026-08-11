package web

import "fmt"

// Plan is the published commercial offer as the public pages print it. Like
// Contact, it is a small value type that carries already-formatted strings: the
// landing page, the article, llms.txt and the structured-data Offer must all
// quote the same figure, and the only way to guarantee that is to format it once
// here rather than four times at four call sites.
type Plan struct {
	// PriceEUR is the monthly price excluding VAT, in whole euros. It stays a
	// number because schema.org's Offer wants a bare "50", not a typeset "50 €".
	PriceEUR int
	// VATPercent is the Lithuanian PVM rate applied at invoicing.
	VATPercent int
	// PriceExVAT is the quoted figure, e.g. "50". Lithuanian B2B quotes exclude
	// VAT, so this is the number that carries the headline.
	PriceExVAT string
	// PriceIncVAT is the figure that actually lands on the invoice, e.g.
	// "60,50" — written with the decimal comma Lithuanian uses. It is published
	// alongside the quote so a reader who cannot reclaim VAT does not have to do
	// the multiplication to find out what they will be charged.
	PriceIncVAT string
	// VATLabel is the rate as printed, e.g. "21%".
	VATLabel string
}

// NewPlan builds the published offer from the configured price and VAT rate.
//
// The VAT-inclusive figure is derived in integer cents rather than with float
// arithmetic: money that is off by a rounding error is worse than money that is
// merely wrong, because nobody notices it until an invoice disagrees with the
// page. price * (100 + vat) is exactly the price in cents, with no division and
// so no rounding at all.
func NewPlan(priceEUR, vatPercent int) Plan {
	cents := priceEUR * (100 + vatPercent)
	return Plan{
		PriceEUR:    priceEUR,
		VATPercent:  vatPercent,
		PriceExVAT:  fmt.Sprintf("%d", priceEUR),
		PriceIncVAT: fmt.Sprintf("%d,%02d", cents/100, cents%100),
		VATLabel:    fmt.Sprintf("%d%%", vatPercent),
	}
}

// ltPlural picks the Lithuanian form of a noun for a count.
//
// Lithuanian does not inflect on "is it one": the form follows the last digits.
// 21 takes the singular, 25 the nominative plural, and 100 — along with
// anything ending 11–19 — the genitive plural. The counts here come from
// configuration rather than from prose, so the rule has to be applied; a page
// that prints "100 užklausos" reads as broken Lithuanian to precisely the
// audience it was written in Lithuanian for.
//
// Counts reaching this are validated positive at startup, so there is no
// negative case to fold.
func ltPlural(n int, one, few, many string) string {
	switch last2, last1 := n%100, n%10; {
	case last2 >= 11 && last2 <= 19:
		return many
	case last1 == 1:
		return one
	case last1 == 0:
		return many
	default:
		return few
	}
}

// ratePhrase and batchPhrase render the two limits the plan sells as Lithuanian
// that agrees with its own numbers. They live beside the price because the plan
// card and llms.txt both print them and must print them identically.
func ratePhrase(perMin int) string {
	return fmt.Sprintf("%d %s per minutę", perMin,
		ltPlural(perMin, "užklausa", "užklausos", "užklausų"))
}

func batchPhrase(batchMax int) string {
	return fmt.Sprintf("%d %s vienoje užklausoje", batchMax,
		ltPlural(batchMax, "tekstas", "tekstai", "tekstų"))
}

// priceSentence is the offer as a clause that reads inside running prose, for
// the two surfaces that cannot use the landing page's layout to carry meaning:
// the article's FAQ answer and llms.txt. Both are lifted and quoted verbatim —
// by a search result, by an assistant — so the clause has to stand alone and
// still say which figure includes VAT.
func (p Plan) priceSentence() string {
	if !p.HasVAT() {
		return p.PriceExVAT + " € per mėnesį"
	}
	return p.PriceExVAT + " € + PVM per mėnesį (" + p.PriceIncVAT + " € su PVM)"
}

// HasVAT reports whether VAT is charged at all. An operator who is not VAT
// registered configures 0%, and for them "+ PVM" and a VAT-inclusive line are
// not a nuance — they are a wrong statement about the price, so the pages drop
// both rather than print "60,50 € su PVM" next to "50 € + 0%".
func (p Plan) HasVAT() bool { return p.VATPercent > 0 }
