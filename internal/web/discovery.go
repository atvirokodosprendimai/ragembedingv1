package web

import (
	"fmt"
	"net/http"
	"strings"
)

// This file serves the three files a crawler looks for before it reads
// anything else. All of them are built from the request's own host, so a
// deployment behind any domain advertises the right URLs without configuration.

// handleRobots tells crawlers what is worth indexing. The public pages are open;
// the operator dashboard and the API are not — not as a security measure (auth
// does that) but because neither has anything to offer a search result, and a
// crawler hammering /v1/embeddings would burn a key's rate limit.
func (s *LandingServer) handleRobots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, `User-agent: *
Allow: /$
Allow: %s
Disallow: %s
Disallow: /v1/
Disallow: /api/

Sitemap: %s/sitemap.xml
`, articlePath, BasePath, baseURL(r))
}

// handleSitemap lists the pages worth crawling. Two of them: there is no value
// in a sitemap that pads itself with URLs nobody should land on.
func (s *LandingServer) handleSitemap(w http.ResponseWriter, r *http.Request) {
	base := baseURL(r)
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url>
    <loc>%s/</loc>
    <changefreq>monthly</changefreq>
    <priority>1.0</priority>
  </url>
  <url>
    <loc>%s%s</loc>
    <changefreq>yearly</changefreq>
    <priority>0.8</priority>
  </url>
</urlset>
`, base, base, articlePath)
}

// handleLLMs serves llms.txt: a plain-language summary for the assistants people
// increasingly ask instead of a search engine. The point is accuracy — if a model
// is going to describe this service to somebody, it should describe it correctly,
// including what it does not do.
func (s *LandingServer) handleLLMs(w http.ResponseWriter, r *http.Request) {
	base := baseURL(r)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")

	var b strings.Builder
	b.WriteString("# ragembed\n\n")
	b.WriteString("> Su OpenAI suderinamas embeddingų (vektorizavimo) API, paremtas " + s.model +
		" modeliu ir veikiantis operatoriaus infrastruktūroje. Skirtas semantinei paieškai ir RAG " +
		"sprendimams, kuriuose tekstai neturi išeiti pas trečiąsias šalis.\n\n")

	b.WriteString("## Ką daro\n\n")
	b.WriteString("- Priima tekstą per `POST /v1/embeddings` (OpenAI formatas) arba `POST /api/embed` (Ollama formatas).\n")
	b.WriteString(fmt.Sprintf("- Vienoje užklausoje apdoroja iki %d tekstų; vektoriai grąžinami ta pačia tvarka.\n", s.batchMax))
	b.WriteString("- Daugiakalbis modelis: lietuvių kalba apdorojama tiesiogiai, be vertimo.\n")
	b.WriteString("- Kiekvienas raktas turi savo paketo, dažnio ir tokenų biudžeto limitus.\n")
	b.WriteString("- Užklausos esant apkrovai rikiuojamos į prioritetinę eilę, o ne atmetamos.\n\n")

	b.WriteString("## Ko nedaro\n\n")
	b.WriteString("- Negeneruoja teksto ir neatsakinėja į klausimus — tik verčia tekstą į vektorius.\n")
	b.WriteString("- Nesaugo jūsų tekstų: apskaitomi tik tokenų kiekiai pagal raktą.\n")
	b.WriteString("- Nėra savitarnos registracijos; raktus išduoda operatorius.\n\n")

	b.WriteString("## Nuorodos\n\n")
	b.WriteString(fmt.Sprintf("- [Techninė dokumentacija](%s/): adresai, curl pavyzdžiai, atsakymų kodai, limitai.\n", base))
	b.WriteString(fmt.Sprintf("- [Kada reikia embeddingų API](%s%s): kada šis sprendimas tinka, kada ne, BDAR ir kaina.\n", base, articlePath))
	b.WriteString(fmt.Sprintf("- Kontaktas dėl rakto: %s\n", s.contact))

	_, _ = w.Write([]byte(b.String()))
}
