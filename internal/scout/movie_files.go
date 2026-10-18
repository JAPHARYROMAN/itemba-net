package scout

import (
	"strings"

	"github.com/itemba-net/core/internal/lens"
)

func archiveNonMovieFile(name string) bool {
	words := " " + strings.Join(titleWords(strings.ReplaceAll(name, "_", " ")), " ") + " "
	for _, marker := range []string{"dvd menu", "disc menu", "menu", "compilation", "double feature", "triple feature"} {
		if strings.Contains(words, " "+marker+" ") {
			return true
		}
	}
	return false
}

// Item descriptions can be mislabeled. An explicit filename year that is
// neither the release year nor part of the movie title contradicts that item.
func movieFileYearConflicts(name, title string, releaseYear int) bool {
	return lens.FileYearConflicts(name, title, releaseYear)
}
