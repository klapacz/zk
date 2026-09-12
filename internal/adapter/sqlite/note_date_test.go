package sqlite

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zk-org/zk/internal/adapter/fs"
	"github.com/zk-org/zk/internal/adapter/markdown"
	"github.com/zk-org/zk/internal/core"
	"github.com/zk-org/zk/internal/util"
	"github.com/zk-org/zk/internal/util/test/assert"
)

func TestNotebookIndexFrontmatterDates(t *testing.T) {
	for _, key := range []string{"date", "assigned"} {
		for _, force := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/force=%t", key, force), func(t *testing.T) {
				dir := t.TempDir()
				db, err := OpenInMemory()
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				storage, err := fs.NewFileStorage(dir, &util.NullLogger)
				if err != nil {
					t.Fatal(err)
				}
				config := core.NewDefaultConfig()
				config.Format.Markdown.Frontmatter.CreationDate = key
				notebook := core.NewNotebook(dir, config, core.NotebookPorts{
					NoteIndex:         NewNoteIndex(dir, db, &util.NullLogger, "md"),
					NoteContentParser: markdown.NewParser(markdown.ParserOpts{}, &util.NullLogger),
					FS:                storage,
					Logger:            &util.NullLogger,
				})
				writeNote := func(name, date, modified string) {
					t.Helper()
					content := "# " + name + "\nSynthetic note.\n"
					if date != "" {
						content = fmt.Sprintf("---\n%s: %s\nmodified: %s\n---\n%s", key, date, modified, content)
					}
					if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
						t.Fatal(err)
					}
				}
				index := func() {
					t.Helper()
					if _, err := notebook.Index(core.NoteIndexOpts{Force: force}); err != nil {
						t.Fatal(err)
					}
				}
				findNote := func(name string) *core.Note {
					t.Helper()
					note, err := notebook.FindNote(core.NoteFindOpts{IncludeHrefs: []string{name}})
					if err != nil {
						t.Fatal(err)
					}
					if note == nil {
						t.Fatalf("missing indexed note %s", name)
					}
					return note
				}

				writeNote("assigned.md", "2020-01-02", "2020-01-03")
				writeNote("middle.md", "2022-01-01", "2022-01-02")
				writeNote("undated.md", "", "")
				index()
				originalUndated := findNote("undated.md").Created

				writeNote("assigned.md", "2024-06-07", "2024-06-08")
				index()
				updated := findNote("assigned.md")
				created := time.Date(2024, 6, 7, 0, 0, 0, 0, time.UTC)
				modified := created.AddDate(0, 0, 1)
				assert.Equal(t, updated.Created, created)
				assert.Equal(t, updated.Modified, modified)
				assert.Equal(t, updated.Metadata[key], "2024-06-07")
				assert.Equal(t, findNote("undated.md").Created, originalUndated)

				notes, err := notebook.FindNotes(core.NoteFindOpts{
					CreatedStart: &created, CreatedEnd: &modified,
				})
				assert.Nil(t, err)
				assert.Equal(t, len(notes), 1)
				oldDate := time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC)
				oldEnd := oldDate.AddDate(0, 0, 1)
				notes, err = notebook.FindNotes(core.NoteFindOpts{CreatedStart: &oldDate, CreatedEnd: &oldEnd})
				assert.Nil(t, err)
				assert.Equal(t, len(notes), 0)
				notes, err = notebook.FindNotes(core.NoteFindOpts{
					IncludeHrefs: []string{"assigned.md", "middle.md"},
					Sorters:      []core.NoteSorter{{Field: core.NoteSortCreated, Ascending: true}},
				})
				assert.Nil(t, err)
				paths := make([]string, 0, len(notes))
				for _, note := range notes {
					paths = append(paths, note.Path)
				}
				assert.Equal(t, paths, []string{"middle.md", "assigned.md"})

				// An explicit date can also replace a previously indexed fallback.
				writeNote("undated.md", "2024-06-07", "2024-06-08")
				index()
				assert.Equal(t, findNote("undated.md").Created, created)

				// Without a valid explicit date, retain the indexed creation date.
				for _, date := range []string{"", "not-a-date"} {
					writeNote("assigned.md", date, "2024-06-08")
					index()
					assert.Equal(t, findNote("assigned.md").Created, created)
				}
			})
		}
	}
}
