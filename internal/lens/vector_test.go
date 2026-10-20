package lens

import (
	"strings"
	"testing"
)

func TestTitleVectorFindsNearSpellingsAndNotStrangers(t *testing.T) {
	alice := TitleVector("Alice's Adventures in Wonderland")
	if len(alice) != VectorDims {
		t.Fatalf("a vector has %d dimensions: %d", VectorDims, len(alice))
	}
	if s := CosineSimilarity(alice, TitleVector("ALICE'S ADVENTURES IN WONDERLAND")); s < 0.999 {
		t.Fatalf("the same title in another case is itself: %v", s)
	}
	if s := CosineSimilarity(alice, TitleVector("alices adventures in wonderland")); s < 0.9 {
		t.Fatalf("the title without its apostrophe is nearly itself: %v", s)
	}
	cases := []struct {
		other string
		min   float64
		max   float64
	}{
		{"Alice's Adventures in Wonderlnd", 0.85, 1},
		{"Alices Adventure in Wonderland", 0.85, 1},
		{"Adventures in Wonderland, Alice's", 0.9, 1},
		{"Alice in Wonderland", 0.6, 0.95},
		{"Alicia en el País de las Maravillas", 0, 0.4},
		{"Through the Looking-Glass", 0, 0.35},
		{"On the Origin of Species", 0, 0.35},
	}
	for _, tc := range cases {
		s := CosineSimilarity(alice, TitleVector(tc.other))
		if s < tc.min || s > tc.max {
			t.Errorf("similarity to %q = %.3f, want between %.2f and %.2f", tc.other, s, tc.min, tc.max)
		}
	}
	if s := CosineSimilarity(TitleVector("Les Misérables"), TitleVector("Les Miserables")); s < 0.999 {
		t.Fatalf("accents do not count: %v", s)
	}
	if TitleVector("") != nil || TitleVector("   ") != nil {
		t.Fatal("an empty title has no vector")
	}
	if CosineSimilarity(nil, alice) != 0 || CosineSimilarity(alice, alice[:10]) != 0 {
		t.Fatal("vectors of different lengths are not compared")
	}
	literal := VectorLiteral(TitleVector("Sintel"))
	if !strings.HasPrefix(literal, "[") || !strings.HasSuffix(literal, "]") || strings.Count(literal, ",") != VectorDims-1 {
		t.Fatalf("the literal is pgvector's form: %.60s...", literal)
	}
	if VectorLiteral(nil) != "" {
		t.Fatal("no vector, no literal")
	}
	if VectorRelevance(0.3) != 0 || VectorRelevance(1) != 0.95 || VectorRelevance(0.5) != 0.475 {
		t.Fatalf("relevance from similarity: %v %v %v", VectorRelevance(0.3), VectorRelevance(1), VectorRelevance(0.5))
	}
}
