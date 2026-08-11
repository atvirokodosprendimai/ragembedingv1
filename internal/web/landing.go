package web

import (
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// LandingServer serves the public, unauthenticated page that tells a client how
// to call the API. It is deliberately separate from the operator dashboard: it
// touches neither the key store nor the usage store, so there is no path by
// which it could leak who holds a key or what they spend.
type LandingServer struct {
	model      string
	batchMax   int
	ratePerMin int
	contact    string
	logger     *slog.Logger
}

// NewLanding builds the landing page from the gateway's own configuration, so
// the limits it documents are the ones the gateway actually applies to a new key
// rather than numbers copied into prose and left to rot.
func NewLanding(model string, batchMax, ratePerMin int, contact string, logger *slog.Logger) *LandingServer {
	if logger == nil {
		logger = slog.Default()
	}
	return &LandingServer{
		model:      model,
		batchMax:   batchMax,
		ratePerMin: ratePerMin,
		contact:    contact,
		logger:     logger,
	}
}

// Handler returns the public routes: the landing page, the article, the files
// crawlers ask for, and the stylesheet they all share.
func (s *LandingServer) Handler() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.handleIndex)
	r.Get(articlePath, s.handleArticle)
	r.Get("/robots.txt", s.handleRobots)
	r.Get("/sitemap.xml", s.handleSitemap)
	r.Get("/llms.txt", s.handleLLMs)

	sub, _ := fs.Sub(assets, "static")
	r.Handle("/assets/*", http.StripPrefix("/assets/", http.FileServer(http.FS(sub))))
	return r
}

func (s *LandingServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := Landing(s.build(r)).Render(r.Context(), w); err != nil {
		s.logger.Error("landing: render", "err", err)
	}
}

// LandingVM is the landing page's view model. Like the dashboard's, every string
// the template prints is computed here so the markup stays a pure projection.
type LandingVM struct {
	Model      string
	BaseURL    string // the address the visitor actually reached, for copy-paste curl
	BatchMax   string
	RatePerMin string
	AdminPath  string
	// ContactEmail is who to ask for a key. There is no self-service signup, so
	// without it the page tells a visitor to ask someone it cannot name.
	ContactEmail string
	Statuses     []StatusVM
}

// StatusVM is one row of the status-code contract.
type StatusVM struct {
	Code    string
	Meaning string
	Action  string
	Tone    string // "ok" | "warn" | "bad" — drives the row's accent
}

// build assembles the page for this request.
func (s *LandingServer) build(r *http.Request) LandingVM {
	return LandingVM{
		Model:        s.model,
		BaseURL:      baseURL(r),
		BatchMax:     strconv.Itoa(s.batchMax),
		RatePerMin:   strconv.Itoa(s.ratePerMin),
		AdminPath:    BasePath,
		ContactEmail: s.contact,
		Statuses: []StatusVM{
			{Code: "200", Meaning: "Vektoriai grąžinti", Action: "—", Tone: "ok"},
			{Code: "400", Meaning: "Netaisyklingas JSON arba per daug tekstų viename pakete", Action: "Pataisykite užklausą", Tone: "bad"},
			{Code: "401", Meaning: "Rakto nėra, jis neteisingas arba atšauktas", Action: "Patikrinkite raktą", Tone: "bad"},
			{Code: "402", Meaning: "Tokenų biudžetas išnaudotas", Action: "Palaukite mėnesio atnaujinimo arba paprašykite papildyti", Tone: "warn"},
			{Code: "429", Meaning: "Per daug užklausų per minutę", Action: "Kartokite po Retry-After antraštėje nurodyto laiko", Tone: "warn"},
			{Code: "502", Meaning: "Embeddingų serveris nepasiekiamas", Action: "Pakartokite netrukus", Tone: "bad"},
			{Code: "503", Meaning: "Užklausa nutraukta belaukiant eilėje", Action: "Pakartokite", Tone: "warn"},
		},
	}
}

// baseURL reconstructs the address the visitor used, so the curl examples are
// copy-pasteable rather than pointing at a placeholder host.
//
// X-Forwarded-Proto is consulted because the gateway usually runs behind a TLS
// terminator and would otherwise advertise http:// for an https:// site. The
// header is client-controlled, but it is used for display only — nothing is
// authorised or routed on it — so spoofing it only garbles the visitor's own
// copy of the example.
func baseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
