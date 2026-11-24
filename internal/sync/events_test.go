package sync_test

import (
	"testing"

	"github.com/zhecho/m9s/internal/mail"
	msync "github.com/zhecho/m9s/internal/sync"
)

func TestEventTypes_typeAssertions(t *testing.T) {
	var e msync.Event

	e = msync.FoldersUpdated{AccountName: "a", Folders: []*mail.Folder{}}
	if _, ok := e.(msync.FoldersUpdated); !ok {
		t.Fatal("FoldersUpdated does not satisfy Event")
	}

	e = msync.BadgeUpdated{AccountName: "a", FolderName: "INBOX", Unseen: 3, Total: 10}
	if _, ok := e.(msync.BadgeUpdated); !ok {
		t.Fatal("BadgeUpdated does not satisfy Event")
	}

	e = msync.NewMail{AccountName: "a", FolderName: "INBOX"}
	if _, ok := e.(msync.NewMail); !ok {
		t.Fatal("NewMail does not satisfy Event")
	}

	e = msync.DraftSynced{AccountName: "a", DraftID: "123"}
	if _, ok := e.(msync.DraftSynced); !ok {
		t.Fatal("DraftSynced does not satisfy Event")
	}

	e = msync.AccountStatus{AccountName: "a", State: msync.StateFailed, Attempt: 1}
	if _, ok := e.(msync.AccountStatus); !ok {
		t.Fatal("AccountStatus does not satisfy Event")
	}
}
