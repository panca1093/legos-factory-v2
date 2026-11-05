package kb

import (
	"hash/fnv"
	"math"
	"strings"
	"unicode"
)

const hashDim = 512

// HashEmbedder is a local, deterministic embedder: word unigrams and char
// trigrams hashed into a signed 512-dim vector. Lexical, not semantic.
type HashEmbedder struct{}

func (HashEmbedder) Name() string { return "hash-ngram-v1" }
func (HashEmbedder) Dim() int     { return hashDim }

func (HashEmbedder) Embed(text string) []float32 {
	vec := make([]float32, hashDim)
	r := []rune(strings.ToLower(text))
	if len(r) > 20000 {
		r = r[:20000]
	}
	add := func(feature string, w float32) {
		h := fnv.New64a()
		h.Write([]byte(feature))
		sum := h.Sum64()
		if sum>>63 == 1 {
			w = -w
		}
		vec[sum%hashDim] += w
	}
	for _, tok := range strings.FieldsFunc(string(r), func(c rune) bool { return !unicode.IsLetter(c) && !unicode.IsDigit(c) }) {
		add(tok, 1)
		padded := []rune(" " + tok + " ")
		for i := 0; i+3 <= len(padded); i++ {
			add(string(padded[i:i+3]), 0.5)
		}
	}
	var norm float64
	for _, v := range vec {
		norm += float64(v) * float64(v)
	}
	if norm == 0 {
		return vec
	}
	inv := float32(1 / math.Sqrt(norm))
	for i := range vec {
		vec[i] *= inv
	}
	return vec
}
