package scout

import (
	"strconv"
	"strings"
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
	if releaseYear == 0 {
		return false
	}
	titleYears := map[string]bool{}
	for _, year := range movieYearPattern.FindAllString(title, -1) {
		titleYears[year] = true
	}
	label := strings.NewReplacer("_", " ", "-", " ", ".", " ").Replace(name)
	for _, year := range movieYearPattern.FindAllString(label, -1) {
		parsed, _ := strconv.Atoi(year)
		if parsed != releaseYear && !titleYears[year] {
			return true
		}
	}
	return false
}
