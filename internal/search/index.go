package search

import (
	"fmt"
	"strconv"

	"github.com/blevesearch/bleve/v2"
	"github.com/zhecho/m9s/internal/mail"
)

// Index provides local full-text search over email messages.
type Index interface {
	Index(folder string, msg *mail.Message) error
	Search(folder, query string) ([]uint32, error)
	Close() error
}

type bleveIndex struct {
	b bleve.Index
}

type doc struct {
	Folder  string `json:"folder"`
	Subject string `json:"subject"`
	From    string `json:"from"`
	Body    string `json:"body"`
}

// Open opens or creates a bleve index at path.
func Open(path string) (Index, error) {
	b, err := bleve.Open(path)
	if err == bleve.ErrorIndexPathDoesNotExist {
		mapping := bleve.NewIndexMapping()
		b, err = bleve.New(path, mapping)
	}
	if err != nil {
		return nil, fmt.Errorf("bleve open %s: %w", path, err)
	}
	return &bleveIndex{b: b}, nil
}

func (idx *bleveIndex) Index(folder string, msg *mail.Message) error {
	d := doc{
		Folder:  folder,
		Subject: msg.Envelope.Subject,
		From:    msg.Envelope.FromDisplay(),
		Body:    msg.PlainText(),
	}
	id := folder + "/" + strconv.FormatUint(uint64(msg.Envelope.UID), 10)
	return idx.b.Index(id, d)
}

func (idx *bleveIndex) Search(folder, query string) ([]uint32, error) {
	q := bleve.NewMatchQuery(query)
	req := bleve.NewSearchRequest(q)
	req.Size = 500
	result, err := idx.b.Search(req)
	if err != nil {
		return nil, fmt.Errorf("bleve search: %w", err)
	}
	prefix := folder + "/"
	var uids []uint32
	for _, hit := range result.Hits {
		if len(hit.ID) <= len(prefix) || hit.ID[:len(prefix)] != prefix {
			continue
		}
		uidStr := hit.ID[len(prefix):]
		uid, err := strconv.ParseUint(uidStr, 10, 32)
		if err == nil {
			uids = append(uids, uint32(uid))
		}
	}
	return uids, nil
}

func (idx *bleveIndex) Close() error {
	return idx.b.Close()
}
