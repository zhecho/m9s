package view

import (
	"strings"
	"testing"

	tcell "github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/zhecho/m9s/internal/mail"
	msync "github.com/zhecho/m9s/internal/sync"
)

var cmdAccounts = []string{"gmail", "work"}

func cmdFolders() []*mail.Folder {
	return []*mail.Folder{
		{Name: "INBOX", Delimiter: "/"},
		{Name: "Sent", Delimiter: "/"},
		{Name: "Drafts", Delimiter: "/"},
		{Name: "Archive", Delimiter: "/", Attributes: []string{`\NoSelect`}},
		{Name: "Archive/2024", Delimiter: "/"},
		{Name: "Archive/2023", Delimiter: "/"},
	}
}

func TestResolveCommand(t *testing.T) {
	tests := []struct {
		text     string
		wantKind cmdKind
		wantName string
	}{
		{"gmail", cmdAccount, "gmail"},
		{"WORK", cmdAccount, "work"},
		{"inbox", cmdFolder, "INBOX"},
		{"2024", cmdFolder, "Archive/2024"},
		{"archive/2023", cmdFolder, "Archive/2023"},
		{"se", cmdFolder, "Sent"},
		{"g", cmdAccount, "gmail"},
		{"q", cmdQuit, ""},
		{"quit", cmdQuit, ""},
		{"QUIT", cmdQuit, ""},
		{"help", cmdHelp, ""},
		{"HELP", cmdHelp, ""},
		{"", cmdNone, ""},
		{"   ", cmdNone, ""},
		{"  inbox  ", cmdFolder, "INBOX"},
	}
	for _, tt := range tests {
		got, err := resolveCommand(tt.text, cmdAccounts, cmdFolders())
		if err != nil {
			t.Errorf("resolveCommand(%q) error: %v", tt.text, err)
			continue
		}
		if got.Kind != tt.wantKind || got.Name != tt.wantName {
			t.Errorf("resolveCommand(%q) = %+v, want kind %d name %q", tt.text, got, tt.wantKind, tt.wantName)
		}
	}
}

func TestResolveCommand_errors(t *testing.T) {
	tests := []struct{ text, wantErr string }{
		{"arch", `"arch" is ambiguous: Archive/2023, Archive/2024`},
		{"nope", `no account or folder "nope"`},
		{"archive", `"archive" is ambiguous: Archive/2023, Archive/2024`}, // the NoSelect parent cannot be opened
	}
	for _, tt := range tests {
		got, err := resolveCommand(tt.text, cmdAccounts, cmdFolders())
		if err == nil {
			t.Errorf("resolveCommand(%q) = %+v, want error %q", tt.text, got, tt.wantErr)
			continue
		}
		if err.Error() != tt.wantErr {
			t.Errorf("resolveCommand(%q) error = %q, want %q", tt.text, err.Error(), tt.wantErr)
		}
	}
}

func TestResolveCommand_accountNamedQBeatsQuit(t *testing.T) {
	got, err := resolveCommand("q", []string{"q", "work"}, cmdFolders())
	if err != nil || got.Kind != cmdAccount || got.Name != "q" {
		t.Errorf("resolveCommand(q) = %+v, %v; an account named q wins over quit", got, err)
	}
}

func TestResolveCommand_accountBeatsFolderOfSameName(t *testing.T) {
	folders := append(cmdFolders(), &mail.Folder{Name: "work", Delimiter: "/"})
	got, err := resolveCommand("work", cmdAccounts, folders)
	if err != nil || got.Kind != cmdAccount || got.Name != "work" {
		t.Errorf("resolveCommand(work) = %+v, %v; the account wins", got, err)
	}
}

func TestResolveCommand_sameDisplayNameInTwoParents(t *testing.T) {
	folders := []*mail.Folder{
		{Name: "A/Notes", Delimiter: "/"},
		{Name: "B/Notes", Delimiter: "/"},
		{Name: "Notes", Delimiter: "/"},
	}
	got, err := resolveCommand("notes", nil, folders)
	if err != nil || got.Name != "Notes" {
		t.Errorf("resolveCommand(notes) = %+v, %v; an exact full name beats shared display names", got, err)
	}
	_, err = resolveCommand("notes", nil, folders[:2])
	if err == nil || err.Error() != `"notes" is ambiguous: A/Notes, B/Notes` {
		t.Errorf("error = %v, want ambiguity between A/Notes and B/Notes", err)
	}
}

func TestResolveCommand_ambiguityListIsCapped(t *testing.T) {
	var folders []*mail.Folder
	for _, n := range []string{"x1", "x2", "x3", "x4", "x5", "x6", "x7"} {
		folders = append(folders, &mail.Folder{Name: n, Delimiter: "/"})
	}
	_, err := resolveCommand("x", nil, folders)
	if err == nil || err.Error() != `"x" is ambiguous: x1, x2, x3, x4, x5, …` {
		t.Errorf("error = %v, want five candidates then an ellipsis", err)
	}
}

func TestSuggestCommand(t *testing.T) {
	tests := []struct{ text, want string }{
		{"in", "INBOX"},
		{"gm", "gmail"},
		{"20", "2023"}, // display names, alphabetical
		{"archive/", "Archive/2023"},
		{"", ""},
		{"zz", ""},
		{"qu", ""}, // quit is not suggested: a slip of Tab+Enter must not exit
		{"he", "help"},
	}
	for _, tt := range tests {
		if got := suggestCommand(tt.text, cmdAccounts, cmdFolders()); got != tt.want {
			t.Errorf("suggestCommand(%q) = %q, want %q", tt.text, got, tt.want)
		}
	}
}

// ---- wired into the app ----

func commandApp(t *testing.T) *RootApp {
	t.Helper()
	ra := promptApp(t, "gmail", "work")
	ra.handleEvent(msync.FoldersUpdated{AccountName: "work", Folders: testFolders("INBOX", "Projects")})
	return ra
}

func TestCommand_colonOpensPrompt(t *testing.T) {
	ra := commandApp(t)
	if ev := sendRune(ra, ':'); ev != nil {
		t.Error(": was not consumed")
	}
	if !ra.promptOpen || !ra.promptCommand {
		t.Fatal(": did not open the command prompt")
	}
	if hints := ra.uiApp.Header.HintsText(); !strings.Contains(hints, "Complete") {
		t.Errorf("command prompt hints lack <tab> Complete:\n%s", hints)
	}
}

func TestCommand_switchesAccount(t *testing.T) {
	ra := commandApp(t)
	ra.startCommand()
	typeInPrompt(ra, "work")
	promptKey(ra, tcell.KeyEnter)

	if ra.promptOpen {
		t.Error("prompt still open")
	}
	if ra.manager.Active().Name != "work" || ra.folders.ActiveAccountName() != "work" {
		t.Errorf("active account = %q / %q, want work", ra.manager.Active().Name, ra.folders.ActiveAccountName())
	}
	if ra.currentFolder == nil || ra.currentFolder.Name != "INBOX" {
		t.Errorf("currentFolder = %+v, want work's INBOX", ra.currentFolder)
	}
}

func TestCommand_opensFolderAndKeepsFocus(t *testing.T) {
	ra := commandApp(t)
	ra.uiApp.SetFocus(ra.maillist)
	ra.startCommand()
	typeInPrompt(ra, "sent")
	promptKey(ra, tcell.KeyEnter)

	if ra.currentFolder == nil || ra.currentFolder.Name != "Sent" {
		t.Fatalf("currentFolder = %+v, want Sent", ra.currentFolder)
	}
	if ra.folders.GetCurrentNode() != ra.folders.NodeForFolder("Sent") {
		t.Error("folder cursor did not move to Sent")
	}
	if ra.uiApp.GetFocus() != tview.Primitive(ra.maillist) {
		t.Error("focus must return to the pane that had it")
	}
}

func TestCommand_unknownShowsError(t *testing.T) {
	ra := commandApp(t)
	ra.startCommand()
	typeInPrompt(ra, "nope")
	promptKey(ra, tcell.KeyEnter)

	if flash := ra.uiApp.StatusBar.FlashText(); !strings.Contains(flash, `no account or folder "nope"`) {
		t.Errorf("flash = %q", flash)
	}
	if ra.promptOpen {
		t.Error("prompt still open after an unknown name")
	}
	if ra.manager.Active().Name != "gmail" || ra.currentFolder.Name != "INBOX" {
		t.Error("an unknown name must change nothing")
	}
}

func TestCommand_emptyEnterJustCloses(t *testing.T) {
	for _, text := range []string{"", "  "} {
		ra := commandApp(t)
		ra.startCommand()
		typeInPrompt(ra, text)
		promptKey(ra, tcell.KeyEnter)
		if ra.promptOpen {
			t.Errorf("%q: prompt still open", text)
		}
		if flash := ra.uiApp.StatusBar.FlashText(); flash != "" {
			t.Errorf("%q: flash = %q, want none", text, flash)
		}
	}
}

func TestCommand_escChangesNothing(t *testing.T) {
	ra := commandApp(t)
	ra.uiApp.SetFocus(ra.preview)
	ra.startCommand()
	typeInPrompt(ra, "work")
	promptKey(ra, tcell.KeyEscape)

	if ra.promptOpen || ra.manager.Active().Name != "gmail" {
		t.Errorf("after Esc: open %v, account %q", ra.promptOpen, ra.manager.Active().Name)
	}
	if ra.uiApp.GetFocus() != tview.Primitive(ra.preview) {
		t.Error("Esc must return focus to the pane that had it")
	}
}

func TestCommand_tabCompletes(t *testing.T) {
	ra := commandApp(t)
	ra.startCommand()
	typeInPrompt(ra, "in")
	if ra.prompt.Ghost() != "BOX" {
		t.Errorf("ghost = %q, want BOX", ra.prompt.Ghost())
	}
	promptKey(ra, tcell.KeyTab)
	if ra.prompt.Text() != "INBOX" {
		t.Errorf("prompt text after Tab = %q, want INBOX", ra.prompt.Text())
	}
}

func TestCommand_suggestsFoldersOfActiveAccountOnly(t *testing.T) {
	ra := commandApp(t)
	ra.startCommand()
	typeInPrompt(ra, "proj") // Projects belongs to work, gmail is active
	if ra.prompt.Ghost() != "" {
		t.Errorf("ghost = %q; folders of another account are not reachable in one step", ra.prompt.Ghost())
	}
}
