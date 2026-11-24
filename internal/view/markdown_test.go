package view

import (
	"os"
	"strings"
	"testing"

	"github.com/derailed/tview"
	"github.com/mattn/go-runewidth"

	"github.com/zhecho/m9s/internal/ui"
)

func md(t *testing.T, src string, width int) []mdLine {
	t.Helper()
	skin, _ := ui.LoadSkin("")
	return renderMarkdown(src, skin, width)
}

func mdPlain(lines []mdLine) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, l.plain)
	}
	return out
}

func lineWithText(lines []mdLine, text string) *mdLine {
	for i := range lines {
		if strings.Contains(lines[i].plain, text) {
			return &lines[i]
		}
	}
	return nil
}

func TestMarkdown_headings(t *testing.T) {
	skin, _ := ui.LoadSkin("")
	got := md(t, "# m9s\n\nText.\n\n## Screen\n\n### Keys\n", 80)
	plain := mdPlain(got)
	want := []string{"m9s", "═══", "", "Text.", "", "Screen", "──────", "", "Keys"}
	if strings.Join(plain, "\n") != strings.Join(want, "\n") {
		t.Fatalf("plain =\n%s\nwant\n%s", strings.Join(plain, "\n"), strings.Join(want, "\n"))
	}
	if !strings.Contains(got[0].tagged, skin.ColorName("accent")) || !strings.Contains(got[0].tagged, "::b]") {
		t.Errorf("heading not in accent bold: %q", got[0].tagged)
	}
}

func TestMarkdown_paragraphsReflowToTheWidth(t *testing.T) {
	got := mdPlain(md(t, "one two\nthree four five six\n\nnext", 10))
	want := []string{"one two", "three four", "five six", "", "next"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// Bold and code spans may run over a line break of the source.
func TestMarkdown_inlineAcrossSourceLines(t *testing.T) {
	got := md(t, "Turn on **\"Never ask\nbefore\"** and `m9s\nlogin` now.", 80)
	if len(got) != 1 || got[0].plain != `Turn on "Never ask before" and m9s login now.` {
		t.Fatalf("got %q", mdPlain(got))
	}
	if !strings.Contains(got[0].tagged, `::b]"Never`) {
		t.Errorf("bold lost: %q", got[0].tagged)
	}
}

func TestMarkdown_codeSpansBoldItalicAndEscapes(t *testing.T) {
	skin, _ := ui.LoadSkin("")
	got := md(t, "Press `d` to **delete** [a] *one* message; badge_poll and _two_ \\*stars\\*.", 200)
	l := got[0]
	if l.plain != "Press d to delete [a] one message; badge_poll and two *stars*." {
		t.Fatalf("plain = %q", l.plain)
	}
	if !strings.Contains(l.tagged, "["+skin.ColorName("heading")+"::b]d") {
		t.Errorf("code span not coloured: %q", l.tagged)
	}
	if !strings.Contains(l.tagged, "::b]delete") || !strings.Contains(l.tagged, "::u]one") || !strings.Contains(l.tagged, "::u]two") {
		t.Errorf("bold/italic not rendered: %q", l.tagged)
	}
	if strings.Contains(l.tagged, "::u]poll") {
		t.Errorf("snake_case read as italic: %q", l.tagged)
	}
	if !strings.Contains(l.tagged, "[a[]") {
		t.Errorf("[a] not escaped for tview: %q", l.tagged)
	}
}

func TestMarkdown_links(t *testing.T) {
	got := md(t, "See [Passwords at startup](#passwords-at-startup), [Entra](https://entra.microsoft.com) and <https://x.io>.", 200)
	if got[0].plain != "See Passwords at startup, Entra (https://entra.microsoft.com) and https://x.io." {
		t.Fatalf("plain = %q", got[0].plain)
	}
	if !strings.Contains(got[0].tagged, "::u]Passwords") {
		t.Errorf("link text not underlined: %q", got[0].tagged)
	}
}

func TestMarkdown_listsWrapUnderTheirText(t *testing.T) {
	src := "- **Top bar** — the active account and its user\n  and the folder.\n- second\n  - nested item\n\n1. first\n2. second\n   - under two\n"
	got := mdPlain(md(t, src, 24))
	want := []string{
		"• Top bar — the active",
		"  account and its user",
		"  and the folder.",
		"• second",
		"  ◦ nested item",
		"",
		"1. first",
		"2. second",
		"   • under two",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestMarkdown_tablesAreAligned(t *testing.T) {
	src := "| Key | Action |\n|-----|--------|\n| `m` | Move to a folder |\n| `\\|` | Pipe |\n| ● | wide dot |\n"
	got := md(t, src, 20)
	plain := mdPlain(got)
	if len(got) != 5 {
		t.Fatalf("got %q", plain)
	}
	col := -1
	for i, l := range got {
		if !l.wide {
			t.Errorf("table row %q is wrapped like text", l.plain)
		}
		if strings.Contains(l.plain, "|") && !strings.Contains(l.plain, "│ |") && i != 3 {
			t.Errorf("markdown pipe left in %q", l.plain)
		}
		sep := "│"
		if i == 1 {
			sep = "┼"
		}
		at := runewidth.StringWidth(l.plain[:strings.Index(l.plain, sep)])
		if col >= 0 && at != col {
			t.Errorf("column break at %d in %q, want %d", at, l.plain, col)
		}
		col = at
	}
	if !strings.HasPrefix(plain[0], "Key") || !strings.HasPrefix(plain[1], "───") || !strings.Contains(plain[3], "|") {
		t.Errorf("table = %q", plain)
	}
}

func TestMarkdown_codeIsNeverWrapped(t *testing.T) {
	long := strings.Repeat("x", 100)
	got := md(t, "```text\n"+long+"\n  indented\n```\n", 40)
	if len(got) != 2 || got[0].plain != "  "+long || got[1].plain != "    indented" || !got[0].wide {
		t.Fatalf("got %q", mdPlain(got))
	}
}

func TestMarkdown_blockquote(t *testing.T) {
	got := mdPlain(md(t, "> a note that\n> goes on", 80))
	if len(got) != 1 || got[0] != "│ a note that goes on" {
		t.Fatalf("got %q", got)
	}
}

// The whole README at several widths: text fits, markup is gone and every
// tagged line draws exactly its plain text.
func TestMarkdown_readmeRendersCleanly(t *testing.T) {
	src, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, width := range []int{120, 80, 50} {
		for _, l := range md(t, string(src), width) {
			// Only a word longer than the line (a URL) may run over.
			if w := runewidth.StringWidth(l.plain); !l.wide && w > width && len(strings.Fields(l.plain)) > 2 {
				t.Errorf("width %d: line of %d: %q", width, w, l.plain)
			}
			if tw, pw := tview.TaggedStringWidth(l.tagged), runewidth.StringWidth(l.plain); tw != pw {
				t.Errorf("width %d: tagged draws %d cells, plain is %d: %q / %q", width, tw, pw, l.tagged, l.plain)
			}
			if l.wide {
				continue
			}
			for _, markup := range []string{"**", "](", "```", "\\|", "|---"} {
				if strings.Contains(l.plain, markup) {
					t.Errorf("width %d: markup %q left in %q", width, markup, l.plain)
				}
			}
			if mdHeading.MatchString(l.plain) {
				t.Errorf("width %d: heading marks left in %q", width, l.plain)
			}
		}
	}
}

func TestHelpView_readmeBelowShortcuts(t *testing.T) {
	skin, _ := ui.LoadSkin("")
	km, _ := ui.LoadKeyMap("")
	hv := NewHelpView(skin, nil)
	hv.SetKeys(km)
	if strings.Contains(hv.Text(), "README") {
		t.Fatal("README section shown with no README set")
	}
	hv.SetReadme("# m9s\n\n## Keybindings\n\nSelecting several messages works and this sentence is long enough to wrap.\n")
	hv.render(40)
	text := hv.Text()
	shortcuts, readme := strings.Index(text, "GENERAL"), strings.Index(text, "README")
	if shortcuts < 0 || readme < shortcuts {
		t.Fatalf("want the shortcuts, then README:\n%s", text)
	}
	below := text[readme:]
	if !strings.Contains(below, "Keybindings") || !strings.Contains(below, "Selecting several messages") {
		t.Errorf("README text missing below the shortcuts:\n%s", below)
	}
	for _, l := range strings.Split(below, "\n") {
		if runewidth.StringWidth(l) > 40 {
			t.Errorf("README line wider than the window: %q", l)
		}
	}
	// Laid out again for a new width.
	hv.render(100)
	if !strings.Contains(hv.Text(), "Selecting several messages works and this sentence is long enough to wrap.") {
		t.Errorf("not reflowed at 100:\n%s", hv.Text())
	}
}

// A link whose text is its address without the scheme shows it once.
func TestMarkdown_linkNamedByItsAddress(t *testing.T) {
	got := md(t, "Go to [entra.microsoft.com](https://entra.microsoft.com/) now.", 200)
	if got[0].plain != "Go to entra.microsoft.com now." {
		t.Fatalf("plain = %q", got[0].plain)
	}
}

func TestRenderMarkdown_marksHeadings(t *testing.T) {
	lines := md(t, "# m9s\n\ntext\n\n## Install `m9s` [here](#x)\n\nmore\n\n### From source\n", 80)
	want := map[int]string{1: "m9s", 2: "Install m9s here", 3: "From source"}
	n := 0
	for i, l := range lines {
		if l.level == 0 {
			continue
		}
		n++
		if want[l.level] != l.title {
			t.Errorf("level %d title = %q, want %q", l.level, l.title, want[l.level])
		}
		if i+1 < len(lines) && l.level <= 2 && lines[i+1].level != 0 {
			t.Errorf("rule under %q is marked as a heading", l.title)
		}
	}
	if n != 3 {
		t.Errorf("%d heading lines, want 3", n)
	}
}
