package scout

import (
	"strings"
)

const maxArchiveMovieMetadata = 6

func archiveMetadataStrings(raw any) []string {
	if text, ok := raw.(string); ok {
		return []string{text}
	}
	var out []string
	if values, ok := raw.([]any); ok {
		for _, value := range values {
			if text, ok := value.(string); ok {
				out = append(out, text)
			}
		}
	}
	return out
}

func archiveNonMovieEdition(query, title string) bool {
	words := " " + strings.Join(titleWords(title), " ") + " "
	requested := " " + strings.Join(titleWords(query), " ") + " "
	for _, phrase := range []string{"dvd menu", "dvd menus", "disc menu", "disc menus", "collection", "compilation", "sample", "preview", "promo", "double feature", "triple feature"} {
		if strings.Contains(words, " "+phrase+" ") && !strings.Contains(requested, " "+phrase+" ") {
			return true
		}
	}
	return hasUnrequestedDerivative(query, title)
}
