package view

import (
	"testing"

	tcell "github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/zhecho/m9s/internal/mail"
)

func pickerKeys(p *AttachmentPicker, keys ...tcell.Key) {
	h := p.InputHandler()
	for _, k := range keys {
		h(tcell.NewEventKey(k, 0, tcell.ModNone), func(tview.Primitive) {})
	}
}

func TestAttachmentPicker(t *testing.T) {
	files := []*mail.Attachment{file("a.pdf", "1"), file("b.jpg", "22")}
	var got []*mail.Attachment
	cancelled := false
	choose := func(f []*mail.Attachment) { got = f }
	cancel := func() { cancelled = true }

	pickerKeys(NewAttachmentPicker(testSkin(), files, true, choose, cancel), tcell.KeyEnter)
	if len(got) != 2 {
		t.Errorf("All files gave %d files, want 2", len(got))
	}
	pickerKeys(NewAttachmentPicker(testSkin(), files, true, choose, cancel), tcell.KeyDown, tcell.KeyEnter)
	if len(got) != 1 || got[0] != files[0] {
		t.Errorf("second row gave %v, want a.pdf", got)
	}
	pickerKeys(NewAttachmentPicker(testSkin(), files, false, choose, cancel), tcell.KeyEnter)
	if len(got) != 1 || got[0] != files[0] {
		t.Errorf("open picker's first row gave %v, want a.pdf", got)
	}
	got = nil
	pickerKeys(NewAttachmentPicker(testSkin(), files, true, choose, cancel), tcell.KeyEscape)
	if !cancelled || got != nil {
		t.Errorf("Esc: cancelled %v, chose %v", cancelled, got)
	}
}
