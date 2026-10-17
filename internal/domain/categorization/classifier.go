package categorization

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/google/uuid"
)

// Weights of training examples by how sure we are of their label: a choice made by
// hand beats a rule, and a rule beats the bank's own category.
var sourceWeight = map[Source]float64{SourceManual: 3, SourceRule: 2, SourceBank: 1}

const (
	smoothing      = 0.5
	minTokenLength = 2
	reasonTokens   = 3
	// evidenceHalf is the weight of word evidence at which confidence keeps half of the
	// posterior; see calibrate.
	evidenceHalf = 2.0
)

// noiseTokens carry no information about what was bought.
var noiseTokens = map[string]bool{
	"platba": true, "kartou": true, "internetu": true, "na": true, "cz": true, "sk": true, "s.r.o": true, "sro": true,
	"a.s": true, "as": true, "spol": true, "the": true, "www": true, "com": true, "praha": true, "brno": true,
}

// Classifier is a multinomial naive Bayes model over the words of a transaction. It is
// small enough to rebuild from scratch whenever the user's categories change.
type Classifier struct {
	classes     []uuid.UUID
	prior       map[uuid.UUID]float64
	tokenCounts map[uuid.UUID]map[string]float64
	totals      map[uuid.UUID]float64
	vocabulary  int
	examples    int
}

type Example struct {
	Candidate  Candidate
	CategoryID uuid.UUID
	Source     Source
}

func Train(examples []Example) *Classifier {
	c := &Classifier{prior: map[uuid.UUID]float64{}, tokenCounts: map[uuid.UUID]map[string]float64{}, totals: map[uuid.UUID]float64{}}
	vocabulary := map[string]bool{}
	for _, e := range examples {
		weight := sourceWeight[e.Source]
		if weight == 0 {
			continue
		}
		c.examples++
		if _, ok := c.tokenCounts[e.CategoryID]; !ok {
			c.tokenCounts[e.CategoryID] = map[string]float64{}
			c.classes = append(c.classes, e.CategoryID)
		}
		c.prior[e.CategoryID] += weight
		for _, tok := range Features(e.Candidate) {
			c.tokenCounts[e.CategoryID][tok] += weight
			c.totals[e.CategoryID] += weight
			vocabulary[tok] = true
		}
	}
	c.vocabulary = len(vocabulary)
	return c
}

func (c *Classifier) Examples() int { return c.examples }

type Prediction struct {
	CategoryID uuid.UUID
	Confidence float64
	// Reasons are the words that pointed most strongly at this category.
	Reasons []string
}

// Predict returns up to k categories, most likely first. Confidences are posterior
// probabilities across all known categories and sum to at most 1.
func (c *Classifier) Predict(candidate Candidate, k int) []Prediction {
	tokens := Features(candidate)
	if len(c.classes) == 0 || len(tokens) == 0 {
		return nil
	}
	var priorTotal float64
	for _, p := range c.prior {
		priorTotal += p
	}
	scores := make([]float64, len(c.classes))
	for i, class := range c.classes {
		score := math.Log(c.prior[class] / priorTotal)
		for _, tok := range tokens {
			score += c.logLikelihood(class, tok)
		}
		scores[i] = score
	}
	probabilities := softmax(scores)
	order := make([]int, len(c.classes))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool { return probabilities[order[a]] > probabilities[order[b]] })

	out := make([]Prediction, 0, k)
	for _, i := range order[:min(k, len(order))] {
		class := c.classes[i]
		out = append(out, Prediction{CategoryID: class, Confidence: c.calibrate(class, tokens, probabilities[i]),
			Reasons: c.reasons(class, tokens)})
	}
	return out
}

// calibrate scales the posterior by how much word evidence backs it. Naive Bayes is
// sure of itself even when only generic features (operation type, amount) agree, which
// on a new shop meant 99 % for a wrong guess; with no known word confidence is near 0.
func (c *Classifier) calibrate(class uuid.UUID, tokens []string, posterior float64) float64 {
	var evidence float64
	for _, tok := range tokens {
		if _, ok := readable(tok); ok {
			evidence += c.tokenCounts[class][tok]
		}
	}
	return posterior * evidence / (evidence + evidenceHalf)
}

func (c *Classifier) logLikelihood(class uuid.UUID, tok string) float64 {
	return math.Log((c.tokenCounts[class][tok] + smoothing) / (c.totals[class] + smoothing*float64(c.vocabulary+1)))
}

// reasons ranks the transaction's words by how much more often they appear in this
// category than in the rest, and keeps the readable ones.
func (c *Classifier) reasons(class uuid.UUID, tokens []string) []string {
	type scored struct {
		word  string
		score float64
	}
	var ranked []scored
	seen := map[string]bool{}
	for _, tok := range tokens {
		word, ok := readable(tok)
		if !ok || seen[word] || c.tokenCounts[class][tok] == 0 {
			continue
		}
		seen[word] = true
		var elsewhere, elsewhereTotal float64
		for _, other := range c.classes {
			if other != class {
				elsewhere += c.tokenCounts[other][tok]
				elsewhereTotal += c.totals[other]
			}
		}
		here := (c.tokenCounts[class][tok] + smoothing) / (c.totals[class] + smoothing)
		there := (elsewhere + smoothing) / (elsewhereTotal + smoothing)
		ranked = append(ranked, scored{word, math.Log(here / there)})
	}
	sort.Slice(ranked, func(a, b int) bool { return ranked[a].score > ranked[b].score })
	out := []string{}
	for _, r := range ranked {
		if r.score <= 0 || len(out) == reasonTokens {
			break
		}
		out = append(out, r.word)
	}
	return out
}

// Features turns a transaction into the words the model counts. Prefixes keep a word in
// the counterparty apart from the same word in a message.
func Features(c Candidate) []string {
	var out []string
	add := func(prefix string, words []string) {
		for _, w := range words {
			out = append(out, prefix+w)
		}
	}
	counterparty := words(c.Counterparty)
	add("c:", counterparty)
	description := words(c.Description)
	inCounterparty := map[string]bool{}
	for _, w := range counterparty {
		inCounterparty[w] = true
	}
	var extra []string
	for _, w := range description {
		if !inCounterparty[w] {
			extra = append(extra, w)
		}
	}
	add("d:", extra)
	if op := normalize(c.OperationType); op != "" {
		out = append(out, "op:"+op)
	}
	if bank := normalize(c.BankCategory); bank != "" {
		out = append(out, "bank:"+bank)
	}
	if c.AmountMinor > 0 {
		out = append(out, "sign:+")
	} else {
		out = append(out, "sign:-")
	}
	out = append(out, "amt:"+magnitude(c.AmountMinor))
	return out
}

func words(s string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && r != '.' }) {
		w = strings.Trim(w, ".")
		if len([]rune(w)) < minTokenLength || noiseTokens[w] {
			continue
		}
		out = append(out, w)
	}
	return out
}

// magnitude buckets an amount by order of magnitude in whole units: tens, hundreds, thousands…
func magnitude(minor int64) string {
	whole := math.Abs(float64(minor)) / 100
	if whole < 1 {
		return "0"
	}
	return strconv.Itoa(int(math.Log10(whole)))
}

func readable(token string) (string, bool) {
	for _, prefix := range []string{"c:", "d:"} {
		if word, ok := strings.CutPrefix(token, prefix); ok {
			return word, true
		}
	}
	return "", false
}

func softmax(scores []float64) []float64 {
	maxScore := math.Inf(-1)
	for _, s := range scores {
		maxScore = math.Max(maxScore, s)
	}
	var sum float64
	out := make([]float64, len(scores))
	for i, s := range scores {
		out[i] = math.Exp(s - maxScore)
		sum += out[i]
	}
	for i := range out {
		out[i] /= sum
	}
	return out
}
