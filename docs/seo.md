# Search strategy for the public pages

What the two public pages are trying to rank for, why they are shaped the way
they are, and what still needs real data.

> **Read this first.** No keyword volumes appear in this document. I have no
> access to Search Console, Ahrefs, Semrush or the Keyword Planner for this
> domain, so any number here would be invented. What follows are **intent
> hypotheses** — query *shapes* that follow from who the buyers are — plus the
> instructions for validating them once the site has been live for a month.
> Treat the query lists as a starting hypothesis to test, not as researched
> demand.

## The two audiences, and why it matters

This service has an unusual split, and it drives every decision below.

| | Developer | Decision-maker |
|---|---|---|
| Searches in | Mostly **English** | Mostly **Lithuanian** |
| Types | `bge-m3 api`, `self hosted embeddings` | „ar galima siųsti duomenis į ChatGPT" |
| Wants | curl, dimensions, limits, latency | risk, legality, cost, whether it is needed at all |
| Already knows | what an embedding is | that search "doesn't work" |
| Lands on | `/` | `/kada-reikia-embeddingu-api` |

Writing one page for both is how most technical services fail at search: the
developer page has no answers for the buyer, and marketing copy has no answers
for the developer. So there are two pages with different jobs, cross-linked.

The Lithuanian-language angle is the strongest differentiator available.
"Multilingual embeddings API" is a crowded global term; **"embeddingai lietuvių
kalbai"** is a specific need with few pages answering it, and this service
genuinely answers it (bge-m3 handles Lithuanian directly, no translation step).
That claim is load-bearing and true, which is exactly the kind of claim worth
building a page around.

## Intents and the query shapes behind them

Ordered by how far the searcher is from buying. The ones nearest the bottom
convert; the ones nearest the top are how people arrive months earlier.

### 1. Problem-aware, does not know the technology exists

They describe the symptom, not the solution. This is the largest and least
contested group.

- „darbuotojai neranda dokumentų", „paieška intranete neveikia"
- „kaip surasti informaciją savo dokumentuose"
- „ChatGPT su savo dokumentais", „dirbtinis intelektas įmonės dokumentams"
- EN: `search my own documents ai`, `chatbot trained on my documents`

**Answered by:** the article's opening and *Penki požymiai, kad jums jų reikia*.
The lede deliberately describes the symptom ("žmonės nebesuranda savo
dokumentų") before naming the technology, because that is the language they
arrive with.

### 2. Learning the concept

- „kas yra embeddingai", „embeddings kas tai", „vektorinė paieška"
- „semantinė paieška kas tai", „RAG kas tai", „ką reiškia RAG"
- EN: `what are embeddings`, `what is rag`, `vector search explained`

**Answered by:** *Kas yra embeddingai paprastais žodžiais* plus two FAQ entries.
The definition avoids jargon and leads with a worked example (pristatymas /
siuntimo įkainiai), because the winning page for a "what is X" query is the one
a non-specialist finishes reading.

### 3. Compliance and risk — the highest-intent Lithuanian queries

In my judgement this is where the best-qualified traffic is. Somebody asking
this has a project, a legal blocker, and a budget.

- „ar galima siųsti asmens duomenis į ChatGPT"
- „BDAR dirbtinis intelektas", „GDPR AI duomenų perdavimas"
- „duomenų saugumas dirbtinis intelektas", „AI be duomenų perdavimo"
- EN: `gdpr openai embeddings`, `self hosted embeddings gdpr`

**Answered by:** *Asmens duomenys, BDAR ir debesija*. Note it does **not** claim
that cloud APIs are illegal — that would be false and easily refuted. It says
the transfer must be assessed, and that self-hosting removes the question. The
page also states plainly that it is not legal advice; over-claiming here would
cost more trust than the traffic is worth.

### 4. Cost and alternatives

- „openai embeddings kaina", „embeddings API kaina"
- „openai alternatyva", „self hosted embeddings"
- EN: `openai embeddings pricing`, `openai embeddings alternative`, `ollama vs openai embeddings`

**Answered by:** *Kiek tai kainuoja*, which gives the real decision rule
(re-indexing frequency, not corpus size) instead of a comparison table that
would be out of date within a quarter and that we cannot honestly populate
without the reader's own numbers.

### 5. Lithuanian-language specific — the differentiator

- „embeddingai lietuvių kalbai", „lietuvių kalbos modelis"
- „semantinė paieška lietuviškai", „ar veikia su lietuvių kalba"
- EN: `multilingual embeddings lithuanian`, `bge-m3 lithuanian`

**Answered by:** *Ar tai veikia su lietuvių kalba*, and it is the reason the
whole site is in Lithuanian rather than English with a translation.

### 6. Technical / already shopping

- `bge-m3 api`, `embeddings api`, `openai compatible embeddings api`
- „vektorizavimo paslauga", „embeddings API Lietuvoje"

**Answered by:** the landing page `/`, which leads with the base URL and a
working curl inside the first screen.

## How the pages are built for this

- **Every H2 is a question or a decision**, phrased the way it is typed. Headings
  are what both search engines and assistants use to decide what a section
  answers.
- **Answer-first paragraphs.** Each section answers its own heading in the first
  sentence or two, then explains. This is what gets quoted in an AI answer or a
  featured snippet — a section that warms up for three paragraphs gets skipped.
- **`FAQPage` + `TechArticle` JSON-LD**, generated from the same view model that
  renders the visible FAQ. The structured data can never claim something the page
  does not say, which is the line between valid markup and a spam signal.
- **Honest negative section.** *Kada jų tikrai nereikia* exists because pages that
  only sell do not get linked to or quoted, and because a reader who disqualifies
  themselves early is cheaper than one who churns.
- **`llms.txt`** with an explicit "what it does not do" section. People
  increasingly ask an assistant instead of a search engine; if a model is going to
  describe this service, it should describe it correctly.
- **One H1 per page, canonical URLs, OG tags, `lang="lt"`, keyword-bearing slug**
  (`/kada-reikia-embeddingu-api`), title under ~60 characters and description
  under ~155 so neither truncates.
- **Cross-links** in both directions: the landing page sends the unsure reader to
  the article, the article sends the convinced reader back to the docs.

## What is deliberately not done

- **No comparison table against named competitors.** It would need pricing we
  cannot keep current, and it invites the reader to leave and compare.
- **No invented statistics.** No "70% of companies…" claims. Made-up figures are
  the fastest way to lose a technical reader.
- **No blog.** One good article beats six thin ones, and nobody is committed to
  maintaining a publishing cadence.

## Validate the hypotheses, then rewrite this file

The above is reasoning, not evidence. After a month live:

1. **Search Console** → Performance → Queries. Filter to Lithuania. The queries
   that actually bring impressions will not be the ones guessed here — some
   sections will pull traffic nobody predicted.
2. **Impressions with no clicks** = the page ranks but the title/description do
   not match the intent. Rewrite those two fields first; it is the cheapest win
   available.
3. **Clicks with no contact** = the page answers the question and ends. Look at
   whether the CTA appears anywhere near where the answer lands.
4. **Split any section that pulls its own traffic** into a page of its own. If
   the BDAR section earns impressions on its own, it deserves a URL.
5. Ask arriving customers *what they typed*. For a market this size, ten honest
   answers beat any keyword tool.

## Deployment checklist

- [ ] Serve over HTTPS with a real domain (canonical and OG URLs follow the
      request host automatically, and `X-Forwarded-Proto` is respected).
- [ ] Submit `/sitemap.xml` in Search Console.
- [ ] Confirm `/robots.txt` disallows `/admin`, `/v1/` and `/api/` in production.
- [ ] Run the article through the Rich Results test to confirm the `FAQPage`
      markup is accepted.
- [ ] Link to the article from any existing company site — a page nothing links
      to takes far longer to be crawled.
