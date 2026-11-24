package mail

import "slices"

// NewestFirst returns envelopes ordered for the mail list: latest date on
// top; messages of the same moment by SortKey, the later arrival first. The
// caller's slice is left as it was.
func NewestFirst(envelopes []*Envelope) []*Envelope {
	out := slices.Clone(envelopes)
	slices.SortStableFunc(out, func(a, b *Envelope) int {
		if c := b.Date.Compare(a.Date); c != 0 {
			return c
		}
		switch {
		case a.SortKey > b.SortKey:
			return -1
		case a.SortKey < b.SortKey:
			return 1
		}
		return 0
	})
	return out
}
