package core

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zk-org/zk/internal/util/paths"
	"github.com/zk-org/zk/internal/util/test/assert"
)

func TestIndexTaskReturnsPerNoteWriteErrors(t *testing.T) {
	tests := []struct {
		name string
		kind paths.DiffKind
	}{
		{name: "add", kind: paths.DiffAdded},
		{name: "update", kind: paths.DiffModified},
		{name: "remove", kind: paths.DiffRemoved},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			index := &indexTaskTestIndex{}
			writeErr := errors.New("persistence failed")

			switch test.kind {
			case paths.DiffAdded:
				writeTestNote(t, dir, "note.md")
				index.add = func(note Note, fixLinks bool) (NoteID, error) {
					return 42, writeErr
				}
			case paths.DiffModified:
				writeTestNote(t, dir, "note.md")
				index.indexed = []paths.Metadata{{Path: "note.md", Modified: time.Time{}}}
				index.update = func(note Note) error { return writeErr }
			case paths.DiffRemoved:
				index.indexed = []paths.Metadata{{Path: "note.md", Modified: time.Time{}}}
				index.remove = func(path string) error { return writeErr }
			}

			task := indexTask{
				path:   dir,
				config: NewDefaultConfig(),
				index:  index,
				parser: indexTaskTestParser{parse: func(absPath string) (*Note, error) {
					return &Note{Path: filepath.Base(absPath)}, nil
				}},
				logger: &indexTaskTestLogger{},
			}
			_, err := task.execute(func(change paths.DiffChange) {})

			assert.Err(t, err, writeErr.Error())
			assert.False(t, index.batchCalled)
		})
	}
}

func TestIndexTaskToleratesParseErrors(t *testing.T) {
	dir := t.TempDir()
	writeTestNote(t, dir, "bad.md")
	parseErr := errors.New("invalid note")
	logger := &indexTaskTestLogger{}
	index := &indexTaskTestIndex{
		add: func(note Note, fixLinks bool) (NoteID, error) {
			t.Fatal("Add called for a note which failed to parse")
			return 0, nil
		},
	}
	task := indexTask{
		path:   dir,
		config: NewDefaultConfig(),
		index:  index,
		parser: indexTaskTestParser{parse: func(absPath string) (*Note, error) {
			return nil, parseErr
		}},
		logger: logger,
	}

	stats, err := task.execute(func(change paths.DiffChange) {})
	assert.Nil(t, err)
	assert.Equal(t, stats.AddedCount, 1)
	assert.True(t, index.batchCalled)
	assert.Equal(t, index.batchIDs, []NoteID{})
	assert.Equal(t, index.batchPaths, []string{})
	assert.Equal(t, logger.errs, []error{parseErr})
}

func TestDrainMetadataChannelsConsumesBothProducers(t *testing.T) {
	source := make(chan paths.Metadata)
	target := make(chan paths.Metadata)
	sourceDone := make(chan struct{})
	targetDone := make(chan struct{})

	go func() {
		defer close(sourceDone)
		defer close(source)
		source <- paths.Metadata{Path: "a.md"}
		source <- paths.Metadata{Path: "b.md"}
	}()
	go func() {
		defer close(targetDone)
		defer close(target)
		target <- paths.Metadata{Path: "c.md"}
		target <- paths.Metadata{Path: "d.md"}
	}()

	drainMetadataChannels(source, target)
	<-sourceDone
	<-targetDone
}

func writeTestNote(t *testing.T, dir, path string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, path), []byte("# Note\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

type indexTaskTestParser struct {
	parse func(absPath string) (*Note, error)
}

func (p indexTaskTestParser) ParseNoteAt(absPath string) (*Note, error) {
	return p.parse(absPath)
}

type indexTaskTestLogger struct {
	errs []error
}

func (l *indexTaskTestLogger) Printf(format string, v ...any) {}
func (l *indexTaskTestLogger) Println(v ...any)               {}
func (l *indexTaskTestLogger) Err(err error) {
	if err != nil {
		l.errs = append(l.errs, err)
	}
}

type indexTaskTestIndex struct {
	indexed []paths.Metadata
	add     func(note Note, fixLinks bool) (NoteID, error)
	update  func(note Note) error
	remove  func(path string) error

	batchCalled bool
	batchIDs    []NoteID
	batchPaths  []string
}

func (m *indexTaskTestIndex) Find(opts NoteFindOpts) ([]ContextualNote, error) {
	return nil, nil
}

func (m *indexTaskTestIndex) FindMinimal(opts NoteFindOpts) ([]MinimalNote, error) {
	return nil, nil
}

func (m *indexTaskTestIndex) FindLinksBetweenNotes(ids []NoteID) ([]ResolvedLink, error) {
	return nil, nil
}

func (m *indexTaskTestIndex) FindCollections(kind CollectionKind, sorters []CollectionSorter) ([]Collection, error) {
	return nil, nil
}

func (m *indexTaskTestIndex) IndexedPaths() (<-chan paths.Metadata, error) {
	metadata := make(chan paths.Metadata, len(m.indexed))
	for _, item := range m.indexed {
		metadata <- item
	}
	close(metadata)
	return metadata, nil
}

func (m *indexTaskTestIndex) Add(note Note, fixLinks bool) (NoteID, error) {
	if m.add == nil {
		return 1, nil
	}
	return m.add(note, fixLinks)
}

func (m *indexTaskTestIndex) Update(note Note) error {
	if m.update == nil {
		return nil
	}
	return m.update(note)
}

func (m *indexTaskTestIndex) Remove(path string) error {
	if m.remove == nil {
		return nil
	}
	return m.remove(path)
}

func (m *indexTaskTestIndex) BatchUpdateLinks(ids []NoteID, paths []string) error {
	m.batchCalled = true
	m.batchIDs = append([]NoteID{}, ids...)
	m.batchPaths = append([]string{}, paths...)
	return nil
}

func (m *indexTaskTestIndex) Commit(transaction func(idx NoteIndex) error) error {
	return transaction(m)
}

func (m *indexTaskTestIndex) NeedsReindexing() (bool, error) {
	return false, nil
}

func (m *indexTaskTestIndex) SetNeedsReindexing(needsReindexing bool) error {
	return nil
}
