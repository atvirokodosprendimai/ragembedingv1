package web

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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

// renderLanding fetches the public page as a visitor would, so the assertions
// below run against the real handler rather than a hand-built view model.
func renderLanding(t *testing.T, plan Plan, tokenBudget int64) string {
	t.Helper()

	h := NewLanding("bge-m3", 25, 100, tokenBudget, plan,
		NewContact("info@ituoga.lt", "+37063594444", "https://letas.lt"),
		slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	return w.Body.String()
}

// TestLandingPublishesThePrice is the end-to-end guard on the number people are
// charged: it must reach the page from configuration, in both the quoted and
// the VAT-inclusive form, together with the limits it buys.
func TestLandingPublishesThePrice(t *testing.T) {
	body := renderLanding(t, NewPlan(50, 21), -1)

	require.Contains(t, body, "Kiek kainuoja")
	require.Contains(t, body, "50&nbsp;€")
	require.Contains(t, body, "+ PVM")
	require.Contains(t, body, "60,50 € su PVM (21%)")
	// The limits the price buys, each read from configuration rather than
	// written into the markup.
	require.Contains(t, body, "Tokenai — neriboti")
	require.Contains(t, body, "100 užklausos per minutę")
	require.Contains(t, body, "25 tekstai vienoje užklausoje")
}

// TestLandingOfferIsMachineReadable: an assistant asked what this costs reads
// the Offer, not the prose. A price that renders but does not parse is invisible
// to exactly the audience the structured data exists for.
func TestLandingOfferIsMachineReadable(t *testing.T) {
	body := renderLanding(t, NewPlan(50, 21), -1)

	_, rest, found := strings.Cut(body, `<script type="application/ld+json">`)
	require.True(t, found, "the page must carry a JSON-LD block")
	raw, _, found := strings.Cut(rest, `</script>`)
	require.True(t, found)

	var graph struct {
		Graph []struct {
			Type   string `json:"@type"`
			Offers struct {
				Price             int    `json:"price"`
				PriceCurrency     string `json:"priceCurrency"`
				PriceSpecInterval struct {
					VATIncluded       bool `json:"valueAddedTaxIncluded"`
					ReferenceQuantity struct {
						Value    int    `json:"value"`
						UnitCode string `json:"unitCode"`
					} `json:"referenceQuantity"`
				} `json:"priceSpecification"`
			} `json:"offers"`
		} `json:"@graph"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &graph), "JSON-LD must parse")

	var service *int
	for i, node := range graph.Graph {
		if node.Type == "Service" {
			service = &i
		}
	}
	require.NotNil(t, service, "the graph must describe a Service")

	offer := graph.Graph[*service].Offers
	require.Equal(t, 50, offer.Price)
	require.Equal(t, "EUR", offer.PriceCurrency)
	// Monthly, and quoted before VAT — the two facts prose would leave ambiguous.
	require.False(t, offer.PriceSpecInterval.VATIncluded)
	require.Equal(t, 1, offer.PriceSpecInterval.ReferenceQuantity.Value)
	require.Equal(t, "MON", offer.PriceSpecInterval.ReferenceQuantity.UnitCode)
}

// TestLandingOmitsVATWhenNoneIsCharged: a 0% operator must not be made to
// publish "+ PVM" and a VAT-inclusive total, both of which would be false.
func TestLandingOmitsVATWhenNoneIsCharged(t *testing.T) {
	body := renderLanding(t, NewPlan(50, 0), -1)

	require.Contains(t, body, "50&nbsp;€")
	require.NotContains(t, body, "+ PVM")
	require.NotContains(t, body, "su PVM")
}

// TestLandingShowsAPrepaidBudget: the plan is sold on unlimited tokens, but an
// operator who issues prepaid keys must not have the page claim otherwise.
func TestLandingShowsAPrepaidBudget(t *testing.T) {
	body := renderLanding(t, NewPlan(50, 21), 100_000_000)

	require.Contains(t, body, "Tokenai — 100,000,000")
	require.NotContains(t, body, "Tokenai — neriboti")
}

// TestLLMsTxtStatesThePrice: llms.txt already promised the article covered
// "kaina" without ever giving a number. An assistant summarising this service
// should be able to answer "how much" from the file itself.
func TestLLMsTxtStatesThePrice(t *testing.T) {
	h := NewLanding("bge-m3", 25, 100, -1, NewPlan(50, 21),
		NewContact("info@ituoga.lt", "+37063594444", "https://letas.lt"),
		slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()

	r := httptest.NewRequest(http.MethodGet, "/llms.txt", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	require.Contains(t, body, "## Kiek kainuoja")
	require.Contains(t, body, "50 € + PVM per mėnesį (60,50 € su PVM)")
	require.Contains(t, body, "tokenai — neriboti, 100 užklausos per minutę, 25 tekstai")
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
