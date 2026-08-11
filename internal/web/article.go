package web

import (
	"encoding/json"
	"net/http"
)

// articlePath is the article's URL. It is a flat, keyword-bearing Lithuanian
// slug: the page has one job in search, and the words people type are the words
// in the path.
const articlePath = "/kada-reikia-embeddingu-api"

// articleHeadline is the page's one H1, reused verbatim in the structured data.
const articleHeadline = "Kada jūsų įmonei reikia embeddingų API (ir kada nereikia)"

// PublicPaths is every path the public site answers on. The composition root
// registers exactly these — rather than mounting the landing handler as a
// catch-all — so a typo'd API path still 404s instead of returning a marketing
// page with a 200.
var PublicPaths = []string{
	"/",
	articlePath,
	"/assets/landing.css",
	"/robots.txt",
	"/sitemap.xml",
	"/llms.txt",
}

// ArticleVM is the article page's view model. The prose lives in the template;
// what varies per request — the host in the examples, the contact address, the
// structured data — is assembled here.
type ArticleVM struct {
	Model        string
	BaseURL      string
	CanonicalURL string
	ContactEmail string
	BatchMax     string
	// FAQ is rendered twice: as visible questions and answers, and as JSON-LD.
	// One source for both, so the structured data can never claim something the
	// page does not actually say — which is what makes it a valid FAQPage rather
	// than a spam signal.
	FAQ []FAQItem
	// JSONLD is the Article + FAQPage graph, pre-serialised.
	JSONLD string
}

// FAQItem is one question and its answer, in the words a reader would use.
type FAQItem struct {
	Q string
	A string
}

// handleArticle renders the article.
func (s *LandingServer) handleArticle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := Article(s.buildArticle(r)).Render(r.Context(), w); err != nil {
		s.logger.Error("article: render", "err", err)
	}
}

func (s *LandingServer) buildArticle(r *http.Request) ArticleVM {
	base := baseURL(r)
	vm := ArticleVM{
		Model:        s.model,
		BaseURL:      base,
		CanonicalURL: base + articlePath,
		ContactEmail: s.contact,
		BatchMax:     intStr(s.batchMax),
		FAQ:          articleFAQ(s.model),
	}
	vm.JSONLD = articleJSONLD(vm)
	return vm
}

// articleFAQ is the question list. Each one is phrased the way somebody would
// type it into a search box, because that is what it has to match — not the way
// a product page would phrase it.
func articleFAQ(model string) []FAQItem {
	return []FAQItem{
		{
			Q: "Kas yra embeddingai paprastais žodžiais?",
			A: "Embeddingas yra teksto reikšmės atvaizdavimas skaičių eilute. Panašios reikšmės " +
				"tekstai gauna panašias eilutes, todėl kompiuteris gali surasti „apie tą patį“ " +
				"parašytus dokumentus net tada, kai juose nėra nė vieno bendro žodžio.",
		},
		{
			Q: "Kuo embeddingų paieška skiriasi nuo įprastos paieškos pagal raktažodžius?",
			A: "Raktažodžių paieška ieško sutampančių simbolių eilučių: neįrašius teisingo žodžio, " +
				"dokumentas nerandamas. Embeddingų paieška lygina reikšmę, todėl „kiek kainuoja " +
				"pristatymas“ randa dokumentą, kuriame parašyta „siuntimo įkainiai“. Praktikoje " +
				"geriausiai veikia abu metodai kartu.",
		},
		{
			Q: "Ar galiu siųsti įmonės dokumentus į OpenAI ar kitą debesijos paslaugą?",
			A: "Teisiškai tai duomenų perdavimas trečiajai šaliai, todėl reikia teisinio pagrindo, " +
				"sutarties su tvarkytoju ir aiškumo, kur duomenys apdorojami. Jei dokumentuose yra " +
				"asmens ar komercinės paslapties duomenų, paprasčiausias sprendimas dažnai yra " +
				"neišleisti jų iš savo infrastruktūros: tada perdavimo klausimo apskritai nelieka.",
		},
		{
			Q: "Ar embeddingai veikia su lietuvių kalba?",
			A: model + " yra daugiakalbis modelis ir lietuviškus tekstus apdoroja tiesiogiai, be " +
				"vertimo į anglų kalbą. Tai svarbu, nes vertimas prieš vektorizavimą praranda " +
				"dalį reikšmės ir prideda dar vieną klaidų šaltinį.",
		},
		{
			Q: "Kiek kainuoja embeddingai?",
			A: "Debesijos paslaugos ima mokestį už kiekvieną apdorotą tokeną, todėl kaina auga kartu " +
				"su duomenų kiekiu ir su kiekvienu pakartotiniu indeksavimu. Savame serveryje " +
				"mokate už techniką ir jos priežiūrą, o užklausų kiekis kainos nekeičia. Lūžio " +
				"taškas priklauso nuo to, kiek ir kaip dažnai perindeksuojate.",
		},
		{
			Q: "Kada embeddingų nereikia?",
			A: "Kai dokumentų nedaug ir jie gerai randami pagal pavadinimą ar raktažodį; kai " +
				"reikia tikslaus atitikmens (sąskaitos numeris, asmens kodas); ir kai problema iš " +
				"tikrųjų yra netvarkingi duomenys, o ne paieška. Tokiais atvejais embeddingai " +
				"prideda sudėtingumo, bet neduoda naudos.",
		},
		{
			Q: "Ką reiškia RAG?",
			A: "RAG (retrieval-augmented generation) yra būdas kalbos modeliui atsakyti remiantis " +
				"jūsų dokumentais: pirma embeddingų pagalba surandamos tinkamos teksto atkarpos, " +
				"paskui jos paduodamos modeliui kaip kontekstas. Embeddingai yra pirmoji šios " +
				"grandinės dalis ir dažniausiai nuo jų priklauso, ar atsakymas bus teisingas.",
		},
	}
}

// articleJSONLD builds the Article + FAQPage graph from the same view model the
// page renders, so the structured data cannot drift from what a reader sees —
// which is the difference between valid markup and a spam signal.
//
// encoding/json escapes <, > and & to \u003c-style sequences by default. That is
// usually a nuisance; inside a <script> block it is exactly what is wanted, since
// it makes it impossible for any text to close the element early.
func articleJSONLD(vm ArticleVM) string {
	type thing struct {
		Type string `json:"@type"`
		Name string `json:"name"`
	}
	type answer struct {
		Type string `json:"@type"`
		Text string `json:"text"`
	}
	type question struct {
		Type     string `json:"@type"`
		Name     string `json:"name"`
		Accepted answer `json:"acceptedAnswer"`
	}

	article := map[string]any{
		"@type":            "TechArticle",
		"headline":         articleHeadline,
		"inLanguage":       "lt",
		"mainEntityOfPage": vm.CanonicalURL,
		"description": "Kas yra embeddingai, kada jų verta imtis, kada ne, ką daryti su " +
			"asmens duomenimis pagal BDAR ir kiek tai kainuoja.",
		"about": []thing{
			{Type: "Thing", Name: "Embeddingai"},
			{Type: "Thing", Name: "Semantinė paieška"},
			{Type: "Thing", Name: "RAG"},
		},
	}

	questions := make([]question, 0, len(vm.FAQ))
	for _, item := range vm.FAQ {
		questions = append(questions, question{
			Type:     "Question",
			Name:     item.Q,
			Accepted: answer{Type: "Answer", Text: item.A},
		})
	}
	faq := map[string]any{
		"@type":      "FAQPage",
		"inLanguage": "lt",
		"mainEntity": questions,
	}

	graph := map[string]any{
		"@context": "https://schema.org",
		"@graph":   []any{article, faq},
	}

	out, err := json.Marshal(graph)
	if err != nil {
		// Every value here is a plain string or slice thereof, so this cannot
		// fail; dropping the block is still better than emitting broken markup.
		return ""
	}
	return string(out)
}
