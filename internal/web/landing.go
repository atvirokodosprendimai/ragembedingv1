package web

import (
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/dustin/go-humanize"
	"github.com/go-chi/chi/v5"
)

// LandingServer serves the public, unauthenticated page that tells a client how
// to call the API. It is deliberately separate from the operator dashboard: it
// touches neither the key store nor the usage store, so there is no path by
// which it could leak who holds a key or what they spend.
type LandingServer struct {
	model       string
	batchMax    int
	ratePerMin  int
	tokenBudget int64
	plan        Plan
	contact     Contact
	logger      *slog.Logger
}

// Contact is how a reader reaches the operator. It travels as one value because
// the three parts are always published together — an email with no phone and no
// company behind it reads like an anonymous side project.
type Contact struct {
	Email string
	// Phone in international form, digits only apart from the leading plus:
	// this is what goes in a tel: link.
	Phone string
	// PhoneLabel is the same number grouped for reading.
	PhoneLabel string
	// CompanyURL is the operator's own site.
	CompanyURL string
	// CompanyName is the site's domain, used as the visible link text and as the
	// organisation name in structured data.
	CompanyName string
}

// NewLanding builds the landing page from the gateway's own configuration, so
// the limits it documents are the ones the gateway actually applies to a new key
// rather than numbers copied into prose and left to rot. The published price
// arrives the same way and for the same reason: what the page sells and what a
// new key is issued with are one set of numbers, read from one place.
func NewLanding(model string, batchMax, ratePerMin int, tokenBudget int64, plan Plan, contact Contact, logger *slog.Logger) *LandingServer {
	if logger == nil {
		logger = slog.Default()
	}
	return &LandingServer{
		model:       model,
		batchMax:    batchMax,
		ratePerMin:  ratePerMin,
		tokenBudget: tokenBudget,
		plan:        plan,
		contact:     contact,
		logger:      logger,
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
	// TokenBudget is what a new key may spend, already in words: "neriboti" for
	// the unlimited default, otherwise the allowance grouped for reading. The
	// price section sells this figure, so it is read from configuration rather
	// than asserted in markup — a page promising unlimited tokens while the
	// gateway issues prepaid keys is the one drift that costs money.
	TokenBudget string
	AdminPath   string
	// Contact is who to ask for a key. There is no self-service signup, so
	// without it the page tells a visitor to ask someone it cannot name.
	Contact Contact
	// Plan is what the key costs. It sits beside the limits above because the
	// two answer one question — what do I get, and for how much.
	Plan     Plan
	Statuses []StatusVM
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
		Model:       s.model,
		BaseURL:     baseURL(r),
		BatchMax:    strconv.Itoa(s.batchMax),
		RatePerMin:  strconv.Itoa(s.ratePerMin),
		TokenBudget: tokenBudgetLabel(s.tokenBudget),
		AdminPath:   BasePath,
		Contact:     s.contact,
		Plan:        s.plan,
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

// tokenBudgetLabel puts a key's token allowance into the words the price
// section uses. -1 is the unlimited default the plan is sold on; anything
// positive is a prepaid allowance, grouped so a nine-digit number can be read
// at a glance rather than counted digit by digit.
func tokenBudgetLabel(budget int64) string {
	if budget < 0 {
		return "neriboti"
	}
	return humanize.Comma(budget)
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

// organizationJSONLD builds the schema.org Organization node for the operator.
// Both public pages emit it — the article as the publisher of the text, the
// landing page as the provider of the service — and they must describe the same
// organisation to the same crawler, so the node is built once here rather than
// written out twice and left to diverge.
func organizationJSONLD(c Contact) map[string]any {
	return map[string]any{
		"@type": "Organization",
		"name":  c.CompanyName,
		"url":   c.CompanyURL,
		"email": c.Email,
		"contactPoint": map[string]any{
			"@type":             "ContactPoint",
			"contactType":       "sales",
			"telephone":         c.Phone,
			"email":             c.Email,
			"availableLanguage": []string{"lt", "en"},
		},
	}
}

// NewContact assembles the published contact details. The phone is stored twice
// on purpose: dialling needs unpunctuated digits, reading needs grouping, and
// deriving one from the other in a template is how they drift apart.
func NewContact(email, phone, companyURL string) Contact {
	return Contact{
		Email:       email,
		Phone:       phone,
		PhoneLabel:  groupPhone(phone),
		CompanyURL:  companyURL,
		CompanyName: hostOf(companyURL),
	}
}

// groupPhone renders a Lithuanian mobile number the way it is read aloud:
// +370 6xx xxxxx. Anything that is not a Lithuanian mobile is left alone rather
// than mangled into a shape it does not have.
func groupPhone(phone string) string {
	const ltMobile = "+3706"
	if len(phone) != 12 || !strings.HasPrefix(phone, ltMobile) {
		return phone
	}
	return phone[:4] + " " + phone[4:7] + " " + phone[7:]
}

// hostOf is the display name for a company URL: the bare host, since that is
// how people say and recognise it.
func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return rawURL
	}
	return strings.TrimPrefix(u.Host, "www.")
}
