package sqlite

import (
	"testing"
	"time"

	"github.com/zk-org/zk/internal/core"
	"github.com/zk-org/zk/internal/util/test/assert"
)

func TestNoteDAOFindMatchWithLinks(t *testing.T) {
	testNoteDAOWithFixtures(t, "", func(tx Transaction, dao *NoteDAO) {
		ids := map[string]core.NoteID{}
		for _, note := range []core.Note{
			{Path: "hub.md", Title: "Hub", Body: "Links to other notes"},
			{Path: "apple.md", Title: "Apple", Body: "quasar matching direct note"},
			{Path: "banana.md", Title: "Banana", Body: "Not a search result"},
			{Path: "deep.md", Title: "Deep", Body: "quasar indirect note"},
			{Path: "outside.md", Title: "Outside", Body: "quasar unlinked note"},
		} {
			note.RawContent = note.Body
			note.Created = time.Date(2024, 6, 7, 0, 0, 0, 0, time.UTC)
			id, err := dao.Add(note)
			if err != nil {
				t.Fatal(err)
			}
			ids[note.Path] = id
		}
		for _, link := range [][2]string{
			{"hub.md", "apple.md"}, {"hub.md", "banana.md"},
			{"apple.md", "hub.md"}, {"banana.md", "hub.md"},
			{"apple.md", "deep.md"}, {"deep.md", "apple.md"},
			// Multiple links must not duplicate notes in the intersection.
			{"hub.md", "apple.md"},
		} {
			_, err := tx.Exec(`INSERT INTO links (source_id, target_id, href, title, snippet)
				VALUES (?, ?, ?, 'link', 'A link to another note')`, ids[link[0]], ids[link[1]], link[1])
			if err != nil {
				t.Fatal(err)
			}
		}
		_, err := tx.Exec(`INSERT INTO collections (name, kind) VALUES ('chosen', 'tag')`)
		assert.Nil(t, err)
		_, err = tx.Exec(`INSERT INTO notes_collections (note_id, collection_id)
			SELECT ?, id FROM collections WHERE name = 'chosen'`, ids["apple.md"])
		assert.Nil(t, err)

		link := func(recursive bool, distance int, negate bool) *core.LinkFilter {
			return &core.LinkFilter{Hrefs: []string{"hub.md"}, Recursive: recursive, MaxDistance: distance, Negate: negate}
		}
		start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
		end := start.AddDate(1, 0, 0)
		tests := []struct {
			name string
			opts core.NoteFindOpts
			want []string
		}{
			{"fts only", core.NoteFindOpts{}, []string{"apple.md", "deep.md", "outside.md"}},
			{"linked by", core.NoteFindOpts{LinkedBy: link(false, 0, false)}, []string{"apple.md"}},
			{"link to", core.NoteFindOpts{LinkTo: link(false, 0, false)}, []string{"apple.md"}},
			{"both directions", core.NoteFindOpts{LinkedBy: link(false, 0, false), LinkTo: link(false, 0, false)}, []string{"apple.md"}},
			{"recursive linked by", core.NoteFindOpts{LinkedBy: link(true, 0, false)}, []string{"apple.md", "deep.md"}},
			{"recursive link to", core.NoteFindOpts{LinkTo: link(true, 0, false)}, []string{"apple.md", "deep.md"}},
			{"linked by distance", core.NoteFindOpts{LinkedBy: link(true, 1, false)}, []string{"apple.md"}},
			{"link to distance", core.NoteFindOpts{LinkTo: link(true, 1, false)}, []string{"apple.md"}},
			{"not linked by", core.NoteFindOpts{LinkedBy: link(false, 0, true)}, []string{"deep.md", "outside.md"}},
			{"not link to", core.NoteFindOpts{LinkTo: link(false, 0, true)}, []string{"deep.md", "outside.md"}},
			{"related", core.NoteFindOpts{Related: []string{"hub.md"}}, []string{"deep.md"}},
			{"multiple predicates", core.NoteFindOpts{
				LinkedBy: link(true, 0, false), Match: []string{"quasar", "matching"},
				Tags: []string{"chosen"}, CreatedStart: &start, CreatedEnd: &end, Limit: 1,
			}, []string{"apple.md"}},
			{"empty intersection", core.NoteFindOpts{LinkedBy: link(false, 0, false), Match: []string{"unlinked"}}, []string{}},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				opts := tt.opts
				if opts.Match == nil {
					opts.Match = []string{"quasar"}
				}
				opts.MatchStrategy = core.MatchStrategyFts
				opts.Sorters = []core.NoteSorter{{Field: core.NoteSortPath, Ascending: true}}
				notes, err := dao.Find(opts)
				assert.Nil(t, err)
				paths := make([]string, 0, len(notes))
				for _, note := range notes {
					paths = append(paths, note.Path)
				}
				assert.Equal(t, paths, tt.want)
				minimal, err := dao.FindMinimal(opts)
				assert.Nil(t, err)
				paths = make([]string, 0, len(minimal))
				for _, note := range minimal {
					paths = append(paths, note.Path)
				}
				assert.Equal(t, paths, tt.want)
			})
		}

		t.Run("link snippets", func(t *testing.T) {
			notes, err := dao.Find(core.NoteFindOpts{
				Match: []string{"quasar"}, MatchStrategy: core.MatchStrategyFts,
				LinkTo: link(false, 0, false),
			})
			if err != nil || len(notes) != 1 {
				t.Fatalf("Find returned %d notes, error %v", len(notes), err)
			}
			assert.Equal(t, notes[0].Snippets, []string{"A <zk:match>link</zk:match> to another note"})
		})
		t.Run("related search snippet", func(t *testing.T) {
			notes, err := dao.Find(core.NoteFindOpts{
				Match: []string{"quasar"}, MatchStrategy: core.MatchStrategyFts,
				Related: []string{"hub.md"},
			})
			if err != nil || len(notes) != 1 {
				t.Fatalf("Find returned %d notes, error %v", len(notes), err)
			}
			assert.Equal(t, notes[0].Snippets, []string{"<zk:match>quasar</zk:match> indirect note"})
		})
	})
}

func TestNoteDAOFindReturnsRowErrors(t *testing.T) {
	testNoteDAO(t, func(tx Transaction, dao *NoteDAO) {
		opts := core.NoteFindOpts{Match: []string{"["}, MatchStrategy: core.MatchStrategyRe}
		// The invalid regexp fails during iteration, not query preparation.
		rows, err := dao.findRows(opts, noteSelectionFull)
		if err != nil {
			t.Fatal(err)
		}
		assert.False(t, rows.Next())
		assert.Err(t, rows.Err(), "error parsing regexp")
		rows.Close()

		_, err = dao.Find(opts)
		assert.Err(t, err, "error parsing regexp")
		_, err = dao.FindMinimal(opts)
		assert.Err(t, err, "error parsing regexp")
	})
}
