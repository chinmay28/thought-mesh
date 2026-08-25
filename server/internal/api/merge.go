package api

import (
	"net/http"

	"github.com/chinmay28/thought-mesh/server/internal/merge"
	"github.com/chinmay28/thought-mesh/server/internal/vault"
)

// mergeText is the third option on a save conflict.
//
// When a save comes back 409 the editor already offers "load theirs" and "keep
// mine". Merging needs the version the edit *started* from, and the only place
// that exists is the client — it loaded it. So this route is stateless: the
// browser posts the three texts, the server runs the same diff3 cloud sync
// uses, and the merged draft goes back into the textarea for the person who
// wrote both halves to finish.
//
// Keeping the algorithm here rather than in the PWA is the same rule the rest
// of the app follows: the client holds no domain logic, and one merge
// implementation means a sync conflict and an editor conflict resolve
// identically.
func (s *server) mergeText(w http.ResponseWriter, r *http.Request) {
	var body struct {
		// Base is the common ancestor — what the editor loaded before either
		// side changed. Absent (or explicitly null) means there isn't one, and
		// the merge falls back to reconciling the shared prefix and suffix.
		Base   *string `json:"base"`
		Mine   string  `json:"mine"`
		Theirs string  `json:"theirs"`
	}
	if err := decodeBody(r, &body); err != nil {
		handleErr(w, err)
		return
	}
	base := ""
	if body.Base != nil {
		base = *body.Base
	}
	if len(body.Mine) > maxMergeBytes || len(body.Theirs) > maxMergeBytes || len(base) > maxMergeBytes {
		handleErr(w, &vault.ValidationError{Msg: "that document is too large to merge"})
		return
	}
	result := merge.Merge(base, body.Mine, body.Theirs, body.Base != nil)
	writeJSON(w, http.StatusOK, mergeJSON(result))
}

// maxMergeBytes bounds one side of a merge. Far past any note anyone writes by
// hand; present because the merge is quadratic in the worst case and this
// endpoint takes its input straight from a request body.
const maxMergeBytes = 4 << 20 // 4 MiB

// mergeJSON is the wire shape of a merge result, shared with cloud sync's
// conflict detail so the client renders both the same way.
type mergeJSONBody struct {
	Merged string `json:"merged"`
	// Conflicts is how many regions both sides rewrote; 0 means the merge came
	// out clean and can be saved as-is.
	Conflicts int `json:"conflicts"`
}

func mergeJSON(r merge.Result) mergeJSONBody {
	return mergeJSONBody{Merged: r.Text, Conflicts: r.Conflicts}
}
