package scout

import (
	"strings"
)

// These are branding/encoding wrappers used on the official instance, not
// general words to erase from arbitrary movie titles or search results.
func normalizeBlenderMovieTitle(title string) string {
	title = strings.TrimSpace(title)
	for _, suffix := range []string{
		" - Third Open Movie by Blender Foundation", " - Blender VFX Open Movie",
		" - Official Blender Foundation Short Film", " - Blender animated cartoon",
		" - Funny 3D Animated Short", " - Blender Animated Short",
		". Official Blender Foundation release.",
	} {
		if strings.HasSuffix(strings.ToLower(title), strings.ToLower(suffix)) && len(title) > len(suffix) {
			title = strings.TrimSpace(title[:len(title)-len(suffix)])
			break
		}
	}
	if strings.HasSuffix(title, " 60fps 4K") {
		title = strings.TrimSpace(strings.TrimSuffix(title, " 60fps 4K"))
	}
	return title
}
